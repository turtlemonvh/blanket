// Tests for the structured task event stream -- POST /task/?wait&stream
// and the NDJSON variant of GET /task/:id/log (turtlemonvh/blanket#27,
// PR 2). See server/serve_stream.go.
//
// Covered here:
//   - the encoder, both framings, against golden strings:
//     TestStreamEncoder_NDJSON, TestStreamEncoder_SSE,
//     TestStreamEncoder_ResultEventCarriesCompletionPayload
//   - a streamed submit emitting state -> log -> result:
//     TestSyncSubmit_StreamCompletes
//   - the wait expiring mid-stream: TestSyncSubmit_StreamWaitTimeout
//   - client disconnect releasing everything:
//     TestSyncSubmit_StreamClientDisconnect
//   - SSE framing via Accept: TestSyncSubmit_StreamSSEFraming
//   - GET /task/:id/log negotiation: TestStreamTaskLog_DefaultIsRawSSE,
//     TestStreamTaskLog_NDJSONIsStructured
//   - the isComplete regression: TestStreamTaskLog_StaysOpenUntilTerminal

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/tasks"
)

/*
 * The encoder
 */

// fixedTs pins an event's timestamp so the golden strings below are
// stable; everything else about the event is what the constructors
// produce.
func fixedTs(ev *EventEnvelope) {
	ev.Ts = 1756900000
}

func TestStreamEncoder_NDJSON(t *testing.T) {
	var buf bytes.Buffer
	enc := NewStreamEncoder(&buf, FramingNDJSON)

	first := NewStateEvent("68b4c1f2a3e4d5b6c7a8f9e0", "WAITING", "")
	fixedTs(&first.EventEnvelope)
	require.NoError(t, enc.Encode(first))

	next := NewStateEvent("68b4c1f2a3e4d5b6c7a8f9e0", "RUNNING", "CLAIMED")
	fixedTs(&next.EventEnvelope)
	require.NoError(t, enc.Encode(next))

	line := NewLogEvent("68b4c1f2a3e4d5b6c7a8f9e0", LogStreamStdout, 1, "hello world")
	fixedTs(&line.EventEnvelope)
	require.NoError(t, enc.Encode(line))

	assert.Equal(t,
		`{"ts":1756900000,"taskId":"68b4c1f2a3e4d5b6c7a8f9e0","type":"state","state":"WAITING","previousState":null}`+"\n"+
			`{"ts":1756900000,"taskId":"68b4c1f2a3e4d5b6c7a8f9e0","type":"state","state":"RUNNING","previousState":"CLAIMED"}`+"\n"+
			`{"ts":1756900000,"taskId":"68b4c1f2a3e4d5b6c7a8f9e0","type":"log","stream":"stdout","seq":1,"line":"hello world"}`+"\n",
		buf.String())

	assert.Equal(t, ContentTypeNDJSON, enc.ContentType())
}

func TestStreamEncoder_SSE(t *testing.T) {
	var buf bytes.Buffer
	enc := NewStreamEncoder(&buf, FramingSSE)

	ev := NewStateEvent("68b4c1f2a3e4d5b6c7a8f9e0", "RUNNING", "CLAIMED")
	fixedTs(&ev.EventEnvelope)
	require.NoError(t, enc.Encode(ev))

	line := NewLogEvent("68b4c1f2a3e4d5b6c7a8f9e0", LogStreamStderr, 2, "a warning")
	fixedTs(&line.EventEnvelope)
	require.NoError(t, enc.Encode(line))

	// The `event:` name is the event's own type, and the `data:` payload
	// is byte-identical to what the NDJSON framing writes -- that
	// sameness is the reason there is one encoder rather than two.
	assert.Equal(t,
		"event:state\ndata:"+`{"ts":1756900000,"taskId":"68b4c1f2a3e4d5b6c7a8f9e0","type":"state","state":"RUNNING","previousState":"CLAIMED"}`+"\n\n"+
			"event:log\ndata:"+`{"ts":1756900000,"taskId":"68b4c1f2a3e4d5b6c7a8f9e0","type":"log","stream":"stderr","seq":2,"line":"a warning"}`+"\n\n",
		buf.String())

	assert.Equal(t, ContentTypeSSE, enc.ContentType())
}

