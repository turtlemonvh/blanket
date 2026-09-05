// Tests for synchronous ("blocking") task submission -- POST /task/?wait
// (turtlemonvh/blanket#27). See server/serve_sync.go.
//
// Covered here:
//   - wait satisfied by a task reaching a terminal state:
//     TestSyncSubmit_Completes, TestSyncSubmit_ExitCodeInPayload
//   - wait expiring on a task nobody claims: TestSyncSubmit_WaitExpires
//   - client disconnecting mid-wait: TestSyncSubmit_ClientDisconnect
//   - ?wait validation: TestSyncSubmit_WaitOverCap,
//     TestSyncSubmit_WaitUnparseable, TestSyncSubmit_NoWaitIsUnchanged
//   - fail_on_error: TestSyncSubmit_FailOnError
//   - result_file reading: TestReadTaskResult_*
//   - log tailing + truncation flags: TestTailLinesTruncated

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
)

// allTasks lists every task in the database, in id order. The handler
// generates the task id itself, so a test that needs to act on the task a
// blocking submit just created has to go looking for it.
func allTasks(t *testing.T, s *ServerConfig) []tasks.Task {
	t.Helper()
	found, _, err := s.DB.GetTasks(&database.TaskSearchConf{
		Limit:      100,
		SmallestId: objectid.NewObjectIdWithTime(time.Unix(0, 0)),
		LargestId:  objectid.NewObjectIdWithTime(time.Unix(database.FAR_FUTURE_SECONDS, 0)),
	})
	require.NoError(t, err)
	return found
}

