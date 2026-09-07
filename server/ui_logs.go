package server

// The task detail page's log pane and result block -- turtlemonvh/blanket#104.
//
// Two additions to /ui/tasks/<id>, both server-rendered:
//
//   - a stdout / stderr / both toggle over the log pane. The default view
//     is byte-for-byte what it always was: the raw stdout SSE stream at
//     GET /task/:id/log for a running task, the stored tail otherwise. The
//     other two views reuse the same plumbing -- `stderr` is the same raw
//     stream pointed at the other file (?stream=stderr), and `both` is the
//     one case that needs its own route, because interleaving two streams
//     into one pane only makes sense if each line says which stream it
//     came from. That route (GET /ui/sse/tasks/:id/log) is UI-only and
//     emits pre-rendered, escaped HTML fragments, which is why it isn't a
//     second shape of the public log API. It replays what the task has
//     already written before it starts following, so switching views on
//     a running task shows its output from the start rather than from the
//     moment you switched -- and it replays it in the order the task
//     produced it, reading the worker's combined record
//     (lib/combined_log) rather than merging the two per-stream files,
//     which carry no shared ordering. A task with no combined record
//     (an older worker, or `workers.combinedLog = false`) still gets the
//     grouped stdout-then-stderr replay, labelled as such.
//
//   - the parsed `result_file` artifact and its resultError, read through
//     the same reader POST /task/?wait uses (readTaskResult in
//     serve_sync.go), so the page and the API can never disagree about
//     containment or the size cap.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/manucorporat/sse"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/combined_log"
	"github.com/turtlemonvh/blanket/lib/tailed_file"
	"github.com/turtlemonvh/blanket/tasks"
)

// LogStreamBoth is the UI-only third value of the log pane's toggle. The
// raw log routes reject it (see rawLogStreamFor): an undecorated byte
// stream has no way to say which of the two files a line came from.
const LogStreamBoth = "both"

const (
	// maxResultDisplayBytes caps how much of a parsed result artifact the
	// detail page prints. The reader's own tasks.sync.maxResultBytes cap
	// (1 MiB by default) is about what the server is willing to read; this
	// is about what a browser should be asked to lay out. Beyond it the
	// block shows the head and links the raw file under /results/.
	maxResultDisplayBytes = 20000
	// resultCollapseLines is when the result block renders as a closed
	// <details> instead of an open one. Short artifacts -- the common case,
	// a handful of fields -- stay visible without a click.
	resultCollapseLines = 40
)

/*
 * The log pane
 */

// TaskLogLine is one stored log line rendered into the pane. Stream is
// LogStreamStdout or LogStreamStderr; it is only shown as a badge in the
// `both` view, where the two are mixed together.
//
// Meta marks the entry as a note *about* the output rather than a line of
// it -- the "earlier lines omitted" marker that heads a truncated block.
// It carries its own text and Stream/Text are then empty, so the note can
// sit inside the sequence it describes instead of only at the top of the
// pane.
type TaskLogLine struct {
	Stream string
	Text   string
	Meta   string
}

// TaskLogOption is one button of the stdout / stderr / both toggle.
type TaskLogOption struct {
	Value  string
	Label  string
	Active bool
}

// TaskLogView is what task_log.html renders: the toggle, plus either a
// live stream to attach to (Live) or the stored tail to print (Lines).
type TaskLogView struct {
	TaskId string
	// Stream is the selected view: LogStreamStdout, LogStreamStderr or
	// LogStreamBoth.
	Stream  string
	Options []TaskLogOption

	// Live means the task is running, so the pane starts empty and fills
	// from SseUrl instead of from Lines.
	Live bool
	// SseUrl is the stream the pane connects to when Live. For the two
	// single-stream views this is the public raw log route, unchanged; for
	// `both` it is the UI's own interleaving route.
	SseUrl string

	// Lines is the stored tail, for a task that isn't running.
	Lines []TaskLogLine
	// Badged means each line is prefixed with its stream name -- true only
	// in the `both` view, where lines from the two files are mixed.
	Badged bool
	// Grouped means the `both` view is falling back to grouping history
	// by stream -- all of stdout, then all of stderr -- because the task
	// has no combined record to interleave from. That is the case for a
	// task run before turtlemonvh/blanket#104, or by a worker with
	// `workers.combinedLog = false`: the two per-stream files carry no
	// shared ordering, so grouping is the only honest rendering of them.
	// With the record present this stays false and the pane shows real
	// time order, the same one live lines arrive in.
	Grouped bool
}

