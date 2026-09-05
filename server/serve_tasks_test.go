// TODO: additional HTTP handler tests to write
//
// Covered:
//   - POST /task/ with JSON body: TestPostTask_Valid, TestPostTask_MissingTypeField,
//     TestPostTask_UnknownType
//   - GET /task/:id:            TestGetTask_InvalidId, TestGetTask_Exists
//   - GET /task/ + state filter: TestTaskList_FilterByState
//   - DELETE /task/:id:         TestDeleteTask
//   - PUT /task/:id/cancel from WAITING: TestCancelTask_Waiting
//   - PUT /task/:id/cancel from RUNNING without force (rejected, no-op):
//     TestCancelTask_RunningWithoutForce
//   - PUT /task/:id/cancel?force=true from RUNNING: TestCancelTask_RunningWithForce
//   - cancelTaskById RUNNING force gate: TestCancelTaskById_RunningRequiresForce
//   - worker observing the STOPPED tombstone mid-run and killing the
//     subprocess: TestProcessOne_StoppedMidFlight (worker/worker_test.go)
//   - PUT /task/:id/progress (valid + out-of-range): TestUpdateProgress_Valid,
//     TestUpdateProgress_InvalidValue
//   - PUT /task/:id/progress (missing task): TestUpdateProgress_MissingTask
//   - PUT /task/:id/progress (wrong state): TestUpdateProgress_WrongState
//   - PUT /task/:id/finish: TestFinishTask_Valid, TestFinishTask_MissingTask,
//     TestFinishTask_AlreadyTerminalIsNoop, TestFinishTask_InvalidState,
//     TestFinishTask_FromClaimedIsBadRequest,
//     TestFinishTask_InvalidExitCode (serve_sync_test.go)
//   - PUT /task/:id/run + /finish idempotency and the RunId fencing token
//     (turtlemonvh/blanket#23 phase 1): TestRunTask_*, TestFinishTask_*RunId*,
//     TestFinishTask_LateErrorAfterUserStopKeepsStopped,
//     TestFinishTask_RepeatKeepsStoredExitCode
//   - POST /task/?wait (synchronous submission): server/serve_sync_test.go
//   - POST /task/claim/:workerid edges: TestClaim_MissingWorker,
//     TestClaim_NoMatchingTask, TestClaim_DeletedTaskDoesNotPanic
//   - claim-task happy path: covered by worker integration test TestProcessOne
//
// Not yet covered:
//   - POST /task/ with multipart form + file uploads (data=@file, extra files
//     placed at the task's working dir root)
//   - GET /task/ with the full filter flag set (not just state): type, tags,
//     created-before/after, limit, offset, sort order, etc.

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
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
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

// minimalTaskTypeToml is a task type with no required environment variables,
// suitable for tests that just need any valid task type.
const minimalTaskTypeToml = `
tags = ["bash", "unix"]
timeout = 10
command = "echo 'hello from blanket'"
executor = "bash"
`

// setupTestTaskType writes a minimal task type to a temp directory, points
// viper at it, and returns a cleanup function to restore state.
func setupTestTaskType(t *testing.T) func() {
	t.Helper()

	typesDir, err := os.MkdirTemp("", "blanket-test-types-*")
	if err != nil {
		t.Fatalf("failed to create types dir: %v", err)
	}

	resultsDir, err := os.MkdirTemp("", "blanket-test-results-*")
	if err != nil {
		os.RemoveAll(typesDir)
		t.Fatalf("failed to create results dir: %v", err)
	}

	err = os.WriteFile(
		filepath.Join(typesDir, "echo_task.toml"),
		[]byte(minimalTaskTypeToml),
		0644,
	)
	if err != nil {
		os.RemoveAll(typesDir)
		os.RemoveAll(resultsDir)
		t.Fatalf("failed to write task type TOML: %v", err)
	}

	viper.Set("tasks.typesPaths", []string{typesDir})
	viper.Set("tasks.resultsPath", resultsDir)

	return func() {
		os.RemoveAll(typesDir)
		os.RemoveAll(resultsDir)
		viper.Set("tasks.typesPaths", nil)
		viper.Set("tasks.resultsPath", "")
	}
}

