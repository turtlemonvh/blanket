package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Log search: page through a log file, or grep it with context lines,
// without ever holding the whole file in memory (turtlemonvh/blanket#231).
//
// Written as a plain function over a file path rather than inside the
// blanket_task_logs MCP handler so a REST endpoint (GET /task/:id/log?grep=)
// or the UI can reuse it later -- the MCP tool is just the first caller.
//
// Two memory bounds hold however large the file is:
//
//   - Every line is read in chunks and only its first logSearchMatchBytes
//     are kept, so one pathological 50MB line costs 64KiB, not 50MB.
//   - Only the requested page is kept. A negative offset ("the last 50
//     lines", "the last 3 matches") is resolved with a counting pass first
//     rather than a ring buffer, because a ring buffer sized by |offset|
//     is unbounded when a caller asks for offset=-1000000, limit=10.

const (
	// logSearchMatchBytes is how much of each line is kept, and so how
	// much of it a grep can match against. Text past this is counted for
	// the truncation marker but never matched.
	logSearchMatchBytes = 64 * 1024

	// LogSearchDefaultContext / LogSearchMaxContext bound grep's -C.
	LogSearchDefaultContext = 2
	LogSearchMaxContext     = 10
)

// ErrLogPageTooLarge is returned, wrapped with the numbers, when a page
// would exceed LogSearchOptions.MaxPageChars. The search refuses rather
// than cutting the page short: a silently truncated page reads as "that
// was everything", which is the wrong conclusion to hand a debugger.
var ErrLogPageTooLarge = errors.New("log page too large")

// LogSearchOptions configures SearchLog. Limit must be positive.
type LogSearchOptions struct {
	// Grep, if set, switches to match mode: Offset and Limit then count
	// matches, not lines, and each match carries Context lines either
	// side (capped at LogSearchMaxContext).
	Grep    *regexp.Regexp
	Context int

	// Offset is 0-based. A negative Offset counts back from the end, so
	// Offset=-50 is the last 50 lines (or matches).
	Offset int
	Limit  int

	// MaxLineChars truncates each returned line to this many characters,
	// with a "…[+N chars]" marker. 0 means no per-line cap.
	MaxLineChars int
	// MaxPageChars refuses (ErrLogPageTooLarge) a page whose returned
	// text would exceed this many characters. 0 means no cap.
	MaxPageChars int
}

// LogLine is one returned line. Num is 1-based, as grep -n prints it.
type LogLine struct {
	Num   int
	Text  string
	Match bool

	// dropped is how many bytes past Text eachLogLine discarded unread,
	// so the truncation marker reports the line's real length.
	dropped int
}

// LogSearchResult is one page.
type LogSearchResult struct {
	Lines      []LogLine
	TotalLines int
	// TotalMatches is only meaningful in grep mode.
	TotalMatches int
	// Start is the resolved (non-negative) offset of the first line or
	// match in the page; Count is how many lines or matches it holds.
	Start, Count int
	// NextOffset is the Offset to ask for the following page, or -1 if
	// this page reaches the end.
	NextOffset int
}

