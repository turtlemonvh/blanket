// TODO: additional HTTP handler tests to write
//
// Covered:
//   - POST /task/ with JSON body: TestPostTask_Valid, TestPostTask_MissingTypeField,
//     TestPostTask_UnknownType
//   - GET /task/:id:            TestGetTask_InvalidId, TestGetTask_Exists
//   - GET /task/ + state filter: TestTaskList_FilterByState
//   - DELETE /task/:id:         TestDeleteTask
//   - PUT /task/:id/cancel from WAITING: TestCancelTask_Waiting
//   - PUT /task/:id/progress (valid + out-of-range): TestUpdateProgress_Valid,
//     TestUpdateProgress_InvalidValue
//   - PUT /task/:id/progress (missing task): TestUpdateProgress_MissingTask
//   - PUT /task/:id/finish: TestFinishTask_Valid, TestFinishTask_MissingTask,
//     TestFinishTask_WrongState, TestFinishTask_InvalidState
//   - POST /task/claim/:workerid edges: TestClaim_MissingWorker,
//     TestClaim_NoMatchingTask, TestClaim_DeletedTaskDoesNotPanic
//   - claim-task happy path: covered by worker integration test TestProcessOne
//
// Not yet covered:
//   - POST /task/ with multipart form + file uploads (data=@file, extra files
//     placed at the task's working dir root)
//   - GET /task/ with the full filter flag set (not just state): type, tags,
//     created-before/after, limit, offset, sort order, etc.
//   - PUT /task/:id/stop (distinct from cancel: stop applies to a RUNNING task
//     and should signal the worker)
//   - cancel-then-still-try-to-run: ensure the worker observes the tombstone
//     and refuses/stops the task cleanly
//   - PUT /task/:id/progress: wrong-state rejection — the handler currently
//     doesn't check state, see docs/NextUp.md

package server

import (
	"bytes"
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

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
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

	req, _ := http.NewRequest("PUT", fmt.Sprintf("/task/%s/progress?progress=50", createdTask.Id.Hex()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
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

	// Current handler returns 400 for any DB error. docs/NextUp.md tracks
	// normalizing this to 404 for ItemNotFoundError.
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestFinishTask_WrongState(t *testing.T) {
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

	// Now try to finish a STOPPED task — should be rejected.
	url := fmt.Sprintf("/task/%s/finish?state=SUCCESS", createdTask.Id.Hex())
	req, _ = http.NewRequest("PUT", url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
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

	// Current handler returns 500 for any DB error; docs/NextUp.md tracks
	// normalizing this to 404 for ItemNotFoundError.
	assert.Equal(t, http.StatusInternalServerError, w.Code)
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

	// Worker not in DB → handler returns 500 with a descriptive error string.
	// Ideally this would be 404; tracked in docs/NextUp.md.
	assert.Equal(t, http.StatusInternalServerError, w.Code)
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