// The terminal event's JSON must be the blocking ?wait body plus the
// three envelope fields -- that is what lets a caller use one parser for
// both modes.
func TestStreamEncoder_ResultEventCarriesCompletionPayload(t *testing.T) {
	payload := CompletionPayload{
		WaitOutcome:     WaitOutcomeCompleted,
		Stdout:          "hello world\n",
		Stderr:          "",
		StdoutTruncated: false,
		Result:          map[string]interface{}{"answer": float64(42)},
	}

	var buf bytes.Buffer
	require.NoError(t, NewStreamEncoder(&buf, FramingNDJSON).Encode(NewResultEvent("68b4", payload)))

	var got map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, EventTypeResult, got["type"])
	assert.Equal(t, "68b4", got["taskId"])
	assert.Equal(t, WaitOutcomeCompleted, got["waitOutcome"])
	assert.Equal(t, "hello world\n", got["stdout"])
	assert.Contains(t, got, "task")
	assert.Contains(t, got, "stdoutTruncated")
	assert.Contains(t, got, "resultError")

	// And the same fields the blocking payload marshals, with the same
	// names -- compared key set to key set so a field added to one
	// without the other is caught.
	blocking, err := json.Marshal(payload)
	require.NoError(t, err)
	var blockingKeys map[string]interface{}
	require.NoError(t, json.Unmarshal(blocking, &blockingKeys))
	for k := range blockingKeys {
		assert.Contains(t, got, k, "result event is missing completion payload field %q", k)
	}
}

/*
 * Helpers for the streaming handler tests
 */

// ndjsonEvents splits an NDJSON body into decoded events.
func ndjsonEvents(t *testing.T, body string) []map[string]interface{} {
	t.Helper()
	var events []map[string]interface{}
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &ev), "undecodable event line: %s", line)
		events = append(events, ev)
	}
	require.NoError(t, scanner.Err())
	return events
}

func eventsOfType(events []map[string]interface{}, typ string) []map[string]interface{} {
	var found []map[string]interface{}
	for _, ev := range events {
		if ev["type"] == typ {
			found = append(found, ev)
		}
	}
	return found
}

// writeTaskLogs creates the task's result dir and both log files, the way
// the worker's SetupExecutionDirectory would.
func writeTaskLogs(t *testing.T, tsk tasks.Task, stdout, stderr string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(tsk.ResultDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tsk.ResultDir, "blanket.stdout.log"), []byte(stdout), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tsk.ResultDir, "blanket.stderr.log"), []byte(stderr), 0644))
}

/*
 * POST /task/?wait&stream
 */

