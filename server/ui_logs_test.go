package server

// Tests for the task detail page's live log streams (turtlemonvh/blanket#104).
//
// Two bugs these exist to hold shut, both found reviewing the same route:
//
//  1. The combined (`both`) view showed only lines that arrived after you
//     opened it. Sitting on it filled the pane, but switching to stdout,
//     then stderr, then back to `both` came back with the stderr history
//     gone -- because the route only attached tailers and never read the
//     files, and how much history a tailed_file subscriber gets depends
//     on whether anything else is already watching the same path.
//  2. The history it then replayed was grouped (all of stdout, then all
//     of stderr) while live lines interleaved, so the same output looked
//     different depending on whether you were watching when it happened.
//     It now follows the worker's combined record instead, which holds
//     both streams in arrival order -- and only falls back to the grouped
//     two-file replay for a task that has no such record.
//
// These need a real listener rather than httptest.NewRecorder: gin's
// c.Stream calls CloseNotify() on the ResponseWriter, which a recorder
// doesn't implement.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/combined_log"
	"github.com/turtlemonvh/blanket/tasks"
)

// logEntry is one line of a task's combined record, as the worker would
// have written it.
type logEntry struct {
	stream string
	line   string
}

// combinedLogBody renders entries as the NDJSON the worker writes.
func combinedLogBody(entries []logEntry) string {
	var b strings.Builder
	for i, e := range entries {
		rec, err := json.Marshal(combined_log.Record{
			Ts:     time.Now().UnixMilli(),
			Stream: e.stream,
			Seq:    i + 1,
			Line:   e.line,
		})
		if err != nil {
			panic(err)
		}
		b.Write(rec)
		b.WriteString("\n")
	}
	return b.String()
}

// runningCombinedLogTask is runningLogTask for a worker that recorded the
// interleaving: the two per-stream files are still written (nothing
// stopped doing that), plus the combined record that says what order the
// lines actually came in.
func runningCombinedLogTask(t *testing.T, s *ServerConfig, entries ...logEntry) tasks.Task {
	t.Helper()
	var stdout, stderr strings.Builder
	for _, e := range entries {
		if e.stream == LogStreamStderr {
			stderr.WriteString(e.line + "\n")
		} else {
			stdout.WriteString(e.line + "\n")
		}
	}
	tsk := runningLogTask(t, s, stdout.String(), stderr.String())
	require.NoError(t, os.WriteFile(
		filepath.Join(tsk.ResultDir, TaskCombinedLogFile),
		[]byte(combinedLogBody(entries)), 0644))
	return tsk
}

// runningLogTask saves a task in RUNNING state with the given output
// already in its two log files, the way a worker mid-run leaves them.
func runningLogTask(t *testing.T, s *ServerConfig, stdout, stderr string) tasks.Task {
	t.Helper()
	tsk := resultFileTaskType(t, "")
	writeTaskLogs(t, tsk, stdout, stderr)
	tsk.State = "RUNNING"
	require.NoError(t, s.DB.SaveTask(&tsk))
	return tsk
}

// appendTaskLog adds a line to one of a running task's log files, standing
// in for the task writing one.
func appendTaskLog(t *testing.T, tsk tasks.Task, filename, line string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(tsk.ResultDir, filename), os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(line)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// sseFrames forwards an SSE response's non-empty `data:` payloads, one per
// event. The pre-rendered fragments this route emits end in a newline, so
// each event is two data lines -- the fragment and an empty one.
func sseFrames(body io.Reader) <-chan string {
	out := make(chan string, 4096)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimPrefix(line, "data:")
			if strings.TrimSpace(payload) == "" {
				continue
			}
			out <- payload
		}
	}()
	return out
}

// nextFrame takes one frame, or fails the test saying what it was waiting
// for -- a hang here is always "the stream never sent it".
func nextFrame(t *testing.T, frames <-chan string, what string) string {
	t.Helper()
	select {
	case f, ok := <-frames:
		require.True(t, ok, "stream closed while waiting for %s", what)
		return f
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return ""
	}
}

// openLogFrames connects to an SSE route and returns its frames plus a
// teardown that hangs up. The caller's defer order matters: the connection
// has to be dropped before httptest.Server.Close, which waits on in-flight
// requests.
func openLogFrames(t *testing.T, srv *httptest.Server, path string) (<-chan string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return sseFrames(resp.Body), func() {
		cancel()
		resp.Body.Close()
	}
}