// Empty reports whether there is nothing to print. Used by the template
// to show a placeholder rather than an empty black box.
func (v TaskLogView) Empty() bool { return !v.Live && len(v.Lines) == 0 }

// normalizeLogStream maps a `stream` query value onto one of the three
// views, defaulting to stdout. Unknown values fall back rather than
// erroring: this is a UI toggle, and the worst case is showing the
// default view.
func normalizeLogStream(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case LogStreamStderr:
		return LogStreamStderr
	case LogStreamBoth:
		return LogStreamBoth
	default:
		return LogStreamStdout
	}
}

// buildTaskLogView assembles the pane for one task and one selected view.
//
// A RUNNING task streams (exactly as the page did before this toggle
// existed); anything else prints the tail already on disk. That split is
// deliberately the same predicate the page used before, so the default
// view's behaviour is unchanged in both directions.
func buildTaskLogView(task tasks.Task, rawStream string) TaskLogView {
	stream := normalizeLogStream(rawStream)
	idHex := task.Id.Hex()

	v := TaskLogView{
		TaskId: idHex,
		Stream: stream,
		Live:   task.State == "RUNNING",
		Badged: stream == LogStreamBoth,
	}
	for _, o := range []struct{ value, label string }{
		{LogStreamStdout, "stdout"},
		{LogStreamStderr, "stderr"},
		{LogStreamBoth, "both"},
	} {
		v.Options = append(v.Options, TaskLogOption{
			Value:  o.value,
			Label:  o.label,
			Active: o.value == stream,
		})
	}

	if v.Live {
		switch stream {
		case LogStreamBoth:
			v.SseUrl = fmt.Sprintf("/ui/sse/tasks/%s/log", idHex)
			// The stream replays the backlog before it starts following.
			// Whether that backlog is in time order or grouped by stream
			// depends on the same thing the stored pane's does: whether
			// the worker recorded the interleaving.
			v.Grouped = liveHistoryIsGrouped(task)
		case LogStreamStderr:
			v.SseUrl = fmt.Sprintf("/task/%s/log?stream=stderr", idHex)
		default:
			v.SseUrl = fmt.Sprintf("/task/%s/log", idHex)
		}
		return v
	}

	// The combined record is one file already in time order, so the
	// stored pane is a straight read of its tail -- exactly what the live
	// stream replays, through the same cap.
	if stream == LogStreamBoth {
		if lines, ok := storedCombinedLines(task); ok {
			v.Lines = lines
			return v
		}
	}

	var stdoutLines, stderrLines []TaskLogLine
	if stream != LogStreamStderr {
		stdoutLines = storedLogLines(task, TaskStdoutLogFile, LogStreamStdout)
	}
	if stream != LogStreamStdout {
		stderrLines = storedLogLines(task, TaskStderrLogFile, LogStreamStderr)
	}
	v.Lines = append(append([]TaskLogLine{}, stdoutLines...), stderrLines...)
	v.Grouped = stream == LogStreamBoth && len(stdoutLines) > 0 && len(stderrLines) > 0

	return v
}