// postJSON posts a JSON string to path and returns the recorder.
func postJSON(r http.Handler, path string, body string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// postTask is a convenience wrapper around postJSON for /task/.
func postTask(r http.Handler, taskType string) *httptest.ResponseRecorder {
	return postJSON(r, "/task/", fmt.Sprintf(`{"type": %q}`, taskType))
}

// putJSON PUTs a JSON string to path and returns the recorder. Used by the
// pause/resume/change-schedule handler tests (server/serve_schedule_test.go).
func putJSON(r http.Handler, path string, body string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("PUT", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// putNoBody PUTs with no body -- for endpoints like /pause and /resume
// that take no payload.
func putNoBody(r http.Handler, path string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("PUT", path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// --- Infrastructure endpoints ---

func TestVersionEndpoint(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	req, _ := http.NewRequest("GET", "/version", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, "blanket", resp["name"])
	assert.NotEmpty(t, resp["version"])
}

func TestMetricsEndpoint(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	req, _ := http.NewRequest("GET", "/ops/status/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestTaskTypes_Listed(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	req, _ := http.NewRequest("GET", "/task_type/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var tts []interface{}
	err := json.Unmarshal(w.Body.Bytes(), &tts)
	assert.NoError(t, err)
	assert.Len(t, tts, 1)
}

func TestTaskType_ByName(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	req, _ := http.NewRequest("GET", "/task_type/echo_task", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var tt map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &tt)
	assert.NoError(t, err)
	assert.Equal(t, "echo_task", tt["name"])
}

// --- POST /task/ ---

func TestPostTask_MissingTypeField(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()

	w := postJSON(s.GetRouter(), "/task/", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPostTask_UnknownType(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()

	w := postTask(s.GetRouter(), "no_such_task_type")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPostTask_Valid(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := postTask(r, "echo_task")
	assert.Equal(t, http.StatusCreated, w.Code)

	var task map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &task)
	assert.NoError(t, err)
	assert.Equal(t, "echo_task", task["type"])
	assert.Equal(t, "WAITING", task["state"])
	assert.NotEmpty(t, task["id"])
}

func TestCreateTask_Valid(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)
	assert.Equal(t, "echo_task", tsk.TypeId)
	assert.Equal(t, "WAITING", tsk.State)

	saved, err := s.DB.GetTask(tsk.Id)
	assert.NoError(t, err)
	assert.Equal(t, tsk.Id, saved.Id)
}

func TestCreateTask_UnknownType(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, err := s.createTask(context.Background(), "does-not-exist", nil)
	assert.Error(t, err)
}

func TestNewTaskForType_MissingRequiredEnv(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	typesDir, err := os.MkdirTemp("", "blanket-test-types-*")
	assert.NoError(t, err)
	defer os.RemoveAll(typesDir)
	err = os.WriteFile(filepath.Join(typesDir, "needs_env.toml"), []byte(`
command = "echo {{.MSG}}"
executor = "bash"
[[environment.required]]
name = "MSG"
`), 0644)
	assert.NoError(t, err)
	viper.Set("tasks.typesPaths", []string{typesDir})
	defer viper.Set("tasks.typesPaths", nil)

	_, err = s.newTaskForType("needs_env", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required environment")
}

// --- GET /task/:id ---

func TestGetTask_InvalidId(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	req, _ := http.NewRequest("GET", "/task/notanobjectid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGetTask_Exists(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Create a task
	created := postTask(r, "echo_task")
	assert.Equal(t, http.StatusCreated, created.Code)

	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)

	// Fetch it back by ID
	req, _ := http.NewRequest("GET", fmt.Sprintf("/task/%s", createdTask.Id.Hex()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var fetched tasks.Task
	err := json.Unmarshal(w.Body.Bytes(), &fetched)
	assert.NoError(t, err)
	assert.Equal(t, createdTask.Id, fetched.Id)
	assert.Equal(t, "WAITING", fetched.State)
	assert.Equal(t, "echo_task", fetched.TypeId)
}

// --- DELETE /task/:id ---

func TestDeleteTask(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Create a task and verify it exists
	created := postTask(r, "echo_task")
	assert.Equal(t, http.StatusCreated, created.Code)

	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)

	req, _ := http.NewRequest("GET", "/task/", nil)
	assertResponseLength(t, r, req, 1)

	// Delete it
	req, _ = http.NewRequest("DELETE", fmt.Sprintf("/task/%s", createdTask.Id.Hex()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Confirm it's gone
	req, _ = http.NewRequest("GET", "/task/", nil)
	assertResponseLength(t, r, req, 0)
}

// --- PUT /task/:id/cancel ---

func TestCancelTask_Waiting(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Create task (starts in WAITING state)
	created := postTask(r, "echo_task")
	assert.Equal(t, http.StatusCreated, created.Code)

	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)

	// Cancel it
	req, _ := http.NewRequest("PUT", fmt.Sprintf("/task/%s/cancel", createdTask.Id.Hex()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Verify the task is now in STOPPED state
	req, _ = http.NewRequest("GET", fmt.Sprintf("/task/%s", createdTask.Id.Hex()), nil)
	getW := httptest.NewRecorder()
	r.ServeHTTP(getW, req)
	assert.Equal(t, http.StatusOK, getW.Code)

	var stopped tasks.Task
	json.Unmarshal(getW.Body.Bytes(), &stopped)
	assert.Equal(t, "STOPPED", stopped.State)
}

func TestCancelTaskById_Waiting(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	err = s.cancelTaskById(context.Background(), tsk.Id, false)
	assert.NoError(t, err)

	updated, err := s.DB.GetTask(tsk.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", updated.State)
}

func TestCancelTaskById_AlreadyTerminal(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)
	assert.NoError(t, s.DB.FinishTask(tsk.Id, database.FinishState("SUCCESS")))

	err = s.cancelTaskById(context.Background(), tsk.Id, false)
	assert.ErrorIs(t, err, ErrTaskNotCancelable)
}

// putTaskInRunningState registers a worker matching echo_task's tags, claims
// createdTask for it, and marks the task RUNNING — all through the same
// handlers a real worker would call (POST /task/claim/:workerid, PUT
// /task/:id/run). Used by the cancel-a-RUNNING-task tests below, since
// cancelTaskById's RUNNING branch is only reachable from that state.
func putTaskInRunningState(t *testing.T, s *ServerConfig, r *gin.Engine, createdTask tasks.Task) {
	t.Helper()

	wconf := worker.WorkerConf{
		Id:      objectid.NewObjectId(),
		Tags:    []string{"bash", "unix"},
		Stopped: false,
	}
	assert.NoError(t, s.DB.UpdateWorker(&wconf))

	claimReq, _ := http.NewRequest("POST", fmt.Sprintf("/task/claim/%s", wconf.Id.Hex()), nil)
	claimW := httptest.NewRecorder()
	r.ServeHTTP(claimW, claimReq)
	assert.Equal(t, http.StatusOK, claimW.Code)

	runReq, _ := http.NewRequest("PUT", fmt.Sprintf("/task/%s/run", createdTask.Id.Hex()), nil)
	runW := httptest.NewRecorder()
	r.ServeHTTP(runW, runReq)
	assert.Equal(t, http.StatusOK, runW.Code)
}

// TestCancelTask_RunningWithoutForce is the regression test for
// turtlemonvh/blanket#52: cancelling a RUNNING task without ?force=true must
// be rejected (not silently no-op'd or silently applied), since it would
// otherwise kill a subprocess actively executing on a worker.
func TestCancelTask_RunningWithoutForce(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	created := postTask(r, "echo_task")
	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)
	putTaskInRunningState(t, s, r, createdTask)

	req, _ := http.NewRequest("PUT", fmt.Sprintf("/task/%s/cancel", createdTask.Id.Hex()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "force")

	// No-op: task is still RUNNING.
	got, err := s.DB.GetTask(createdTask.Id)
	assert.NoError(t, err)
	assert.Equal(t, "RUNNING", got.State)
}

// TestCancelTask_RunningWithForce is the companion regression test: with
// ?force=true, cancelling a RUNNING task transitions it to STOPPED — the
// server-side half of turtlemonvh/blanket#52. The worker-side half (the
// worker noticing STOPPED and killing the subprocess) is covered by
// TestProcessOne_StoppedMidFlight in worker/worker_test.go.
func TestCancelTask_RunningWithForce(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	created := postTask(r, "echo_task")
	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)
	putTaskInRunningState(t, s, r, createdTask)

	req, _ := http.NewRequest("PUT", fmt.Sprintf("/task/%s/cancel?force=true", createdTask.Id.Hex()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	got, err := s.DB.GetTask(createdTask.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", got.State)
}

// TestCancelTaskById_RunningRequiresForce exercises the same force gate at
// the cancelTaskById level (used directly by the MCP cancel tool).
func TestCancelTaskById_RunningRequiresForce(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()
	r := s.GetRouter()

	created := postTask(r, "echo_task")
	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)
	putTaskInRunningState(t, s, r, createdTask)

	err := s.cancelTaskById(context.Background(), createdTask.Id, false)
	assert.ErrorIs(t, err, ErrRunningTaskRequiresForce)

	got, err := s.DB.GetTask(createdTask.Id)
	assert.NoError(t, err)
	assert.Equal(t, "RUNNING", got.State)

	err = s.cancelTaskById(context.Background(), createdTask.Id, true)
	assert.NoError(t, err)

	got, err = s.DB.GetTask(createdTask.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", got.State)
}

func TestRemoveTaskById(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	err = s.removeTaskById(context.Background(), tsk.Id)
	assert.NoError(t, err)

	_, err = s.DB.GetTask(tsk.Id)
	assert.Error(t, err)
}

// --- PUT /task/:id/progress ---

func TestUpdateProgress_InvalidValue(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	created := postTask(r, "echo_task")
	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)

	// 150 is out of range
	req, _ := http.NewRequest("PUT", fmt.Sprintf("/task/%s/progress?progress=150", createdTask.Id.Hex()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateProgress_Valid(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	created := postTask(r, "echo_task")
	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)

	// Progress updates are only accepted for RUNNING tasks; move it there
	// directly since going through claim+run isn't the point of this test.
	createdTask.State = "RUNNING"
	assert.NoError(t, s.DB.SaveTask(&createdTask))

	req, _ := http.NewRequest("PUT", fmt.Sprintf("/task/%s/progress?progress=50", createdTask.Id.Hex()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestUpdateProgress_WrongState(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Create then cancel so the task is in STOPPED (terminal) state.
	created := postTask(r, "echo_task")
	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)

	cancelReq, _ := http.NewRequest("PUT", fmt.Sprintf("/task/%s/cancel", createdTask.Id.Hex()), nil)
	cancelW := httptest.NewRecorder()
	r.ServeHTTP(cancelW, cancelReq)
	assert.Equal(t, http.StatusOK, cancelW.Code)

	// A progress update on a terminal task should be rejected, not silently
	// accepted. Regression test for turtlemonvh/blanket#49.
	req, _ := http.NewRequest("PUT", fmt.Sprintf("/task/%s/progress?progress=50", createdTask.Id.Hex()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	got, err := s.DB.GetTask(createdTask.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", got.State)
	assert.NotEqual(t, 50, got.Progress)
}

// --- GET /task/ with filters ---

func TestTaskList_FilterByState(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Create 3 tasks
	for i := 0; i < 3; i++ {
		w := postTask(r, "echo_task")
		assert.Equal(t, http.StatusCreated, w.Code)
	}

	req, _ := http.NewRequest("GET", "/task/?states=WAITING", nil)
	assertResponseLength(t, r, req, 3)

	req, _ = http.NewRequest("GET", "/task/?states=RUNNING", nil)
	assertResponseLength(t, r, req, 0)

	req, _ = http.NewRequest("GET", "/task/", nil)
	assertResponseLength(t, r, req, 3)
}

// --- PUT /task/:id/finish ---

func TestFinishTask_Valid(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Create a task; it starts in WAITING, which FinishTask accepts.
	created := postTask(r, "echo_task")
	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)

	url := fmt.Sprintf("/task/%s/finish?state=SUCCESS", createdTask.Id.Hex())
	req, _ := http.NewRequest("PUT", url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Confirm state is now SUCCESS and progress was bumped to 100.
	got, err := s.DB.GetTask(createdTask.Id)
	assert.NoError(t, err)
	assert.Equal(t, "SUCCESS", got.State)
	assert.Equal(t, 100, got.Progress)
}

func TestFinishTask_MissingTask(t *testing.T) {
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Random id that doesn't exist in the DB.
	url := fmt.Sprintf("/task/%s/finish?state=SUCCESS", objectid.NewObjectId().Hex())
	req, _ := http.NewRequest("PUT", url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Missing task id maps to 404 via database.ItemNotFoundError.
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestFinishTask_AlreadyTerminalIsNoop covers what used to be
// TestFinishTask_WrongState, whose expectation changed with
// turtlemonvh/blanket#23 phase 1: finishing an already-terminal task is now
// a 200 no-op rather than a 400.
//
// Rejecting it is what stranded tasks. The worker retries this call, so a
// rejection meant it retried into the same rejection until its deadline and
// then gave up having reported nothing — while the state it was trying to
// report was already recorded. The first terminal state a task reaches
// wins, and saying so with a 200 lets the worker finish cleanly.
// A finish from a non-terminal but ineligible state (CLAIMED) still 400s;
// see TestFinishTask_FromClaimedIsBadRequest.
func TestFinishTask_AlreadyTerminalIsNoop(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Create then cancel so the task is in STOPPED state.
	created := postTask(r, "echo_task")
	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)

	req, _ := http.NewRequest("PUT", fmt.Sprintf("/task/%s/cancel", createdTask.Id.Hex()), nil)
	cancelW := httptest.NewRecorder()
	r.ServeHTTP(cancelW, req)
	assert.Equal(t, http.StatusOK, cancelW.Code)

	// Now finish a STOPPED task — accepted, but the STOPPED sticks.
	url := fmt.Sprintf("/task/%s/finish?state=SUCCESS", createdTask.Id.Hex())
	req, _ = http.NewRequest("PUT", url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	got, err := s.DB.GetTask(createdTask.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", got.State)
}

func TestFinishTask_InvalidState(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	created := postTask(r, "echo_task")
	var createdTask tasks.Task
	json.Unmarshal(created.Body.Bytes(), &createdTask)

	// RUNNING is not a valid terminal state.
	url := fmt.Sprintf("/task/%s/finish?state=RUNNING", createdTask.Id.Hex())
	req, _ := http.NewRequest("PUT", url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- PUT /task/:id/progress ---

func TestUpdateProgress_MissingTask(t *testing.T) {
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	url := fmt.Sprintf("/task/%s/progress?progress=50", objectid.NewObjectId().Hex())
	req, _ := http.NewRequest("PUT", url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Missing task id maps to 404 via database.ItemNotFoundError.
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- POST /task/claim/:workerid ---

func TestClaim_MissingWorker(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Put a task on the queue so the claim attempt reaches the worker lookup.
	created := postTask(r, "echo_task")
	assert.Equal(t, http.StatusCreated, created.Code)

	// Random worker id that isn't registered.
	url := fmt.Sprintf("/task/claim/%s", objectid.NewObjectId().Hex())
	req, _ := http.NewRequest("POST", url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Worker not in DB maps to 404 via database.ItemNotFoundError, with a
	// descriptive error string.
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "worker")
}

func TestClaim_NoMatchingTask(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Register a worker with tags that match the task type, but add no tasks
	// to the queue.
	wconf := worker.WorkerConf{
		Id:      objectid.NewObjectId(),
		Tags:    []string{"bash", "unix"},
		Stopped: false,
	}
	assert.NoError(t, s.DB.UpdateWorker(&wconf))

	url := fmt.Sprintf("/task/claim/%s", wconf.Id.Hex())
	req, _ := http.NewRequest("POST", url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Empty queue is a normal polling state — handler returns 204 No Content
	// so idle workers don't spam error logs.
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestClaim_DeletedTaskDoesNotPanic(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	wconf := worker.WorkerConf{
		Id:   objectid.NewObjectId(),
		Tags: []string{"bash", "unix"},
	}
	assert.NoError(t, s.DB.UpdateWorker(&wconf))

	created := postTask(r, "echo_task")
	assert.Equal(t, http.StatusCreated, created.Code)
	var body struct {
		ID string `json:"id"`
	}
	json.NewDecoder(created.Body).Decode(&body)

	taskId := objectid.ObjectIdHex(body.ID)
	assert.NoError(t, s.DB.DeleteTask(taskId))

	url := fmt.Sprintf("/task/claim/%s", wconf.Id.Hex())
	req, _ := http.NewRequest("POST", url, nil)
	w := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		r.ServeHTTP(w, req)
	})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- GET /task/ with additional filters ---

// taggedTaskTypeToml returns a task type TOML with a caller-supplied tag.
func taggedTaskTypeToml(tag string) string {
	return fmt.Sprintf(`
tags = ["%s"]
timeout = 10
command = "echo hi"
executor = "bash"
`, tag)
}

// setupMultipleTaskTypes writes N task types with distinct names and tags to
// a temp dir, points viper at it, and returns a cleanup function.
func setupMultipleTaskTypes(t *testing.T, names []string) func() {
	t.Helper()

	typesDir, err := os.MkdirTemp("", "blanket-test-types-*")
	if err != nil {
		t.Fatalf("failed to create types dir: %v", err)
	}
	resultsDir, err := os.MkdirTemp("", "blanket-test-results-*")
	if err != nil {
		os.RemoveAll(typesDir)
		t.Fatalf("failed to create results dir: %v", err)
	}

	for _, name := range names {
		// Each type gets a single tag matching its name; makes filtering easy.
		err = os.WriteFile(
			filepath.Join(typesDir, name+".toml"),
			[]byte(taggedTaskTypeToml(name)),
			0644,
		)
		if err != nil {
			os.RemoveAll(typesDir)
			os.RemoveAll(resultsDir)
			t.Fatalf("failed to write task type TOML: %v", err)
		}
	}

	viper.Set("tasks.typesPaths", []string{typesDir})
	viper.Set("tasks.resultsPath", resultsDir)

	return func() {
		os.RemoveAll(typesDir)
		os.RemoveAll(resultsDir)
		viper.Set("tasks.typesPaths", nil)
		viper.Set("tasks.resultsPath", "")
	}
}

func TestTaskList_FilterByType(t *testing.T) {
	cleanup := setupMultipleTaskTypes(t, []string{"alpha", "beta"})
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// 2 alpha tasks, 3 beta tasks.
	for i := 0; i < 2; i++ {
		assert.Equal(t, http.StatusCreated, postTask(r, "alpha").Code)
	}
	for i := 0; i < 3; i++ {
		assert.Equal(t, http.StatusCreated, postTask(r, "beta").Code)
	}

	req, _ := http.NewRequest("GET", "/task/?types=alpha", nil)
	assertResponseLength(t, r, req, 2)

	req, _ = http.NewRequest("GET", "/task/?types=beta", nil)
	assertResponseLength(t, r, req, 3)

	req, _ = http.NewRequest("GET", "/task/?types=alpha,beta", nil)
	assertResponseLength(t, r, req, 5)

	req, _ = http.NewRequest("GET", "/task/?types=gamma", nil)
	assertResponseLength(t, r, req, 0)
}

func TestTaskList_FilterByTags(t *testing.T) {
	cleanup := setupMultipleTaskTypes(t, []string{"alpha", "beta"})
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	assert.Equal(t, http.StatusCreated, postTask(r, "alpha").Code)
	assert.Equal(t, http.StatusCreated, postTask(r, "beta").Code)

	// requiredTags: each returned task must have all the listed tags.
	req, _ := http.NewRequest("GET", "/task/?requiredTags=alpha", nil)
	assertResponseLength(t, r, req, 1)

	req, _ = http.NewRequest("GET", "/task/?requiredTags=beta", nil)
	assertResponseLength(t, r, req, 1)

	// maxTags: each task's tags must be a subset of the listed tags.
	// Both types have one tag each, so both tasks pass.
	req, _ = http.NewRequest("GET", "/task/?maxTags=alpha,beta", nil)
	assertResponseLength(t, r, req, 2)

	// maxTags=alpha excludes the beta task (its "beta" tag isn't in the set).
	req, _ = http.NewRequest("GET", "/task/?maxTags=alpha", nil)
	assertResponseLength(t, r, req, 1)
}

func TestTaskList_LimitOffset(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	for i := 0; i < 5; i++ {
		assert.Equal(t, http.StatusCreated, postTask(r, "echo_task").Code)
	}

	req, _ := http.NewRequest("GET", "/task/?limit=2", nil)
	assertResponseLength(t, r, req, 2)

	req, _ = http.NewRequest("GET", "/task/?limit=2&offset=3", nil)
	// 5 total - offset 3 - limit 2 = 2 returned
	assertResponseLength(t, r, req, 2)

	req, _ = http.NewRequest("GET", "/task/?limit=2&offset=4", nil)
	// Only 1 left after offset=4
	assertResponseLength(t, r, req, 1)
}

func TestTaskList_JustCounts(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	for i := 0; i < 4; i++ {
		assert.Equal(t, http.StatusCreated, postTask(r, "echo_task").Code)
	}

	req, _ := http.NewRequest("GET", "/task/?count=true", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "4", strings.TrimSpace(w.Body.String()))
}

func TestTaskList_CreatedAfterBefore(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Create a task, note the boundary, then create another.
	before := postTask(r, "echo_task")
	assert.Equal(t, http.StatusCreated, before.Code)

	// Use the created task's id timestamp as a pivot.
	var t1 tasks.Task
	json.Unmarshal(before.Body.Bytes(), &t1)
	pivot := t1.Id.Timestamp().Unix()

	// Sleep a tick so the next task's id timestamp is strictly later.
	time.Sleep(1100 * time.Millisecond)
	after := postTask(r, "echo_task")
	assert.Equal(t, http.StatusCreated, after.Code)

	// createdAfter pivot → just the second task.
	req, _ := http.NewRequest(
		"GET",
		fmt.Sprintf("/task/?createdAfter=%d", pivot+1),
		nil,
	)
	assertResponseLength(t, r, req, 1)

	// createdBefore uses NewObjectIdWithTime(t) as an upper bound, which
	// zeros the trailing 8 bytes — so a task with the *same* timestamp
	// compares greater. Use pivot+1 to include t1 but still exclude t2.
	req, _ = http.NewRequest(
		"GET",
		fmt.Sprintf("/task/?createdBefore=%d", pivot+1),
		nil,
	)
	assertResponseLength(t, r, req, 1)
}

// --- POST /task/ multipart ---

// TestPostTask_MultipartUpload exercises the multipart path: the "data" field
// carries the JSON task config, and additional form files are dropped into
// the task's ResultDir so the worker can find them.
func TestPostTask_MultipartUpload(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// The "data" field: JSON task config (same as POST body would contain).
	err := writer.WriteField("data", `{"type": "echo_task"}`)
	assert.NoError(t, err)

	// An attached file; the handler writes this into t.ResultDir/<fieldname>.
	part, err := writer.CreateFormFile("payload.txt", "payload.txt")
	assert.NoError(t, err)
	_, err = part.Write([]byte("hello attachment"))
	assert.NoError(t, err)
	writer.Close()

	req, _ := http.NewRequest("POST", "/task/", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var created tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.NotEmpty(t, created.ResultDir)

	// The uploaded file should be at the root of the task's ResultDir.
	got, err := os.ReadFile(filepath.Join(created.ResultDir, "payload.txt"))
	assert.NoError(t, err)
	assert.Equal(t, "hello attachment", string(got))
}

// --- PUT /task/:id/run + /finish idempotency (turtlemonvh/blanket#23 phase 1) ---
//
// The table these tests encode:
//
//	transition | task state | stored runId | caller runId | result
//	-----------+------------+--------------+--------------+--------------------
//	run        | CLAIMED    | ""           | R1           | 200, stored = R1
//	run        | RUNNING    | R1           | R1           | 200 no-op
//	run        | RUNNING    | R1           | R2           | 409
//	run        | RUNNING    | R1           | ""           | 200 no-op (legacy)
//	run        | STOPPED    | R1           | R1           | 409
//	finish     | RUNNING    | R1           | R1           | 200, terminal
//	finish     | SUCCESS    | R1           | R1           | 200 no-op
//	finish     | RUNNING    | R1           | R2           | 409
//	finish     | RUNNING    | R1           | ""           | 200 (legacy)
//	finish     | STOPPED    | R1           | R1           | 200 no-op, STOPPED wins

// claimTaskFor registers a worker matching echo_task's tags and claims the
// oldest matching task for it, leaving that task CLAIMED.
func claimTaskFor(t *testing.T, s *ServerConfig, r *gin.Engine) objectid.ObjectId {
	t.Helper()

	wconf := worker.WorkerConf{
		Id:      objectid.NewObjectId(),
		Tags:    []string{"bash", "unix"},
		Stopped: false,
	}
	assert.NoError(t, s.DB.UpdateWorker(&wconf))

	claimReq, _ := http.NewRequest("POST", fmt.Sprintf("/task/claim/%s", wconf.Id.Hex()), nil)
	claimW := httptest.NewRecorder()
	r.ServeHTTP(claimW, claimReq)
	assert.Equal(t, http.StatusOK, claimW.Code)

	var claimed tasks.Task
	assert.NoError(t, json.Unmarshal(claimW.Body.Bytes(), &claimed))
	return claimed.Id
}

// putTransition issues PUT /task/:id/<verb> with the given query string and
// returns the response code.
func putTransition(r *gin.Engine, taskId objectid.ObjectId, verb, query string) int {
	req, _ := http.NewRequest("PUT", fmt.Sprintf("/task/%s/%s?%s", taskId.Hex(), verb, query), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// newClaimedTask sets up a task type, a task, and a worker that has claimed
// it — the starting point for every row of the table above.
func newClaimedTask(t *testing.T) (*ServerConfig, *gin.Engine, objectid.ObjectId, func()) {
	t.Helper()

	cleanupType := setupTestTaskType(t)
	s, scleanup := NewTestServer()
	r := s.GetRouter()

	postTask(r, "echo_task")
	taskId := claimTaskFor(t, s, r)

	return s, r, taskId, func() {
		scleanup()
		cleanupType()
	}
}

func TestRunTask_RepeatWithSameRunIdIsNoop(t *testing.T) {
	s, r, taskId, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "runId=RUN1&pid=4242&timeout=10"))

	got, err := s.DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, "RUNNING", got.State)
	assert.Equal(t, "RUN1", got.RunId)
	assert.Equal(t, 4242, got.Pid)

	// The worker never heard the first response and retried.
	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "runId=RUN1&pid=4242&timeout=10"))

	got, err = s.DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, "RUNNING", got.State)
	assert.Equal(t, "RUN1", got.RunId)
}

func TestRunTask_DifferentRunIdIsConflict(t *testing.T) {
	s, r, taskId, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "runId=RUN1&pid=1&timeout=10"))
	assert.Equal(t, http.StatusConflict, putTransition(r, taskId, "run", "runId=RUN2&pid=2&timeout=10"))

	// The first run still owns the task.
	got, err := s.DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, "RUN1", got.RunId)
	assert.Equal(t, 1, got.Pid)
}

// A worker built before the fencing token existed sends no runId at all.
// Until the phase 4 schema bump those calls must keep working.
func TestRunTask_EmptyRunIdIsLegacyPermissive(t *testing.T) {
	s, r, taskId, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "runId=RUN1&pid=1&timeout=10"))
	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "pid=1&timeout=10"))

	got, err := s.DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, "RUNNING", got.State)
	assert.Equal(t, "RUN1", got.RunId)
}

// A user's stop beats a worker that gets around to starting the task
// afterwards; the worker is told 409 so it doesn't retry into it forever.
func TestRunTask_AfterUserStopIsConflict(t *testing.T) {
	s, r, taskId, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "runId=RUN1&pid=1&timeout=10"))
	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "cancel", "force=true"))

	assert.Equal(t, http.StatusConflict, putTransition(r, taskId, "run", "runId=RUN1&pid=1&timeout=10"))

	got, err := s.DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", got.State)
}

func TestFinishTask_RepeatWithSameRunIdIsNoop(t *testing.T) {
	s, r, taskId, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "runId=RUN1&pid=1&timeout=10"))
	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "finish", "state=SUCCESS&runId=RUN1"))
	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "finish", "state=SUCCESS&runId=RUN1"))

	got, err := s.DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, "SUCCESS", got.State)
	assert.Equal(t, 100, got.Progress)
}

func TestFinishTask_DifferentRunIdIsConflict(t *testing.T) {
	s, r, taskId, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "runId=RUN1&pid=1&timeout=10"))
	assert.Equal(t, http.StatusConflict, putTransition(r, taskId, "finish", "state=SUCCESS&runId=RUN2"))

	got, err := s.DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, "RUNNING", got.State)
}

func TestFinishTask_EmptyRunIdIsLegacyPermissive(t *testing.T) {
	s, r, taskId, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "runId=RUN1&pid=1&timeout=10"))
	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "finish", "state=SUCCESS"))

	got, err := s.DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, "SUCCESS", got.State)
}

