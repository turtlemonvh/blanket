// Tests for the HTMX + Go-template UI at /ui/. Covers the handler
// surface that would otherwise only be exercised by browser tests:
//   - filter form renders on the tasks page
//   - tasks-rows partial accepts multi-value query params (states=A&states=B)
//     and date-string createdAfter (datetime-local / RFC3339)
//   - custom-env-row partial renders the name/value inputs
//   - POST /ui/tasks zips customEnvName/customEnvValue pairs into ExecEnv

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func getUI(r http.Handler, path string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func postForm(r http.Handler, path string, values url.Values) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("POST", path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// --- /ui/ shell ---

func TestUI_TasksPage_RendersFilterForm(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "<title>Blanket")
	assert.Contains(t, body, `id="task-filter"`)
	assert.Contains(t, body, `name="states"`)
	assert.Contains(t, body, `name="types"`)
	assert.Contains(t, body, `name="requiredTags"`)
	assert.Contains(t, body, `name="createdAfter"`)
	// The echo_task fixture should be one of the type checkboxes.
	assert.Contains(t, body, `value="echo_task"`)
}

func TestUI_RootRedirectsToUI(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := getUI(r, "/")
	assert.Equal(t, http.StatusMovedPermanently, w.Code)
	assert.Equal(t, "/ui/", w.Header().Get("Location"))
}

// --- /ui/partials/tasks-rows with filters ---

func TestUI_TasksRows_MultiValueStates(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// Two tasks, both WAITING by default.
	postTask(r, "echo_task")
	postTask(r, "echo_task")

	// states=WAITING&states=STOPPED — both should be accepted as a
	// multi-value query, and the WAITING rows should be present. Count the
	// badge text (>WAITING<), not the class name, so we count rows 1:1.
	w := getUI(r, "/ui/partials/tasks-rows?states=WAITING&states=STOPPED")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Equal(t, 2, strings.Count(body, ">WAITING<"),
		"two WAITING task rows expected in filtered partial")

	// A state that matches nothing should yield the empty-state row.
	w2 := getUI(r, "/ui/partials/tasks-rows?states=STOPPED")
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Contains(t, w2.Body.String(), "No tasks.")
}

func TestUI_TasksRows_DateStringFilter(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()
	postTask(r, "echo_task")

	// createdBefore in the distant past → the task shouldn't match.
	w := getUI(r, "/ui/partials/tasks-rows?createdBefore=2000-01-01T00:00")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "No tasks.",
		"datetime-local createdBefore should filter out tasks")

	// A far-future createdBefore should let the task through.
	w2 := getUI(r, "/ui/partials/tasks-rows?createdBefore=2099-01-01T00:00")
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Contains(t, w2.Body.String(), "WAITING")
}

// --- /ui/partials/custom-env-row ---

func TestUI_CustomEnvRowPartial(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/partials/custom-env-row")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `name="customEnvName"`)
	assert.Contains(t, body, `name="customEnvValue"`)
	assert.Contains(t, body, ">Custom<")
}

// --- POST /ui/tasks ---

func TestUI_SubmitTask_MergesCustomEnv(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	form := url.Values{}
	form.Set("type", "echo_task")
	form.Add("customEnvName", "COLOR")
	form.Add("customEnvValue", "orange")
	form.Add("customEnvName", "SIZE")
	form.Add("customEnvValue", "large")
	// Blank-name rows are emitted by the UI when the user adds a row but
	// doesn't type a setting name; they should be silently dropped.
	form.Add("customEnvName", "")
	form.Add("customEnvValue", "ignored")

	w := postForm(r, "/ui/tasks", form)
	assert.Equal(t, http.StatusOK, w.Code,
		"submit task via UI should succeed; body: %s", w.Body.String())

	// Fetch the task list through the JSON API and confirm the merged env
	// on the persisted task.
	req, _ := http.NewRequest("GET", "/task/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var list []struct {
		DefaultEnv map[string]string `json:"defaultEnv"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode /task/: %v; body=%s", err, rec.Body.String())
	}
	if assert.Len(t, list, 1) {
		env := list[0].DefaultEnv
		assert.Equal(t, "orange", env["COLOR"])
		assert.Equal(t, "large", env["SIZE"])
		_, blankExists := env[""]
		assert.False(t, blankExists, "blank-named custom env row should be dropped")
	}
}