// storedLogLines reads the tail of one of a task's log files, headed by an
// "earlier lines omitted" note when the file held more than the cap. A
// missing file is not an error -- a task that never ran, or one that wrote
// to only one of the two streams, is a normal case.
//
// The cap is the same one the live combined stream replays with
// (uiLogHistoryLines), so a task's pane shows the same window of output
// whether it is still running or already finished.
func storedLogLines(task tasks.Task, filename, stream string) []TaskLogLine {
	content, truncated, err := tailLinesTruncated(path.Join(task.ResultDir, filename), uiLogHistoryLines)
	if err != nil || content == "" {
		return nil
	}
	raw := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	lines := make([]TaskLogLine, 0, len(raw)+1)
	if truncated {
		lines = append(lines, TaskLogLine{Meta: logTruncationNote(stream)})
	}
	for _, l := range raw {
		lines = append(lines, TaskLogLine{Stream: stream, Text: l})
	}
	return lines
}

// hasCombinedLog reports whether the worker recorded this task's stream
// interleaving. A task claimed a moment ago may not have it yet -- the
// worker creates it as it sets the execution directory up -- which is
// why the live stream decides again on every attach rather than trusting
// this one read.
func hasCombinedLog(task tasks.Task) bool {
	if task.ResultDir == "" {
		return false
	}
	_, err := os.Stat(path.Join(task.ResultDir, TaskCombinedLogFile))
	return err == nil
}

// liveHistoryIsGrouped decides whether a *running* task's pane should
// carry the "stdout first, then stderr" note, using the same rule the
// stream itself uses when it attaches: the combined record wins, and its
// absence only means anything once the per-stream files exist (the
// worker creates the combined one first). A task claimed a moment ago
// has no files at all yet, and printing a note about grouping that stops
// being true a second later is worse than printing none.
func liveHistoryIsGrouped(task tasks.Task) bool {
	if task.ResultDir == "" || hasCombinedLog(task) {
		return false
	}
	_, err := os.Stat(path.Join(task.ResultDir, TaskStdoutLogFile))
	return err == nil
}

// storedCombinedLines reads the tail of a finished task's combined
// record: every line of its output, in the order it was produced, each
// tagged with the stream it came from.
//
// ok is false when there is no such record to read, which is the signal
// to fall back to the two per-stream files. An *empty* record is a
// different thing -- a task that ran and printed nothing -- and comes
// back ok with no lines, so the pane says "no output" rather than
// silently re-reading files that are equally empty.
func storedCombinedLines(task tasks.Task) ([]TaskLogLine, bool) {
	if !hasCombinedLog(task) {
		return nil, false
	}
	content, truncated, err := tailLinesTruncated(
		path.Join(task.ResultDir, TaskCombinedLogFile), uiLogHistoryLines)
	if err != nil {
		return nil, false
	}

	var lines []TaskLogLine
	if truncated {
		lines = append(lines, TaskLogLine{Meta: logTruncationNote("")})
	}
	if content != "" {
		for _, raw := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
			rec, ok := combined_log.ParseRecord(raw)
			if !ok {
				continue
			}
			lines = append(lines, TaskLogLine{Stream: rec.Stream, Text: rec.Line})
		}
	}
	return lines, true
}

// logTruncationNote is the pane's marker for output older than the window
// it prints. Worded per stream because the fallback view prints two
// windows, one per file, and only one of them may have been cut; the
// combined record is a single window and passes "" for the stream.
func logTruncationNote(stream string) string {
	if stream == "" {
		return "… earlier lines omitted …"
	}
	return fmt.Sprintf("… earlier %s lines omitted …", stream)
}

// uiTaskLogPartial answers GET /ui/partials/task-log?id=<hex>&stream=<view>
// with the log pane. It is what the toggle's buttons fetch; the page's
// first render calls the same template inline, so the default view costs
// no extra request.
func (s *ServerConfig) uiTaskLogPartial(c *gin.Context) {
	taskId, err := SafeObjectId(c.Query("id"))
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.DB.GetTask(taskId)
	if err != nil {
		c.String(statusForDBError(err, http.StatusNotFound), err.Error())
		return
	}

	t := mustParsePartial("task-log", "task_log.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "task-log", buildTaskLogView(task, c.Query("stream"))); err != nil {
		log.WithField("err", err).Warn("ui: render task-log")
	}
}