// TestSyncSubmit_StreamCompletes is the happy path: the caller sees the
// task's state transitions and its output as they happen, and the stream
// closes with the same payload the blocking mode would have returned.
func TestSyncSubmit_StreamCompletes(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	r := s.GetRouter()
	w := syncSubmit(t, s, "?wait=20s&stream", func(tsk tasks.Task) {
		// The worker writes both log files before it flips the task to
		// RUNNING, so the stream can only attach after that transition.
		writeTaskLogs(t, tsk, "hello world\n", "a warning\n")
		putTaskInRunningState(t, s, r, tsk)
		s.TaskEvents.Notify()

		// Leave the task running long enough for the stream to attach to
		// the log files and deliver a line or two live.
		time.Sleep(750 * time.Millisecond)

		require.NoError(t, s.DB.FinishTask(tsk.Id, &database.TaskFinishConfig{NewState: "SUCCESS", ExitCode: intPtr(0)}))
		s.TaskEvents.Notify()
	})

	// A stream is always a 200: the status is written long before the
	// task's outcome is known.
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, ContentTypeNDJSON, w.Header().Get("Content-Type"))

	events := ndjsonEvents(t, w.Body.String())
	require.NotEmpty(t, events)

	// First event describes the state the task was already in, with a
	// null previousState.
	assert.Equal(t, EventTypeState, events[0]["type"])
	assert.Nil(t, events[0]["previousState"])

	states := eventsOfType(events, EventTypeState)
	var seen []string
	for _, ev := range states {
		seen = append(seen, ev["state"].(string))
	}
	assert.Contains(t, seen, "RUNNING", "stream should report the transition into RUNNING; saw %v", seen)
	assert.Contains(t, seen, "SUCCESS", "stream should report the terminal transition; saw %v", seen)

	logs := eventsOfType(events, EventTypeLog)
	require.NotEmpty(t, logs, "stream should carry the task's output as log events; got %v", events)
	var stdoutLines []string
	for _, ev := range logs {
		assert.Contains(t, []interface{}{LogStreamStdout, LogStreamStderr}, ev["stream"])
		assert.NotNil(t, ev["seq"])
		if ev["stream"] == LogStreamStdout {
			stdoutLines = append(stdoutLines, ev["line"].(string))
		}
	}
	assert.Contains(t, stdoutLines, "hello world")

	// The terminal event is last, and repeats the output tail so it is
	// the blocking payload exactly.
	last := events[len(events)-1]
	assert.Equal(t, EventTypeResult, last["type"])
	assert.Equal(t, WaitOutcomeCompleted, last["waitOutcome"])
	assert.Equal(t, "hello world\n", last["stdout"])
	assert.Equal(t, "a warning\n", last["stderr"])
	task := last["task"].(map[string]interface{})
	assert.Equal(t, "SUCCESS", task["state"])
	assert.Equal(t, float64(0), task["exitCode"])

	assert.Equal(t, 0, s.TaskEvents.SubscriberCount(), "stream should release its subscription")
}

// TestSyncSubmit_StreamWaitTimeout: nobody claims the task, so the wait
// runs out. A stream can't change its status at that point, so the
// distinction the 504 carries in blocking mode is carried by the result
// event's waitOutcome instead.
func TestSyncSubmit_StreamWaitTimeout(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	w := syncSubmit(t, s, "?wait=400ms&stream", nil)
	assert.Equal(t, http.StatusOK, w.Code)

	events := ndjsonEvents(t, w.Body.String())
	require.NotEmpty(t, events)

	assert.Equal(t, EventTypeState, events[0]["type"])
	assert.Equal(t, "WAITING", events[0]["state"])

	last := events[len(events)-1]
	assert.Equal(t, EventTypeResult, last["type"])
	assert.Equal(t, WaitOutcomeTimeout, last["waitOutcome"])
	task := last["task"].(map[string]interface{})
	assert.Equal(t, "WAITING", task["state"])

	// The task is untouched and still queued.
	found := allTasks(t, s)
	require.Len(t, found, 1)
	assert.Equal(t, "WAITING", found[0].State)
	assert.Equal(t, 0, s.TaskEvents.SubscriberCount())
}