// SearchLog reads path line by line and returns one page of it.
func SearchLog(path string, opts LogSearchOptions) (*LogSearchResult, error) {
	if opts.Limit <= 0 {
		return nil, fmt.Errorf("limit must be positive")
	}
	if opts.Context < 0 {
		opts.Context = 0
	}
	if opts.Context > LogSearchMaxContext {
		opts.Context = LogSearchMaxContext
	}

	start := opts.Offset
	if start < 0 {
		lines, matches, err := countLog(path, opts.Grep)
		if err != nil {
			return nil, err
		}
		total := lines
		if opts.Grep != nil {
			total = matches
		}
		start = max(total+opts.Offset, 0)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	p := &logPager{opts: opts, start: start, res: &LogSearchResult{Start: start, NextOffset: -1}}
	if err := eachLogLine(f, p.line); err != nil {
		return nil, err
	}
	if p.tooLarge {
		advice := "use a smaller limit"
		if opts.Grep != nil {
			advice = "use a narrower grep, less context, or a smaller limit"
		}
		return nil, fmt.Errorf("%w: it would be over %d characters; %s",
			ErrLogPageTooLarge, opts.MaxPageChars, advice)
	}

	res := p.res
	total := res.TotalLines
	if opts.Grep != nil {
		total = res.TotalMatches
	}
	if next := start + res.Count; next < total {
		res.NextOffset = next
	}
	return res, nil
}

// logPager is SearchLog's per-line state.
type logPager struct {
	opts  LogSearchOptions
	start int
	res   *LogSearchResult

	chars    int
	tooLarge bool

	// grep mode only
	before    []LogLine // the last Context unemitted lines
	afterLeft int       // trailing context lines still owed
	lastNum   int       // last emitted line number
}

func (p *logPager) emit(l LogLine) {
	if p.tooLarge {
		return
	}
	l.Text = truncateLogLine(l.Text, l.dropped, p.opts.MaxLineChars)
	p.chars += utf8.RuneCountInString(l.Text) + 1
	if p.opts.MaxPageChars > 0 && p.chars > p.opts.MaxPageChars {
		p.tooLarge = true
		p.res.Lines = nil
		return
	}
	p.res.Lines = append(p.res.Lines, l)
	p.lastNum = l.Num
}

// line is eachLogLine's callback: called once per line with its 1-based
// number, kept text, and how many bytes past the kept text were dropped.
func (p *logPager) line(num int, text string, dropped int) {
	p.res.TotalLines = num
	l := LogLine{Num: num, Text: text, dropped: dropped}

	if p.opts.Grep == nil {
		idx := num - 1
		if idx >= p.start && idx < p.start+p.opts.Limit {
			p.emit(l)
			p.res.Count++
		}
		return
	}

	l.Match = p.opts.Grep.MatchString(text)
	selected := false
	if l.Match {
		idx := p.res.TotalMatches
		p.res.TotalMatches++
		selected = idx >= p.start && idx < p.start+p.opts.Limit
	}

	switch {
	case selected:
		for _, b := range p.before {
			if b.Num > p.lastNum {
				p.emit(b)
			}
		}
		p.before = p.before[:0]
		p.emit(l)
		p.res.Count++
		p.afterLeft = p.opts.Context
		return
	case p.afterLeft > 0:
		// Trailing context; a match past the page still shows here
		// (marked as a match, as grep would), it just isn't counted.
		p.emit(l)
		p.afterLeft--
		return
	}

	if p.opts.Context > 0 && p.res.Count < p.opts.Limit {
		if len(p.before) == p.opts.Context {
			copy(p.before, p.before[1:])
			p.before = p.before[:len(p.before)-1]
		}
		p.before = append(p.before, l)
	}
}

// countLog is the counting pass a negative offset needs.
func countLog(path string, re *regexp.Regexp) (lines, matches int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	err = eachLogLine(f, func(num int, text string, _ int) {
		lines = num
		if re != nil && re.MatchString(text) {
			matches++
		}
	})
	return lines, matches, err
}

// eachLogLine calls fn for every line of r, keeping at most
// logSearchMatchBytes of each and reporting how many bytes it dropped.
// bufio.Scanner is not used because it fails outright on a line longer
// than its buffer, and logs do contain those.
func eachLogLine(r io.Reader, fn func(num int, text string, dropped int)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var buf []byte
	dropped := 0
	num := 0
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(chunk) > 0 {
			room := max(logSearchMatchBytes-len(buf), 0)
			if room >= len(chunk) {
				buf = append(buf, chunk...)
			} else {
				buf = append(buf, chunk[:room]...)
				dropped += len(chunk) - room
			}
		}
		if err == io.EOF {
			// A final line with no trailing newline still counts.
			if len(buf) > 0 || dropped > 0 {
				num++
				fn(num, strings.TrimSuffix(string(buf), "\r"), dropped)
			}
			return nil
		}
		if err != nil {
			return err
		}
		if isPrefix {
			continue
		}
		num++
		fn(num, strings.TrimSuffix(string(buf), "\r"), dropped)
		buf = buf[:0]
		dropped = 0
	}
}

// truncateLogLine cuts s to max characters (runes), appending
// "…[+N chars]" for whatever it removed plus the dropped bytes
// eachLogLine never read into s (counted as bytes -- it never decoded
// them). max 0 means no cap.
func truncateLogLine(s string, dropped, max int) string {
	extra := dropped
	if max > 0 && len(s) > max { // byte length >= rune count: cheap pre-check
		n := 0
		for i := range s {
			if n == max {
				extra += utf8.RuneCountInString(s[i:])
				s = s[:i]
				break
			}
			n++
		}
	}
	if extra > 0 {
		return fmt.Sprintf("%s…[+%d chars]", s, extra)
	}
	return s
}