// The fallback shape: a task with no combined record -- run by a worker
// older than turtlemonvh/blanket#104, or one with
// `workers.combinedLog = false` -- still gets its history, replayed from
// the two files as stdout's block then stderr's, before any live line,
// and with nothing replayed twice at the seam. Two independent files are
// all there is to work with here, so grouped is as good as it gets.
func TestUI_TaskLogStream_FallsBackToGroupedReplay(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	// Short idle window so the handler notices a gone client quickly and
	// the test doesn't sit through the 5s production one on teardown.
	s.TimeMultiplier = 0.1

	tsk := runningLogTask(t, s,
		"out backlog 1\nout backlog 2\n",
		"err backlog 1\nerr backlog 2\n")

	srv := httptest.NewServer(s.GetRouter())
	defer srv.Close()

	frames, hangup := openLogFrames(t, srv, "/ui/sse/tasks/"+tsk.Id.Hex()+"/log")
	defer hangup()

	// Four frames of backlog, in stream-grouped order, each badged with
	// the file it came from.
	assert.Contains(t, nextFrame(t, frames, "stdout backlog line 1"),
		`<span class="log-tag">stdout</span>out backlog 1`)
	assert.Contains(t, nextFrame(t, frames, "stdout backlog line 2"),
		`<span class="log-tag">stdout</span>out backlog 2`)
	assert.Contains(t, nextFrame(t, frames, "stderr backlog line 1"),
		`<span class="log-tag">stderr</span>err backlog 1`)
	assert.Contains(t, nextFrame(t, frames, "stderr backlog line 2"),
		`<span class="log-tag">stderr</span>err backlog 2`)

	// Everything written from here on arrives live, and nothing already
	// replayed comes round again.
	appendTaskLog(t, tsk, TaskStdoutLogFile, "out live\n")
	appendTaskLog(t, tsk, TaskStderrLogFile, "err live\n")

	seen := map[string]bool{}
	for len(seen) < 2 {
		f := nextFrame(t, frames, "a live line from each stream")
		assert.NotContains(t, f, "backlog",
			"a replayed line must not arrive a second time at the seam: %q", f)
		if strings.Contains(f, "out live") {
			assert.Contains(t, f, `<span class="log-tag">stdout</span>`)
			seen[LogStreamStdout] = true
		}
		if strings.Contains(f, "err live") {
			assert.Contains(t, f, `<span class="log-tag">stderr</span>`)
			seen[LogStreamStderr] = true
		}
	}
}

// The second review finding, and the point of the combined record:
// history comes back in the order the task produced it, not grouped by
// stream, so what you see after switching to `both` is what you would
// have seen watching it happen.
func TestUI_TaskLogStream_ReplaysCombinedRecordInTimeOrder(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	s.TimeMultiplier = 0.1

	// Deliberately interleaved: a grouped replay of the two per-stream
	// files would put both stdout lines first.
	tsk := runningCombinedLogTask(t, s,
		logEntry{LogStreamStdout, "step one"},
		logEntry{LogStreamStderr, "a warning"},
		logEntry{LogStreamStdout, "step two"},
	)

	srv := httptest.NewServer(s.GetRouter())
	defer srv.Close()

	frames, hangup := openLogFrames(t, srv, "/ui/sse/tasks/"+tsk.Id.Hex()+"/log")
	defer hangup()

	assert.Contains(t, nextFrame(t, frames, "the first line"),
		`<span class="log-tag">stdout</span>step one`)
	assert.Contains(t, nextFrame(t, frames, "the stderr line, in the middle"),
		`<span class="log-tag">stderr</span>a warning`)
	assert.Contains(t, nextFrame(t, frames, "the third line"),
		`<span class="log-tag">stdout</span>step two`)

	// And it keeps following the same file, so live lines arrive on the
	// end of that same ordering rather than from a second tail.
	appendTaskLog(t, tsk, TaskCombinedLogFile,
		combinedLogBody([]logEntry{{LogStreamStderr, "live warning"}}))
	f := nextFrame(t, frames, "a live line")
	assert.Contains(t, f, `<span class="log-tag">stderr</span>live warning`)
}

