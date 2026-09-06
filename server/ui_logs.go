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
//     second shape of the public log API.
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
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/manucorporat/sse"
	log "github.com/sirupsen/logrus"
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
type TaskLogLine struct {
	Stream string
	Text   string
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
	// Grouped means the `both` view is showing all of stdout and then all
	// of stderr rather than a true interleaving. The two files carry no
	// shared ordering once written, so a finished task's `both` view can
	// only group; a live one interleaves by arrival.
	Grouped bool
	// Truncated means at least one file had earlier lines than the tail
	// shown here.
	Truncated bool
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
		case LogStreamStderr:
			v.SseUrl = fmt.Sprintf("/task/%s/log?stream=stderr", idHex)
		default:
			v.SseUrl = fmt.Sprintf("/task/%s/log", idHex)
		}
		return v
	}

	var stdoutLines, stderrLines []TaskLogLine
	if stream != LogStreamStderr {
		lines, truncated := storedLogLines(task, TaskStdoutLogFile, LogStreamStdout)
		stdoutLines = lines
		v.Truncated = v.Truncated || truncated
	}
	if stream != LogStreamStdout {
		lines, truncated := storedLogLines(task, TaskStderrLogFile, LogStreamStderr)
		stderrLines = lines
		v.Truncated = v.Truncated || truncated
	}
	v.Lines = append(append([]TaskLogLine{}, stdoutLines...), stderrLines...)
	v.Grouped = stream == LogStreamBoth && len(stdoutLines) > 0 && len(stderrLines) > 0

	return v
}

// storedLogLines reads the tail of one of a task's log files. A missing
// file is not an error -- a task that never ran, or one that wrote to only
// one of the two streams, is a normal case.
func storedLogLines(task tasks.Task, filename, stream string) ([]TaskLogLine, bool) {
	content, truncated, err := tailLinesTruncated(path.Join(task.ResultDir, filename), DEFAULT_LOG_TAIL_LINES)
	if err != nil || content == "" {
		return nil, false
	}
	raw := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	lines := make([]TaskLogLine, 0, len(raw))
	for _, l := range raw {
		lines = append(lines, TaskLogLine{Stream: stream, Text: l})
	}
	return lines, truncated
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

// uiTaskLogStream answers GET /ui/sse/tasks/:id/log: both of a running
// task's log files, interleaved in arrival order, as `message` SSE frames
// carrying one pre-rendered line each.
//
// This is the UI's counterpart to the structured NDJSON stream (whose log
// events carry the same stdout/stderr discriminator as a JSON field). The
// UI can't consume that one without a JSON parser in the browser, and the
// point of this UI is that there isn't one -- so the discriminator is
// rendered server-side into a badge instead.
//
// Shaped like streamLog (serve_logs.go): same idle window, same
// shutdown frame, same "stop once the task is terminal and the lines have
// stopped coming" rule. It differs only in following two files at once,
// which is why it isn't a call into that helper.
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

	var stdout, stderr *logTail
	defer func() {
		stopTail(stdout)
		stopTail(stderr)
	}()

	shutdown := s.shutdownChan()
	mult := s.timeMultiplier()
	idle := time.Duration(float64(time.Second*LOGLINE_WAIT_DURATION) * mult)

	lineno := 0
	emit := func(stream, line string) {
		lineno++
		c.Writer.Header()["Content-Type"] = []string{ContentTypeSSE}
		sse.Encode(c.Writer, sse.Event{
			Id:    strconv.Itoa(lineno),
			Event: "message",
			Data:  renderLogLineHTML(stream, line),
		})
	}

	// The worker creates both files in SetupExecutionDirectory, so a task
	// that hasn't been claimed yet has neither: attaching is retried on
	// every wake until it works, exactly as the structured stream does.
	attach := func() bool {
		cur, err := s.DB.GetTask(taskId)
		if err != nil {
			return true
		}
		if tailAttachable(cur.State) {
			if stdout == nil {
				stdout = followTail(cur, TaskStdoutLogFile, LogStreamStdout)
			}
			if stderr == nil {
				stderr = followTail(cur, TaskStderrLogFile, LogStreamStderr)
			}
		}
		return tasks.IsTerminalState(cur.State)
	}

	c.Stream(func(w io.Writer) bool {
		terminal := attach()

		var outCh, errCh <-chan string
		if stdout != nil {
			outCh = stdout.sub.NewLines
		}
		if stderr != nil {
			errCh = stderr.sub.NewLines
		}

		timer := time.NewTimer(idle)
		defer timer.Stop()

		select {
		case <-shutdown:
			c.Writer.Header()["Content-Type"] = []string{ContentTypeSSE}
			writeServerRestarting(w)
			return false
		case line, ok := <-outCh:
			if !ok {
				stdout = nil
				return true
			}
			emit(LogStreamStdout, line)
			return true
		case line, ok := <-errCh:
			if !ok {
				stderr = nil
				return true
			}
			emit(LogStreamStderr, line)
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