// TestSyncSubmit_StreamClientDisconnect: the handler returns when the
// client hangs up, releases its subscription, and leaves the task alone.
func TestSyncSubmit_StreamClientDisconnect(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	r := s.GetRouter()
	ctx, cancelRequest := context.WithCancel(context.Background())
	req, _ := http.NewRequest("POST", "/task/?wait=60s&stream", strings.NewReader(`{"type": "echo_task"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	handlerReturned := make(chan struct{})
	go func() {
		defer close(handlerReturned)
		r.ServeHTTP(httptest.NewRecorder(), req)
	}()

	awaitTask(t, s)
	cancelRequest()

	select {
	case <-handlerReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("streaming handler did not return after the client disconnected")
	}

	assert.Equal(t, 0, s.TaskEvents.SubscriberCount(), "stream subscription should be released on disconnect")

	found := allTasks(t, s)
	require.Len(t, found, 1)
	assert.Equal(t, "WAITING", found[0].State, "a disconnected client should not affect the task")
}

// TestSyncSubmit_StreamSSEFraming: same events, SSE frames, chosen by
// Accept alone -- the payload is negotiated, the framing is not.
func TestSyncSubmit_StreamSSEFraming(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	r := s.GetRouter()
	req, _ := http.NewRequest("POST", "/task/?wait=400ms&stream", strings.NewReader(`{"type": "echo_task"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", ContentTypeSSE)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, ContentTypeSSE, w.Header().Get("Content-Type"))

	body := w.Body.String()
	assert.Contains(t, body, "event:state\ndata:{")
	assert.Contains(t, body, "event:result\ndata:{")
	assert.Contains(t, body, `"waitOutcome":"wait_timeout"`)
}

// A ?stream with no ?wait is still a wait -- a stream with no budget
// would be a submit that never returns.
func TestSyncSubmit_StreamImpliesWait(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	viper.Set("tasks.sync.defaultWait", "300ms")
	defer viper.Set("tasks.sync.defaultWait", nil)

	w := syncSubmit(t, s, "?stream", nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, ContentTypeNDJSON, w.Header().Get("Content-Type"))

	events := ndjsonEvents(t, w.Body.String())
	require.NotEmpty(t, events)
	assert.Equal(t, EventTypeResult, events[len(events)-1]["type"])
}

/*
 * GET /task/:id/log
 */

// logStreamFixture stands up a real listener (the raw log stream uses
// gin's c.Stream, which calls CloseNotify -- an httptest recorder has no
// such method) with one task in RUNNING and a seeded stdout log.
func logStreamFixture(t *testing.T) (*ServerConfig, *httptest.Server, tasks.Task, func()) {
	t.Helper()

	cleanupType := setupTestTaskType(t)
	s, cleanupServer := NewTestServer()
	// The raw log stream's idle window is LOGLINE_WAIT_DURATION scaled by
	// this; leaving it at zero makes the handler spin.
	s.TimeMultiplier = 0.1

	r := s.GetRouter()
	created := postTask(r, "echo_task")
	var tsk tasks.Task
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &tsk))

	writeTaskLogs(t, tsk, "seed line\n", "")
	putTaskInRunningState(t, s, r, tsk)

	srv := httptest.NewServer(r)
	return s, srv, tsk, func() {
		srv.Close()
		cleanupServer()
		cleanupType()
	}
}

// openStream issues a GET and reports whatever the body produces, plus
// the error that ended it.
type openStream struct {
	resp  *http.Response
	chunk chan string
	done  chan error
}

func openLogStream(t *testing.T, ctx context.Context, url string, accept string) *openStream {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	require.NoError(t, err)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	st := &openStream{resp: resp, chunk: make(chan string, 64), done: make(chan error, 1)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				st.chunk <- string(buf[:n])
			}
			if rerr != nil {
				st.done <- rerr
				return
			}
		}
	}()
	return st
}

func (st *openStream) nextChunk(t *testing.T, within time.Duration) string {
	t.Helper()
	select {
	case c := <-st.chunk:
		return c
	case err := <-st.done:
		t.Fatalf("stream ended before producing a chunk: %v", err)
	case <-time.After(within):
		t.Fatalf("no data on the stream within %s", within)
	}
	return ""
}

// The historical shape of GET /task/:id/log -- raw stdout lines as SSE
// `event: message` frames -- is what the htmx UI consumes, so it must
// stay exactly as it was.
func TestStreamTaskLog_DefaultIsRawSSE(t *testing.T) {
	s, srv, tsk, cleanup := logStreamFixture(t)
	defer cleanup()
	_ = s

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := openLogStream(t, ctx, srv.URL+"/task/"+tsk.Id.Hex()+"/log", "")
	defer st.resp.Body.Close()

	// (No Content-Type assertion: the raw stream sets that header from
	// inside its loop, so it only lands if a log line beats the first
	// idle tick. Pre-existing, and deliberately not touched here -- this
	// test is about the body shape staying raw.)
	got := st.nextChunk(t, 5*time.Second)
	assert.Contains(t, got, "event:message")
	assert.Contains(t, got, "seed line")
	assert.NotContains(t, got, `"type":"log"`, "the default log stream must stay raw, not structured")
}

