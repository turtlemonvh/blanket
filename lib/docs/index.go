package docs

import (
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// MaxChars is the most text Read returns in one piece. A page or section
// larger than this comes back as a table of contents instead -- refused
// and redirected, never truncated (turtlemonvh/blanket#230).
const MaxChars = 12000

// headingWeight is how many times a heading's terms count relative to
// body terms when scoring a section.
const headingWeight = 3

// BM25 parameters, the usual defaults.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// Section is one heading of a doc page and the text under it. Text runs
// from the heading line to the next heading of the same or a higher
// level, so a "##" section includes its "###" subsections. The page's "#"
// title section is the exception: it holds only the opening text before
// the first "##".
type Section struct {
	Page    string // page key, e.g. "api"
	ID      string // GitHub-style anchor with its '#', e.g. "#errors"
	Heading string
	Level   int // 1 for the page title, 2 for "##", 3 for "###"
	Text    string
	// own is the text up to the next heading of any level -- what the
	// index scores, so a match is credited to the section that holds it.
	own string
}

// Result is one search hit.
type Result struct {
	Section
	Score   float64
	Snippet string
}

// View is what Read returns. Exactly one of Full or TOC is set: Full when
// the page or section fits in MaxChars, otherwise TOC lists the sections
// to fetch instead, and Intro carries the text before the first of them.
type View struct {
	Page    string
	Section *Section // nil for a whole page
	Full    string
	Intro   string
	TOC     []Section
}

// Index is an in-memory BM25 index over every section of every page.
type Index struct {
	sections []Section
	byPage   map[string][]int // page key -> indexes into sections, in order
	tf       []map[string]float64
	length   []float64
	avgLen   float64
	df       map[string]int
}

type indexState struct {
	once sync.Once
	idx  *Index
	err  error
}

var state = &indexState{}

// Warm builds the shared index in the background so the first search
// does not pay for it. Call it at startup, after SetFS.
func Warm() {
	go defaultIndex()
}

// defaultIndex returns the shared index, building it on first use.
// Callers that arrive while a build is running wait for it (sync.Once)
// rather than seeing an empty index.
func defaultIndex() (*Index, error) {
	mu.RLock()
	st, f := state, files
	mu.RUnlock()
	st.once.Do(func() {
		if f == nil {
			st.err = fmt.Errorf("docs filesystem not initialized; call docs.SetFS at startup")
			return
		}
		st.idx, st.err = BuildIndex(f)
	})
	return st.idx, st.err
}

// Search returns the n best-matching sections for query across all pages.
func Search(query string, n int) ([]Result, error) {
	idx, err := defaultIndex()
	if err != nil {
		return nil, err
	}
	return idx.Search(query, n), nil
}

// Read returns a page (section == "") or one section of it, or its table
// of contents if the text is over MaxChars.
func Read(page, section string) (View, error) {
	if _, ok := pages[page]; !ok {
		return View{}, unknownPageError(page)
	}
	idx, err := defaultIndex()
	if err != nil {
		return View{}, err
	}
	return idx.Read(page, section)
}

// BuildIndex parses and indexes every page in the page map from f.
func BuildIndex(f fs.FS) (*Index, error) {
	idx := &Index{byPage: map[string][]int{}, df: map[string]int{}}
	for _, key := range Keys() {
		b, err := fs.ReadFile(f, pages[key])
		if err != nil {
			return nil, err
		}
		for _, s := range splitSections(key, string(b)) {
			idx.byPage[key] = append(idx.byPage[key], len(idx.sections))
			idx.sections = append(idx.sections, s)
		}
	}
	var total float64
	for _, s := range idx.sections {
		tf := map[string]float64{}
		for _, t := range tokenize(s.Heading) {
			tf[t] += headingWeight
		}
		for _, t := range tokenize(s.own) {
			tf[t]++
		}
		var l float64
		for t, c := range tf {
			l += c
			idx.df[t]++
		}
		idx.tf = append(idx.tf, tf)
		idx.length = append(idx.length, l)
		total += l
	}
	if len(idx.sections) > 0 {
		idx.avgLen = total / float64(len(idx.sections))
	}
	return idx, nil
}

// Search ranks sections against query by BM25 and returns the top n.
func (idx *Index) Search(query string, n int) []Result {
	terms := uniq(tokenize(query))
	N := float64(len(idx.sections))
	var res []Result
	for i, tf := range idx.tf {
		var score float64
		for _, t := range terms {
			c := tf[t]
			if c == 0 {
				continue
			}
			df := float64(idx.df[t])
			idf := math.Log(1 + (N-df+0.5)/(df+0.5))
			score += idf * c * (bm25K1 + 1) / (c + bm25K1*(1-bm25B+bm25B*idx.length[i]/idx.avgLen))
		}
		if score > 0 {
			res = append(res, Result{Section: idx.sections[i], Score: score})
		}
	}
	sort.SliceStable(res, func(a, b int) bool { return res[a].Score > res[b].Score })
	if len(res) > n {
		res = res[:n]
	}
	for i := range res {
		res[i].Snippet = snippet(res[i].own, terms, 200)
	}
	return res
}

// Read returns page (section == "") or one of its sections, whole if it
// fits in MaxChars and as a table of contents otherwise.
func (idx *Index) Read(page, section string) (View, error) {
	secs := idx.byPage[page]
	if section == "" {
		var text strings.Builder
		var toc []Section
		for _, i := range secs {
			s := idx.sections[i]
			text.WriteString(s.own)
			if s.Level > 1 {
				toc = append(toc, s)
			}
		}
		v := View{Page: page}
		if text.Len() <= MaxChars || len(toc) == 0 {
			v.Full = text.String()
		} else {
			v.TOC = toc
			if len(secs) > 0 && idx.sections[secs[0]].Level == 1 {
				v.Intro = idx.sections[secs[0]].own
			}
		}
		return v, nil
	}

	id := "#" + strings.TrimPrefix(section, "#")
	for n, i := range secs {
		s := idx.sections[i]
		if s.ID != id {
			continue
		}
		v := View{Page: page, Section: &s}
		var toc []Section
		for _, j := range secs[n+1:] {
			if c := idx.sections[j]; c.Level > s.Level && s.Level > 1 {
				toc = append(toc, c)
			} else {
				break
			}
		}
		// A section with nothing smaller to point at comes back whole
		// even if it is over the cap: refusing it would leave it
		// unreadable.
		if len(s.Text) <= MaxChars || len(toc) == 0 {
			v.Full = s.Text
		} else {
			v.TOC = toc
			v.Intro = s.own
		}
		return v, nil
	}
	return View{}, fmt.Errorf("page %q has no section %q; call blanket_docs(page=%q) for its sections", page, id, page)
}

// Text renders v as plain text for the blanket_docs MCP tool.
func (v View) Text() string {
	if v.Full != "" {
		return v.Full
	}
	var b strings.Builder
	what := fmt.Sprintf("page %q", v.Page)
	if v.Section != nil {
		what = fmt.Sprintf("section %s of %s", v.Section.ID, what)
	}
	fmt.Fprintf(&b, "%s is over %d characters, so here are its sections instead. Fetch one with page=%q, section=\"<id>\".\n\n", what, MaxChars, v.Page)
	for _, s := range v.TOC {
		fmt.Fprintf(&b, "%s%s  %s  (%d chars)\n", strings.Repeat("  ", s.Level-2), s.ID, s.Heading, len(s.Text))
	}
	if strings.TrimSpace(v.Intro) != "" {
		b.WriteString("\n---\n\n")
		b.WriteString(v.Intro)
	}
	return b.String()
}

// splitSections cuts a markdown page at its "#", "##" and "###"
// headings, skipping lines inside fenced code blocks.
func splitSections(page, md string) []Section {
	type head struct {
		level, start int
		text         string
	}
	var heads []head
	fenced := false
	pos := 0
	for _, line := range strings.SplitAfter(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
		} else if !fenced {
			for level := 1; level <= 3; level++ {
				prefix := strings.Repeat("#", level) + " "
				if strings.HasPrefix(line, prefix) {
					heads = append(heads, head{level, pos, strings.TrimSpace(line[len(prefix):])})
					break
				}
			}
		}
		pos += len(line)
	}

	var out []Section
	// Text before any heading (rare: pages open with a "#" title) gets
	// an untitled level-1 section so Read can reassemble the page.
	if len(heads) == 0 || heads[0].start > 0 {
		end := len(md)
		if len(heads) > 0 {
			end = heads[0].start
		}
		if strings.TrimSpace(md[:end]) != "" {
			out = append(out, Section{Page: page, ID: "#", Level: 1, Text: md[:end], own: md[:end]})
		}
	}
	seen := map[string]int{}
	for i, h := range heads {
		ownEnd, end := len(md), len(md)
		if i+1 < len(heads) {
			ownEnd = heads[i+1].start
		}
		for _, n := range heads[i+1:] {
			if n.level <= h.level || h.level == 1 {
				end = n.start
				break
			}
		}
		id := anchor(h.text)
		if c := seen[id]; c > 0 {
			seen[id]++
			id = fmt.Sprintf("%s-%d", id, c)
		} else {
			seen[id] = 1
		}
		out = append(out, Section{
			Page: page, ID: "#" + id, Heading: h.text, Level: h.level,
			Text: md[h.start:end], own: md[h.start:ownEnd],
		})
	}
	return out
}