// A malformed row is skipped, not printed raw: a log pane is the wrong
// place to show someone a line of half-written JSON, and one bad row
// must not cost the task the rest of its output.
func TestUI_TaskLogStream_SkipsUnparseableCombinedRecords(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	s.TimeMultiplier = 0.1

	tsk := runningCombinedLogTask(t, s, logEntry{LogStreamStdout, "good line"})
	require.NoError(t, os.WriteFile(
		filepath.Join(tsk.ResultDir, TaskCombinedLogFile),
		[]byte("{\"ts\":1,\"stream\"\n"+combinedLogBody([]logEntry{{LogStreamStdout, "good line"}})),
		0644))

	srv := httptest.NewServer(s.GetRouter())
	defer srv.Close()

	frames, hangup := openLogFrames(t, srv, "/ui/sse/tasks/"+tsk.Id.Hex()+"/log")
	defer hangup()

	f := nextFrame(t, frames, "the surviving line")
	assert.Contains(t, f, `<span class="log-tag">stdout</span>good line`)
	assert.NotContains(t, f, "ts")
}

// The stored pane of a finished task reads the same record, so it shows
// the same order -- and drops the "stdout first, then stderr" note, which
// would now be a lie.
func TestUI_TaskLog_StoredCombinedIsInterleaved(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	tsk := runningCombinedLogTask(t, s,
		logEntry{LogStreamStdout, "step one"},
		logEntry{LogStreamStderr, "a warning"},
		logEntry{LogStreamStdout, "step two"},
	)
	tsk.State = "SUCCESS"
	require.NoError(t, s.DB.SaveTask(&tsk))

	body := getUI(s.GetRouter(),
		"/ui/partials/task-log?id="+tsk.Id.Hex()+"&stream=both").Body.String()

	assert.Less(t, strings.Index(body, "a warning"), strings.Index(body, "step two"),
		"the stderr line came second and must render second")
	assert.Less(t, strings.Index(body, "step one"), strings.Index(body, "a warning"))
	assert.NotContains(t, body, "stdout first, then stderr")
}

// ...and a task without the record keeps the note, because grouping is
// exactly what it is doing.
func TestUI_TaskLog_StoredFallbackSaysItIsGrouped(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	tsk := runningLogTask(t, s, "step one\nstep two\n", "a warning\n")
	tsk.State = "SUCCESS"
	require.NoError(t, s.DB.SaveTask(&tsk))

	body := getUI(s.GetRouter(),
		"/ui/partials/task-log?id="+tsk.Id.Hex()+"&stream=both").Body.String()

	assert.Contains(t, body, "stdout first, then stderr")
	assert.Less(t, strings.Index(body, "step two"), strings.Index(body, "a warning"),
		"without a combined record the two files can only be grouped")
}

// A task that has only written to one of its files still gets that file's
// backlog: the other one being empty must not swallow the replay.
func TestUI_TaskLogStream_ReplaysWhenOneStreamIsEmpty(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	s.TimeMultiplier = 0.1

	tsk := runningLogTask(t, s, "", "only stderr so far\n")

	srv := httptest.NewServer(s.GetRouter())
	defer srv.Close()

	frames, hangup := openLogFrames(t, srv, "/ui/sse/tasks/"+tsk.Id.Hex()+"/log")
	defer hangup()

	assert.Contains(t, nextFrame(t, frames, "the stderr backlog"),
		`<span class="log-tag">stderr</span>only stderr so far`)
}

// A line the task is still writing -- no newline yet -- belongs to the
// tailer, not the replay: it must arrive once, whole, and not as a
// truncated fragment followed by the rest.
func TestUI_TaskLogStream_PartialFinalLineIsNotReplayed(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	s.TimeMultiplier = 0.1

	tsk := runningLogTask(t, s, "out done\nout half", "")

	srv := httptest.NewServer(s.GetRouter())
	defer srv.Close()

	frames, hangup := openLogFrames(t, srv, "/ui/sse/tasks/"+tsk.Id.Hex()+"/log")
	defer hangup()

	assert.Contains(t, nextFrame(t, frames, "the complete backlog line"), "out done")

	// The writer finishes the line it was in the middle of.
	appendTaskLog(t, tsk, TaskStdoutLogFile, "-way\n")

	f := nextFrame(t, frames, "the completed line")
	assert.Contains(t, f, "out half-way",
		"the unterminated line should arrive once the writer finishes it")
}