// ...and asking for NDJSON gets the structured event stream from the
// same encoder, so the two surfaces cannot drift.
func TestStreamTaskLog_NDJSONIsStructured(t *testing.T) {
	s, srv, tsk, cleanup := logStreamFixture(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := openLogStream(t, ctx, srv.URL+"/task/"+tsk.Id.Hex()+"/log", ContentTypeNDJSON)
	defer st.resp.Body.Close()

	assert.Equal(t, ContentTypeNDJSON, st.resp.Header.Get("Content-Type"))

	// Collect until the stream closes, which it does once the task goes
	// terminal.
	collected := ""
	deadline := time.After(20 * time.Second)
	finished := false
	for {
		if !finished && strings.Contains(collected, `"type":"log"`) {
			require.NoError(t, s.DB.FinishTask(tsk.Id, &database.TaskFinishConfig{NewState: "SUCCESS", ExitCode: intPtr(0)}))
			s.TaskEvents.Notify()
			finished = true
		}
		select {
		case c := <-st.chunk:
			collected += c
		case <-st.done:
			require.True(t, finished, "stream closed before the task finished: %s", collected)
			events := ndjsonEvents(t, collected)
			require.NotEmpty(t, events)
			assert.Equal(t, EventTypeState, events[0]["type"])
			assert.Equal(t, "RUNNING", events[0]["state"])
			assert.NotEmpty(t, eventsOfType(events, EventTypeLog))
			last := events[len(events)-1]
			assert.Equal(t, EventTypeResult, last["type"])
			assert.Equal(t, WaitOutcomeCompleted, last["waitOutcome"])
			return
		case <-deadline:
			t.Fatalf("structured log stream never closed; collected: %s", collected)
		}
	}
}

// TestStreamTaskLog_StaysOpenUntilTerminal is the regression test for the
// isComplete bug: the closure returned true on every path, so a log
// stream hung up after its first idle window regardless of what the task
// was doing. With TimeMultiplier 0.1 that window is 500ms, so a stream
// that survives several seconds of silence proves the fix, and it must
// still close once the task reaches a terminal state.
func TestStreamTaskLog_StaysOpenUntilTerminal(t *testing.T) {
	s, srv, tsk, cleanup := logStreamFixture(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := openLogStream(t, ctx, srv.URL+"/task/"+tsk.Id.Hex()+"/log", "")
	defer st.resp.Body.Close()

	st.nextChunk(t, 5*time.Second) // the seeded line

	idleWindow := time.Duration(float64(LOGLINE_WAIT_DURATION) * s.TimeMultiplier * float64(time.Second))
	select {
	case err := <-st.done:
		t.Fatalf("log stream on a RUNNING task closed after %s (idle window %s): %v", "one idle window", idleWindow, err)
	case <-time.After(6 * idleWindow):
	}

	require.NoError(t, s.DB.FinishTask(tsk.Id, &database.TaskFinishConfig{NewState: "SUCCESS", ExitCode: intPtr(0)}))
	s.TaskEvents.Notify()

	select {
	case <-st.done:
	case <-time.After(10 * time.Second):
		t.Fatal("log stream stayed open after the task reached a terminal state")
	}
}

// negotiation is pure request inspection; worth a table rather than a
// live stream per case.
func TestStructuredLogStreamRequested(t *testing.T) {
	cases := []struct {
		query  string
		accept string
		want   bool
	}{
		{"", "", false},
		{"", "text/event-stream", false},
		{"", "application/x-ndjson", true},
		{"", "application/x-ndjson, text/plain", true},
		{"format=ndjson", "", true},
		{"format=NDJSON", "", true},
		{"format=events", "", true},
		{"format=raw", "", false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("q=%q/accept=%q", tc.query, tc.accept), func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/task/x/log?"+tc.query, nil)
			if tc.accept != "" {
				c.Request.Header.Set("Accept", tc.accept)
			}
			assert.Equal(t, tc.want, structuredLogStreamRequested(c))
			if tc.accept == ContentTypeSSE {
				assert.Equal(t, FramingSSE, streamFramingFor(c))
			}
		})
	}
}