// The row that matters most: a user cancels a RUNNING task, the worker
// kills the child, and the child's exit is then reported as ERROR. The
// task must stay STOPPED — and the worker must not be told it failed, or
// it will retry the ERROR until its deadline and then leave the outcome
// journal behind as if the state had been lost.
func TestFinishTask_LateErrorAfterUserStopKeepsStopped(t *testing.T) {
	s, r, taskId, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "runId=RUN1&pid=1&timeout=10"))
	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "cancel", "force=true"))

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "finish", "state=ERROR&runId=RUN1"))

	got, err := s.DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", got.State)
}

// TestFinishTask_RepeatKeepsStoredExitCode is the seam between the two
// features that landed on this handler at once: the idempotent finish
// (turtlemonvh/blanket#23 phase 1) and the recorded exit code
// (turtlemonvh/blanket#27). A retried finish is a 200 no-op, and a no-op
// must leave the exit code the first report stored exactly as it is —
// whether the retry carries no code, a different code, or no runId at all.
// Downgrading a recorded exit status back to "unknown" would be a silent
// data loss for any caller waiting on POST /task/?wait.
func TestFinishTask_RepeatKeepsStoredExitCode(t *testing.T) {
	s, r, taskId, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "runId=RUN1&pid=1&timeout=10"))
	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "finish", "state=ERROR&runId=RUN1&exitCode=3"))

	got, err := s.DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, "ERROR", got.State)
	if assert.NotNil(t, got.ExitCode) {
		assert.Equal(t, 3, *got.ExitCode)
	}

	// The retries a worker actually makes after a lost response.
	for _, params := range []string{
		"state=ERROR&runId=RUN1",            // same run, no code this time
		"state=ERROR&runId=RUN1&exitCode=9", // same run, contradicting code
		"state=ERROR",                       // legacy worker, no token at all
		"state=SUCCESS&runId=RUN1",          // a late, different terminal state
	} {
		assert.Equal(t, http.StatusOK, putTransition(r, taskId, "finish", params), params)
		got, err = s.DB.GetTask(taskId)
		assert.NoError(t, err)
		assert.Equal(t, "ERROR", got.State, params)
		if assert.NotNil(t, got.ExitCode, params) {
			assert.Equal(t, 3, *got.ExitCode, params)
		}
	}
}

// A CLAIMED task has not started yet, so a finish for it is a genuine
// client error rather than a replay — it keeps the historical 400.
func TestFinishTask_FromClaimedIsBadRequest(t *testing.T) {
	_, r, taskId, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusBadRequest, putTransition(r, taskId, "finish", "state=SUCCESS&runId=RUN1"))
}

// TestUpdateProgress_RunIdMismatchIsConflict: progress carries the same
// fencing token as the other transitions, so a stale runner can't paint
// over the live one's numbers. An absent token stays permitted — task
// scripts reporting their own progress have no way to know it.
func TestUpdateProgress_RunIdFencing(t *testing.T) {
	s, r, taskId, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "run", "runId=RUN1&pid=1&timeout=10"))

	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "progress", "progress=25&runId=RUN1"))
	assert.Equal(t, http.StatusOK, putTransition(r, taskId, "progress", "progress=50"))
	assert.Equal(t, http.StatusConflict, putTransition(r, taskId, "progress", "progress=99&runId=RUN2"))

	got, err := s.DB.GetTask(taskId)
	assert.NoError(t, err)
	assert.Equal(t, 50, got.Progress)
}