// renderLogLineHTML is one line of the interleaved stream, as the fragment
// htmx appends to the pane.
//
// The line is HTML-escaped here because this route's whole job is to emit
// markup: the badge has to be an element for the two streams to be
// distinguishable, and htmx's SSE extension swaps event data in as HTML.
// (The raw log route emits the bytes the task wrote, unchanged -- that is
// its contract, and it is not this route.)
func renderLogLineHTML(stream, line string) string {
	return fmt.Sprintf(`<span class="log-line log-line-%s"><span class="log-tag">%s</span>%s</span>`+"\n",
		stream, stream, template.HTMLEscapeString(line))
}

// renderLogMetaHTML is the streamed form of a note about the output -- the
// truncation marker -- matching what task_log.html renders for a stored
// pane so the two look the same.
func renderLogMetaHTML(text string) string {
	return fmt.Sprintf(`<span class="log-meta">%s</span>`+"\n", template.HTMLEscapeString(text))
}

// uiLogHistoryLines is how much of each log file the combined stream
// replays to a client on connect, and how much of it a stored pane prints.
// One constant because switching a task's log view must not change what
// window of its output you can see.
const uiLogHistoryLines = DEFAULT_LOG_TAIL_LINES

// uiLogSource is one file the combined stream follows: either the
// worker's combined record (one tail, every line already tagged and in
// order) or, falling back, one of the two per-stream files.
//
// `done` exists to keep a closed tailer from being re-opened: attach runs
// on every wake, and re-attaching after the tailer went away would replay
// the whole backlog a second time -- duplicating exactly the history this
// route now exists to deliver once.
type uiLogSource struct {
	// stream is the badge every line from this file gets. Empty for the
	// combined record, whose lines each carry their own.
	stream   string
	file     string
	combined bool
	tail     *tailed_file.ReplayTail
	done     bool
}

// lines is the channel to select on, or nil when there is nothing to read
// from. A nil channel blocks forever, which is what a select wants for an
// absent case -- including the second slot in combined mode, where there
// is no second file.
func (u *uiLogSource) lines() <-chan string {
	if u == nil || u.tail == nil || u.done {
		return nil
	}
	return u.tail.Lines
}

func (u *uiLogSource) stop() {
	if u != nil && u.tail != nil {
		u.tail.Stop()
	}
}

// render turns one line of this source into the fragment the pane
// appends. ok is false for a combined record that couldn't be parsed --
// skipped rather than shown raw, since the alternative is printing a
// line of JSON into someone's log pane.
func (u *uiLogSource) render(line string) (string, bool) {
	if !u.combined {
		return renderLogLineHTML(u.stream, line), true
	}
	rec, ok := combined_log.ParseRecord(line)
	if !ok {
		return "", false
	}
	return renderLogLineHTML(rec.Stream, rec.Line), true
}

