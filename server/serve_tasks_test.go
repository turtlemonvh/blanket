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
//   - PUT /task/:id/finish:
//       * valid transition from RUNNING → SUCCESS / ERROR
//       * task id that does not exist → 404
//       * wrong-state transition (e.g. WAITING → SUCCESS) → rejected
//   - PUT /task/:id/progress:
//       * task id that does not exist → 404
//       * wrong-state (e.g. SUCCESS task) → rejected
//   - POST /task/claim/:workerid:
//       * worker id that does not exist → 4xx (don't silently claim)
//       * no matching task for worker's tags → empty / appropriate status

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/tasks"
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