// anchor is GitHub's heading-to-anchor rule: lowercase, drop everything
// but letters, digits, spaces, '-' and '_', then spaces become '-'.
func anchor(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

// stopWords are dropped from both pages and queries. Without them a
// question phrased as a question ("how do I ...") ranks short sections by
// how often they say "i" (from "i.e.") and "do".
var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be by can do does for
		from how i if in is it its me my of on or so that the this to what when
		where which why with you your`) {
		stopWords[w] = true
	}
}

// tokenize lowercases s, splits it into runs of letters and digits, and
// drops stop words.
func tokenize(s string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if !stopWords[t] {
			out = append(out, t)
		}
	}
	return out
}

func uniq(ts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range ts {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// snippet returns about width characters of text around the first query
// term in it, whitespace collapsed, skipping the heading line.
func snippet(text string, terms []string, width int) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 && strings.HasPrefix(text, "#") {
		text = text[i+1:]
	}
	text = strings.Join(strings.Fields(text), " ")
	lower := strings.ToLower(text)
	start := -1
	for _, t := range terms {
		if i := strings.Index(lower, t); i >= 0 && (start < 0 || i < start) {
			start = i
		}
	}
	if start < 0 {
		start = 0
	}
	start = max(0, start-width/4)
	if start > 0 {
		if sp := strings.IndexByte(text[start:], ' '); sp >= 0 {
			start += sp + 1
		}
	}
	end := min(len(text), start+width)
	for end < len(text) && end > start && text[end]&0xC0 == 0x80 {
		end-- // don't split a UTF-8 sequence
	}
	out := text[start:end]
	if start > 0 {
		out = "..." + out
	}
	if end < len(text) {
		out += "..."
	}
	return out
}