// uiTaskLogStream answers GET /ui/sse/tasks/:id/log: a running task's
// output as `message` SSE frames carrying one pre-rendered line each --
// what was already on disk when the client connected, then everything
// written after it, all of it in the order the task produced it.
//
// This is the UI's counterpart to the structured NDJSON stream (whose log
// events carry the same stdout/stderr discriminator as a JSON field). The
// UI can't consume that one without a JSON parser in the browser, and the
// point of this UI is that there isn't one -- so the discriminator is
// rendered server-side into a badge instead.
//
// It follows *one* file when it can: the worker's combined record
// (lib/combined_log), which already holds both streams interleaved in
// arrival order, each line tagged. That is what makes replayed history
// and live output look the same -- the review finding this route was
// fixed for a second time. Before it existed there was nothing on disk
// that recorded the interleaving, so the replay could only be a stdout
// block followed by a stderr block while live lines arrived mixed, and
// the same output read differently depending on when you looked.
//
// A task with no combined record -- run before turtlemonvh/blanket#104,
// or by a worker with `workers.combinedLog = false` -- falls back to
// following the two per-stream files and replaying them grouped, which
// is all their contents can honestly support. Which shape applies is
// decided once, on the first attach that finds a file: the worker
// creates the combined record before the two per-stream ones, so "the
// files exist but the combined one doesn't" means it is never coming.
//
// The replay comes from tailed_file.ReplayAndFollow, which reads the file
// itself and starts its tailer at the byte offset the read stopped at, so
// the seam neither drops nor duplicates a line. It does *not* come from
// tailed_file.Follow: a TailedFile is shared between subscribers, and what
// history a late subscriber gets is an accident of who else is already
// watching (see the comment on ReplayAndFollow). That accident is the bug
// this route had first -- toggling stdout / stderr / both keeps both files
// warm, so coming back to the combined view replayed a ring buffer's worth
// of stderr, or none at all.
//
// Otherwise shaped like streamLog (serve_logs.go): same idle window, same
// shutdown frame, same "stop once the task is terminal and the lines have
// stopped coming" rule.
func (s *ServerConfig) uiTaskLogStream(c *gin.Context) {
	taskId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.DB.GetTask(taskId); err != nil {
		c.String(statusForDBError(err, http.StatusNotFound), err.Error())
		return
	}

	combined := &uiLogSource{file: TaskCombinedLogFile, combined: true}
	// The fallback pair, replayed stdout-block-then-stderr-block.
	stdout := &uiLogSource{stream: LogStreamStdout, file: TaskStdoutLogFile}
	stderr := &uiLogSource{stream: LogStreamStderr, file: TaskStderrLogFile}
	defer func() {
		for _, src := range []*uiLogSource{combined, stdout, stderr} {
			src.stop()
		}
	}()

	// The two slots the select below reads from. Which sources fill them
	// is decided on the first attach that finds anything: `a` alone in
	// combined mode, both in the fallback. Nil until then, and a nil
	// source's channel is nil, which a select simply never picks.
	var a, b *uiLogSource

	shutdown := s.shutdownChan()
	mult := s.timeMultiplier()
	idle := time.Duration(float64(time.Second*LOGLINE_WAIT_DURATION) * mult)

	lineno := 0
	emit := func(html string) {
		lineno++
		c.Writer.Header()["Content-Type"] = []string{ContentTypeSSE}
		sse.Encode(c.Writer, sse.Event{
			Id:    strconv.Itoa(lineno),
			Event: "message",
			Data:  html,
		})
	}

	// The worker creates the log files in SetupExecutionDirectory, so a
	// task that hasn't been claimed yet has none of them: attaching is
	// retried on every wake until it works, exactly as the structured
	// stream does. A file's backlog goes out the moment that file
	// attaches.
	attach := func() bool {
		cur, err := s.DB.GetTask(taskId)
		if err != nil {
			return true
		}
		if tailAttachable(cur.State) {
			replayed := false
			// open attaches one source and replays its history, and
			// reports whether it is now attached. Already-attached and
			// finished sources are left alone: re-opening one would
			// replay its whole backlog a second time.
			open := func(src *uiLogSource) bool {
				if src.tail != nil || src.done {
					return false
				}
				rt, terr := tailed_file.ReplayAndFollow(
					path.Join(cur.ResultDir, src.file), uiLogHistoryLines)
				if terr != nil {
					return false
				}
				src.tail = rt
				if rt.Truncated {
					emit(renderLogMetaHTML(logTruncationNote(src.stream)))
					replayed = true
				}
				for _, line := range rt.History {
					if html, ok := src.render(line); ok {
						emit(html)
						replayed = true
					}
				}
				return true
			}

			switch {
			case a == nil:
				// Undecided. The combined record wins when it is there,
				// and it is created first, so its absence next to a
				// present blanket.stdout.log is conclusive.
				if open(combined) {
					a = combined
				} else if openedOut, openedErr := open(stdout), open(stderr); openedOut || openedErr {
					a, b = stdout, stderr
				}
			case a == stdout:
				// Fallback mode, and one of the pair may still be
				// missing (a task that has only created one of them yet).
				open(stdout)
				open(stderr)
			}

			// Push the backlog out now. c.Stream only flushes once its
			// step returns, and this step is about to block for a whole
			// idle window waiting for a live line -- on a quiet task that
			// would leave the pane empty for five seconds after
			// connecting, which reads as exactly the missing history the
			// replay exists to fix.
			if replayed {
				c.Writer.Flush()
			}
		}
		return tasks.IsTerminalState(cur.State)
	}

	c.Stream(func(w io.Writer) bool {
		terminal := attach()

		timer := time.NewTimer(idle)
		defer timer.Stop()

		select {
		case <-shutdown:
			c.Writer.Header()["Content-Type"] = []string{ContentTypeSSE}
			writeServerRestarting(w)
			return false
		case line, ok := <-a.lines():
			if !ok {
				a.done = true
				return true
			}
			if html, rendered := a.render(line); rendered {
				emit(html)
			}
			return true
		case line, ok := <-b.lines():
			if !ok {
				b.done = true
				return true
			}
			if html, rendered := b.render(line); rendered {
				emit(html)
			}
			return true
		case <-timer.C:
			// Nothing arrived for a full idle window. If the task is done
			// there is nothing more coming, so let the connection go.
			return !terminal
		}
	})
}