// The combined view of a finished task shows the same content, in the
// same order, as the live one -- switching views, or coming back to a
// task after it ends, must not change what output you can see or how it
// is arranged.
func TestUI_TaskLog_FinishedMatchesLive(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	s.TimeMultiplier = 0.1

	tsk := runningCombinedLogTask(t, s,
		logEntry{LogStreamStdout, "out one"},
		logEntry{LogStreamStderr, "err one"},
		logEntry{LogStreamStdout, "out two"},
	)

	srv := httptest.NewServer(s.GetRouter())
	defer srv.Close()

	frames, hangup := openLogFrames(t, srv, "/ui/sse/tasks/"+tsk.Id.Hex()+"/log")
	live := []string{
		nextFrame(t, frames, "live frame 1"),
		nextFrame(t, frames, "live frame 2"),
		nextFrame(t, frames, "live frame 3"),
	}
	hangup()

	// Same task, now finished: the stored pane.
	tsk.State = "SUCCESS"
	require.NoError(t, s.DB.SaveTask(&tsk))
	stored := getUI(s.GetRouter(),
		"/ui/partials/task-log?id="+tsk.Id.Hex()+"&stream=both").Body.String()

	for _, frame := range live {
		assert.Contains(t, stored, strings.TrimSpace(frame),
			"the finished pane should render the same fragment the live stream sent")
	}
}

// The stderr view of a running task replays the file the same way the
// stdout view does. Both go through the raw log route, which is
// deliberately unchanged by #104 -- this is here so the two stay
// symmetrical if either ever moves.
func TestTaskLogStream_StderrReplaysLikeStdout(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	s.TimeMultiplier = 0.1

	tsk := runningLogTask(t, s, "out one\nout two\n", "err one\nerr two\n")

	srv := httptest.NewServer(s.GetRouter())
	defer srv.Close()

	id := tsk.Id.Hex()

	out, hangupOut := openLogFrames(t, srv, "/task/"+id+"/log")
	assert.Equal(t, "out one", nextFrame(t, out, "stdout backlog line 1"))
	assert.Equal(t, "out two", nextFrame(t, out, "stdout backlog line 2"))
	hangupOut()

	errs, hangupErr := openLogFrames(t, srv, "/task/"+id+"/log?stream=stderr")
	assert.Equal(t, "err one", nextFrame(t, errs, "stderr backlog line 1"))
	assert.Equal(t, "err two", nextFrame(t, errs, "stderr backlog line 2"))

	// And it keeps following after the replay.
	appendTaskLog(t, tsk, TaskStderrLogFile, "err three\n")
	assert.Equal(t, "err three", nextFrame(t, errs, "a live stderr line"))
	hangupErr()
}

// The backlog has to reach the client on connect, not whenever the next
// line happens along. c.Stream only flushes once its step returns, and
// that step then blocks for a whole idle window -- so without an explicit
// flush a quiet running task showed an empty pane for five seconds after
// every switch to `both`, which reads exactly like the lost history this
// route was fixed for.
//
// Deliberately runs at the production idle window (no TimeMultiplier): a
// scaled-down one hides the bug by returning from the step early.
func TestUI_TaskLogStream_FlushesReplayWithoutWaitingForLiveLines(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	tsk := runningLogTask(t, s, "out one\n", "err one\n")

	srv := httptest.NewServer(s.GetRouter())
	defer srv.Close()

	frames, hangup := openLogFrames(t, srv, "/ui/sse/tasks/"+tsk.Id.Hex()+"/log")
	defer hangup()

	var got []string
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case f, ok := <-frames:
			require.True(t, ok, "stream closed before the backlog arrived")
			got = append(got, f)
		case <-deadline:
			t.Fatalf("backlog did not reach the client within 2s of connecting, got %v", got)
		}
	}
	assert.Contains(t, got[0], `<span class="log-tag">stdout</span>out one`)
	assert.Contains(t, got[1], `<span class="log-tag">stderr</span>err one`)
}