// awaitTask polls until the blocking submit has saved its task, then
// returns it. Fails the test if nothing shows up quickly -- the handler
// saves the task before it starts waiting, so this is a near-immediate
// hand-off, not a real race.
func awaitTask(t *testing.T, s *ServerConfig) tasks.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if found := allTasks(t, s); len(found) > 0 {
			return found[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("blocking submit never saved a task")
	return tasks.Task{}
}

// syncSubmit issues POST /task/<query> with an echo_task body and returns
// the recorder. finisher, if non-nil, runs concurrently with the request
// and is handed the task once it exists -- that's how these tests stand in
// for the worker that would normally drive the task to a terminal state.
func syncSubmit(t *testing.T, s *ServerConfig, query string, finisher func(tsk tasks.Task)) *httptest.ResponseRecorder {
	t.Helper()
	r := s.GetRouter()

	done := make(chan struct{})
	if finisher != nil {
		go func() {
			defer close(done)
			finisher(awaitTask(t, s))
		}()
	} else {
		close(done)
	}

	req, _ := http.NewRequest("POST", "/task/"+query, strings.NewReader(`{"type": "echo_task"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	<-done
	return w
}

func intPtr(i int) *int { return &i }

// TestSyncSubmit_Completes is the happy path: the task reaches a terminal
// state inside the wait, and the caller gets state, exit code, and output
// back in the one response.
func TestSyncSubmit_Completes(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	w := syncSubmit(t, s, "?wait=10s", func(tsk tasks.Task) {
		require.NoError(t, os.MkdirAll(tsk.ResultDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(tsk.ResultDir, "blanket.stdout.log"), []byte("hello world\n"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(tsk.ResultDir, "blanket.stderr.log"), []byte("a warning\n"), 0644))
		require.NoError(t, s.DB.FinishTask(tsk.Id, &database.TaskFinishConfig{State: "SUCCESS", ExitCode: intPtr(0)}))
		s.TaskEvents.Notify()
	})

	assert.Equal(t, http.StatusOK, w.Code)

	var payload CompletionPayload
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.Equal(t, WaitOutcomeCompleted, payload.WaitOutcome)
	assert.Equal(t, "SUCCESS", payload.Task.State)
	require.NotNil(t, payload.Task.ExitCode)
	assert.Equal(t, 0, *payload.Task.ExitCode)
	assert.Equal(t, "hello world\n", payload.Stdout)
	assert.Equal(t, "a warning\n", payload.Stderr)
	assert.False(t, payload.StdoutTruncated)
	assert.False(t, payload.StderrTruncated)
	assert.Nil(t, payload.Result)
	assert.Nil(t, payload.ResultError)

	// Subscription released on the success path too.
	assert.Equal(t, 0, s.TaskEvents.SubscriberCount())
}

// TestSyncSubmit_ExitCodeInPayload drives the finish through the real
// worker-facing route (PUT /task/:id/finish?state=..&exitCode=..) rather
// than the DB directly, so the query-parameter plumbing is covered too.
func TestSyncSubmit_ExitCodeInPayload(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	r := s.GetRouter()
	w := syncSubmit(t, s, "?wait=10s", func(tsk tasks.Task) {
		req, _ := http.NewRequest("PUT", "/task/"+tsk.Id.Hex()+"/finish?state=ERROR&exitCode=3", nil)
		fw := httptest.NewRecorder()
		r.ServeHTTP(fw, req)
		assert.Equal(t, http.StatusOK, fw.Code)
	})

	// A failed task is still a successful API call by default.
	assert.Equal(t, http.StatusOK, w.Code)

	var payload CompletionPayload
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.Equal(t, "ERROR", payload.Task.State)
	require.NotNil(t, payload.Task.ExitCode)
	assert.Equal(t, 3, *payload.Task.ExitCode)
}

// TestFinishTask_InvalidExitCode: a present-but-unparseable exitCode is a
// 400 rather than a silently dropped field.
func TestFinishTask_InvalidExitCode(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	require.NoError(t, err)

	r := s.GetRouter()
	req, _ := http.NewRequest("PUT", "/task/"+tsk.Id.Hex()+"/finish?state=SUCCESS&exitCode=banana", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// And the task was left alone.
	after, err := s.DB.GetTask(tsk.Id)
	require.NoError(t, err)
	assert.Equal(t, "WAITING", after.State)
}

// TestSyncSubmit_WaitExpires: no worker will ever claim this task, so the
// wait runs out. The task keeps running and the caller gets the id plus a
// url to poll.
func TestSyncSubmit_WaitExpires(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	w := syncSubmit(t, s, "?wait=250ms", nil)
	assert.Equal(t, http.StatusGatewayTimeout, w.Code)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, WaitOutcomeTimeout, body["waitOutcome"])
	assert.Equal(t, "WAITING", body["state"])

	id, _ := body["id"].(string)
	assert.True(t, objectid.IsObjectIdHex(id), "504 body should carry the task id, got %q", id)
	assert.Equal(t, "/task/"+id, body["pollUrl"])

	// The task is untouched and still queued.
	found := allTasks(t, s)
	require.Len(t, found, 1)
	assert.Equal(t, "WAITING", found[0].State)
	assert.Equal(t, 0, s.TaskEvents.SubscriberCount())
}

// TestSyncSubmit_ClientDisconnect: the handler returns when the client
// hangs up, releases its subscription, and leaves the task running.
func TestSyncSubmit_ClientDisconnect(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	r := s.GetRouter()
	ctx, cancelRequest := context.WithCancel(context.Background())
	req, _ := http.NewRequest("POST", "/task/?wait=60s", strings.NewReader(`{"type": "echo_task"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	handlerReturned := make(chan struct{})
	go func() {
		defer close(handlerReturned)
		r.ServeHTTP(httptest.NewRecorder(), req)
	}()

	// Wait until the handler is actually waiting before hanging up.
	awaitTask(t, s)
	cancelRequest()

	select {
	case <-handlerReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after the client disconnected")
	}

	assert.Equal(t, 0, s.TaskEvents.SubscriberCount(), "wait subscription should be released on disconnect")

	found := allTasks(t, s)
	require.Len(t, found, 1)
	assert.Equal(t, "WAITING", found[0].State, "a disconnected client should not affect the task")
}

// TestSyncSubmit_WaitOverCap: over tasks.sync.maxWait is a 400, not a
// silent clamp -- and nothing is queued.
func TestSyncSubmit_WaitOverCap(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	viper.Set("tasks.sync.maxWait", "300s")
	defer viper.Set("tasks.sync.maxWait", nil)

	w := syncSubmit(t, s, "?wait=1h", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "maxWait")
	assert.Len(t, allTasks(t, s), 0, "a rejected wait must not leave a task behind")
}

func TestSyncSubmit_WaitUnparseable(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	for _, bad := range []string{"?wait=soon", "?wait=-5s", "?wait=0"} {
		w := syncSubmit(t, s, bad, nil)
		assert.Equal(t, http.StatusBadRequest, w.Code, "expected 400 for %s", bad)
	}
	assert.Len(t, allTasks(t, s), 0)
}

// TestSyncSubmit_NoWaitIsUnchanged: without ?wait the endpoint behaves
// exactly as before -- 201 with the task record, no blocking.
func TestSyncSubmit_NoWaitIsUnchanged(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	w := syncSubmit(t, s, "", nil)
	assert.Equal(t, http.StatusCreated, w.Code)

	var tsk tasks.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &tsk))
	assert.Equal(t, "WAITING", tsk.State)
	assert.Nil(t, tsk.ExitCode, "a task that hasn't run has no exit code")
	assert.Equal(t, 0, s.TaskEvents.SubscriberCount(), "no ?wait means no subscription")
}

// TestSyncSubmit_FailOnError: opt-in mapping of a non-SUCCESS terminal
// state onto a 502, with the completion payload still in the body.
func TestSyncSubmit_FailOnError(t *testing.T) {
	cases := []struct {
		query      string
		finalState string
		wantStatus int
	}{
		{"?wait=10s&fail_on_error=true", "ERROR", http.StatusBadGateway},
		{"?wait=10s&fail_on_error=true", "STOPPED", http.StatusBadGateway},
		{"?wait=10s&fail_on_error=true", "SUCCESS", http.StatusOK},
		{"?wait=10s&fail_on_error", "ERROR", http.StatusBadGateway},
		{"?wait=10s&fail_on_error=false", "ERROR", http.StatusOK},
		{"?wait=10s", "ERROR", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.query+" "+tc.finalState, func(t *testing.T) {
			s, cleanup := NewTestServer()
			defer cleanup()
			cleanupType := setupTestTaskType(t)
			defer cleanupType()

			w := syncSubmit(t, s, tc.query, func(tsk tasks.Task) {
				require.NoError(t, s.DB.FinishTask(tsk.Id, &database.TaskFinishConfig{
					State:    tc.finalState,
					ExitCode: intPtr(1),
				}))
				s.TaskEvents.Notify()
			})

			assert.Equal(t, tc.wantStatus, w.Code)

			var payload CompletionPayload
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
			assert.Equal(t, tc.finalState, payload.Task.State)
			assert.Equal(t, WaitOutcomeCompleted, payload.WaitOutcome)
		})
	}
}

func TestSyncSubmit_FailOnErrorUnparseable(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	w := syncSubmit(t, s, "?wait=1s&fail_on_error=maybe", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Len(t, allTasks(t, s), 0)
}

// ---------------------------------------------------------------------------
// result_file
// ---------------------------------------------------------------------------

// resultFileTaskType writes a task type declaring result_file = resultFile
// and returns a task pointing at a fresh result dir.
func resultFileTaskType(t *testing.T, resultFile string) tasks.Task {
	t.Helper()

	typesDir := t.TempDir()
	resultsDir := t.TempDir()
	toml := "timeout = 10\ncommand = \"true\"\nexecutor = \"bash\"\n"
	if resultFile != "" {
		toml += "result_file = \"" + resultFile + "\"\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(typesDir, "result_task.toml"), []byte(toml), 0644))

	viper.Set("tasks.typesPaths", []string{typesDir})
	viper.Set("tasks.resultsPath", resultsDir)
	t.Cleanup(func() {
		viper.Set("tasks.typesPaths", nil)
		viper.Set("tasks.resultsPath", "")
	})

	tt, err := tasks.FetchTaskType("result_task")
	require.NoError(t, err)
	tsk, err := tt.NewTask(nil)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(tsk.ResultDir, 0755))
	return tsk
}

func TestReadTaskResult_Parsed(t *testing.T) {
	tsk := resultFileTaskType(t, "result.json")
	require.NoError(t, os.WriteFile(filepath.Join(tsk.ResultDir, "result.json"), []byte(`{"answer": 42}`), 0644))

	result, resultErr := readTaskResult(tsk, DefaultSyncMaxResultBytes)
	assert.Equal(t, "", resultErr)
	m, ok := result.(map[string]interface{})
	require.True(t, ok, "expected an object, got %#v", result)
	assert.Equal(t, float64(42), m["answer"])
}

func TestReadTaskResult_NoneDeclared(t *testing.T) {
	tsk := resultFileTaskType(t, "")
	result, resultErr := readTaskResult(tsk, DefaultSyncMaxResultBytes)
	assert.Nil(t, result)
	assert.Equal(t, "", resultErr)
}

// A missing file is a normal outcome (the task failed before writing it),
// not an error.
func TestReadTaskResult_Missing(t *testing.T) {
	tsk := resultFileTaskType(t, "result.json")
	result, resultErr := readTaskResult(tsk, DefaultSyncMaxResultBytes)
	assert.Nil(t, result)
	assert.Equal(t, "", resultErr)
}

func TestReadTaskResult_Unparseable(t *testing.T) {
	tsk := resultFileTaskType(t, "result.json")
	require.NoError(t, os.WriteFile(filepath.Join(tsk.ResultDir, "result.json"), []byte("not json at all"), 0644))

	result, resultErr := readTaskResult(tsk, DefaultSyncMaxResultBytes)
	assert.Nil(t, result)
	assert.Contains(t, resultErr, "could not parse")
}

func TestReadTaskResult_Oversized(t *testing.T) {
	tsk := resultFileTaskType(t, "result.json")
	require.NoError(t, os.WriteFile(filepath.Join(tsk.ResultDir, "result.json"), []byte(`{"a": "bbbbbbbbbb"}`), 0644))

	result, resultErr := readTaskResult(tsk, 4)
	assert.Nil(t, result)
	assert.Contains(t, resultErr, "maxResultBytes")
}

// A result_file that escapes the result dir never yields the file it
// points at. The loader rejects such a value long before a task runs (see
// TestResultFileTypeDoesNotLoad below), so this drives the reader's own
// containment check directly -- the point of having it at both layers.
func TestReadResultFileAt_EscapingPathIsRefused(t *testing.T) {
	parent := t.TempDir()
	resultDir := filepath.Join(parent, "68b4c1f2a3e4d5b6c7a8f9e0")
	require.NoError(t, os.MkdirAll(resultDir, 0755))

	// A secret one directory up from the result dir, which a `..` path
	// would otherwise reach.
	secret := filepath.Join(parent, "secret.json")
	require.NoError(t, os.WriteFile(secret, []byte(`{"stolen": true}`), 0644))

	for _, bad := range []string{"../secret.json", "/etc/passwd", `..\secret.json`, "sub/../../secret.json", secret} {
		result, resultErr := readResultFileAt(resultDir, bad, DefaultSyncMaxResultBytes)
		assert.Nil(t, result, "escaping result_file %q must never yield content", bad)
		assert.NotEqual(t, "", resultErr, "escaping result_file %q should report why", bad)
		assert.NotContains(t, resultErr, "stolen")
	}

	// The contained case still works, so the check isn't just refusing
	// everything.
	require.NoError(t, os.WriteFile(filepath.Join(resultDir, "result.json"), []byte(`{"ok": true}`), 0644))
	result, resultErr := readResultFileAt(resultDir, "result.json", DefaultSyncMaxResultBytes)
	assert.Equal(t, "", resultErr)
	assert.NotNil(t, result)
}

func TestResultFileTypeDoesNotLoad(t *testing.T) {
	typesDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(typesDir, "bad_result.toml"),
		[]byte("command = \"true\"\nresult_file = \"../escape.json\"\n"),
		0644,
	))
	viper.Set("tasks.typesPaths", []string{typesDir})
	defer viper.Set("tasks.typesPaths", nil)

	_, err := tasks.FetchTaskType("bad_result")
	assert.Error(t, err, "a type with an escaping result_file must not be loadable")
}

// ---------------------------------------------------------------------------
// log tailing
// ---------------------------------------------------------------------------

func TestTailLinesTruncated(t *testing.T) {
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty.log")
	require.NoError(t, os.WriteFile(empty, nil, 0644))
	content, truncated, err := tailLinesTruncated(empty, 10)
	assert.NoError(t, err)
	assert.Equal(t, "", content)
	assert.False(t, truncated)

	short := filepath.Join(dir, "short.log")
	require.NoError(t, os.WriteFile(short, []byte("a\nb\n"), 0644))
	content, truncated, err = tailLinesTruncated(short, 10)
	assert.NoError(t, err)
	assert.Equal(t, "a\nb\n", content)
	assert.False(t, truncated)

	long := filepath.Join(dir, "long.log")
	require.NoError(t, os.WriteFile(long, []byte("a\nb\nc\nd\n"), 0644))
	content, truncated, err = tailLinesTruncated(long, 2)
	assert.NoError(t, err)
	assert.Equal(t, "c\nd\n", content)
	assert.True(t, truncated)

	_, _, err = tailLinesTruncated(filepath.Join(dir, "does-not-exist.log"), 10)
	assert.Error(t, err)
}