/*
 * The result artifact
 */

// TaskResultView is the detail page's rendering of the task type's
// declared `result_file`. A nil view means the type declares none, which
// is the case for most task types -- the page then shows no result block
// at all rather than an empty one.
type TaskResultView struct {
	// File is the declared path, relative to the task's result dir.
	File string
	// Href links the raw artifact under /results/, so an over-long or
	// unparseable one is still reachable.
	Href string
	// JSON is the pretty-printed result, "" when there is none to show.
	JSON string
	// Error is the payload's resultError: why a declared artifact could
	// not be turned into a result.
	Error string
	// Missing means the type declared an artifact and the finished task
	// never wrote one.
	Missing bool
	// Truncated means JSON is only the head of the artifact.
	Truncated bool
	// Collapsed asks the template for a closed <details> -- a long
	// artifact shouldn't push the log pane off the screen.
	Collapsed bool
}

// buildTaskResultView reads the task's result artifact for display, or
// returns nil when there is nothing to render.
//
// The read goes through readTaskResult -- the same function
// buildCompletionPayload calls -- so the page is showing exactly what
// POST /task/?wait would have returned, including its containment check
// and its tasks.sync.maxResultBytes cap. Only the *presentation* cap
// (maxResultDisplayBytes) is this file's own.
func buildTaskResultView(task tasks.Task) *TaskResultView {
	rel, pathErr := taskResultFile(task)
	if rel == "" && pathErr == "" {
		// The type declares no result_file (or can no longer be loaded).
		return nil
	}

	v := &TaskResultView{File: rel, Error: pathErr}
	if rel != "" {
		v.Href = "/results/" + task.Id.Hex() + "/" + rel
	}
	if pathErr != "" {
		return v
	}

	result, resultErr := readTaskResult(task, syncMaxResultBytes())
	v.Error = resultErr
	if result == nil {
		if resultErr != "" {
			return v
		}
		// A declared artifact that isn't there. Worth saying so once the
		// task is over -- it usually means the task failed before writing
		// it -- but not while it still has time to appear.
		if !tasks.IsTerminalState(task.State) {
			return nil
		}
		// No file to link: /results/ would 404 on it.
		v.Missing = true
		v.Href = ""
		return v
	}

	pretty, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		// Can't happen for a value that came out of json.Unmarshal, but a
		// silent empty block would be worse than saying so.
		v.Error = fmt.Sprintf("could not format result_file %q for display: %s", rel, err.Error())
		return v
	}

	text := string(pretty)
	if len(text) > maxResultDisplayBytes {
		text = strings.ToValidUTF8(text[:maxResultDisplayBytes], "")
		v.Truncated = true
	}
	v.JSON = text
	v.Collapsed = v.Truncated || strings.Count(text, "\n")+1 > resultCollapseLines

	return v
}
