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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
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

// TestUI_SubmitTask_TriggersWarningsFollowup covers #64: submitting against
// a task type with warning-level validation findings (missing
// description/documentation, here) must not block task creation, but must
// tell the client to fetch and display them. The POST response's body is
// (and must stay) raw <tr> rows for #tasks-rows — htmx's table-parsing
// context would silently drop an <hx-swap-oob> element appended there (see
// the comment in uiSubmitTask) — so warnings are signaled via an HX-Trigger
// header instead, naming the task type for a follow-up GET.
func TestUI_SubmitTask_TriggersWarningsFollowup(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	// minimalTaskTypeToml (from serve_tasks_test.go) has neither a
	// description nor documentation, tripping the 006/007 warn-level checks.
	form := url.Values{}
	form.Set("type", "echo_task")

	w := postForm(r, "/ui/tasks", form)
	assert.Equal(t, http.StatusOK, w.Code,
		"warnings must not block submission; body: %s", w.Body.String())

	trigger := w.Header().Get("HX-Trigger")
	assert.Contains(t, trigger, "task-type-warnings")
	assert.Contains(t, trigger, "echo_task")

	// The task itself was still created despite the warnings.
	rows := getUI(r, "/ui/partials/tasks-rows")
	assert.Equal(t, http.StatusOK, rows.Code)
	assert.Contains(t, rows.Body.String(), "WAITING")
}

// TestUI_TaskTypeWarningsPartial_RendersFindings is the follow-up request
// #flash-area issues after seeing the HX-Trigger event above: it
// re-validates the named task type and should render its warning-level
// findings into the self-referential OOB swap.
func TestUI_TaskTypeWarningsPartial_RendersFindings(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/partials/task-type-warnings?type=echo_task")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, `hx-swap-oob="innerHTML:#flash-area"`)
	assert.Contains(t, body, "006")
	assert.Contains(t, body, "007")
	assert.Contains(t, body, "description is missing or empty")
	assert.Contains(t, body, "documentation is missing or empty")
}

// TestUI_TaskTypeWarningsPartial_NoWarningsIsEmpty confirms a clean task
// type still swaps (clearing any stale message from an earlier submission)
// but with no warning content.
func TestUI_TaskTypeWarningsPartial_NoWarningsIsEmpty(t *testing.T) {
	cleanup := setupDocsTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/partials/task-type-warnings?type=greet_task")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, `hx-swap-oob="innerHTML:#flash-area"`)
	assert.NotContains(t, body, "flash-warning")
}

// --- POST /ui/workers ---

// TestUI_SubmitWorker_RejectsLowCheckInterval is the UI side of the
// hot-spin guard: a checkInterval below MIN_CHECK_INTERVAL_SECONDS must
// fail with 400 instead of silently launching a worker that would then
// hammer /task/claim/.
func TestUI_SubmitWorker_RejectsLowCheckInterval(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	form := url.Values{}
	form.Set("tags", "exec:bash,os:unix")
	form.Set("checkInterval", "0.1")

	w := postForm(r, "/ui/workers", form)
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"sub-minimum checkInterval should return 400; body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "checkInterval")
}

// --- /ui/task-types ---

const docsTaskTypeToml = `
description = "Say hello"

documentation = '''
Requires nothing. Writes a greeting to stdout.
'''

tags = ["exec:bash", "os:unix"]
timeout = 10
command = "echo 'hello from blanket'"
executor = "bash"
`

// setupDocsTaskType writes a task type with description/documentation set,
// distinct from the shared minimal fixture in serve_tasks_test.go.
func setupDocsTaskType(t *testing.T) func() {
	t.Helper()

	typesDir, err := os.MkdirTemp("", "blanket-test-types-docs-*")
	if err != nil {
		t.Fatalf("failed to create types dir: %v", err)
	}
	resultsDir, err := os.MkdirTemp("", "blanket-test-results-docs-*")
	if err != nil {
		os.RemoveAll(typesDir)
		t.Fatalf("failed to create results dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(typesDir, "greet_task.toml"), []byte(docsTaskTypeToml), 0644); err != nil {
		os.RemoveAll(typesDir)
		os.RemoveAll(resultsDir)
		t.Fatalf("failed to write task type TOML: %v", err)
	}

	viper.Set("tasks.typesPaths", []string{typesDir})
	viper.Set("tasks.resultsPath", resultsDir)

	return func() {
		os.RemoveAll(typesDir)
		os.RemoveAll(resultsDir)
	}
}

func TestUI_TaskTypesRows_ShowsDescriptionAndDetailLink(t *testing.T) {
	cleanup := setupDocsTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/partials/task-types-rows")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "Say hello")
	assert.Contains(t, body, `href="/ui/task-types/greet_task"`)
	assert.NotContains(t, body, `href="/task_type/greet_task"`,
		"list should link to the UI detail page, not the JSON API route")
}

func TestUI_TaskTypeDetailPage_RendersDescriptionAndDocumentation(t *testing.T) {
	cleanup := setupDocsTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/task-types/greet_task")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "Say hello")
	assert.Contains(t, body, "Requires nothing. Writes a greeting to stdout.")
	assert.Contains(t, body, "exec:bash, os:unix")
}

func TestUI_TaskTypeDetailPage_UnknownTypeReturns404(t *testing.T) {
	cleanup := setupDocsTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/task-types/does_not_exist")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- Scheduling on the new-task form (turtlemonvh/blanket#97) ---
//
// The browser-facing half of these (the checkbox actually revealing the
// section, the radios swapping fields, the live preview updating as you
// type) is covered by tests/e2e/specs/journeys.spec.ts; what's below is
// the handler surface underneath it.

// scheduleRow is the slice of a task's JSON these tests assert on, so they
// check what was persisted rather than what was rendered.
type scheduleRow struct {
	State       string `json:"state"`
	ScheduledTs int64  `json:"scheduledTs"`
	CronExpr    string `json:"cronExpr"`
	Description string `json:"scheduleDescription"`
}

func taskScheduleRows(t *testing.T, r http.Handler) []scheduleRow {
	t.Helper()
	rec := getUI(r, "/task/")
	assert.Equal(t, http.StatusOK, rec.Code)
	var list []scheduleRow
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode /task/: %v; body=%s", err, rec.Body.String())
	}
	return list
}

func TestUI_NewTaskForm_RendersScheduleSection(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/partials/new-task")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	// The opt-in checkbox and the one-time/repeating radio group.
	assert.Contains(t, body, `name="scheduleEnabled"`)
	assert.Contains(t, body, ">Schedule task?</label>")
	assert.Contains(t, body, `name="scheduleMode" value="once"`)
	assert.Contains(t, body, `name="scheduleMode" value="repeating"`)
	assert.Contains(t, body, ">One time</label>")
	assert.Contains(t, body, ">Repeating</label>")

	// Both fields, each with help text tied to it for screen readers.
	assert.Contains(t, body, `type="datetime-local"`)
	assert.Contains(t, body, `name="notBefore"`)
	assert.Contains(t, body, `name="cron"`)
	assert.Contains(t, body, `aria-describedby="schedule-not-before-help"`)
	assert.Contains(t, body, `aria-describedby="schedule-cron-help"`)

	// The cron field drives the live preview.
	assert.Contains(t, body, `hx-get="/ui/partials/schedule-preview"`)
	assert.Contains(t, body, `hx-trigger="keyup changed delay:300ms, change"`)
	assert.Contains(t, body, `id="schedule-preview"`)

	// And a place for a rejected submit to land (htmx won't swap a 4xx).
	assert.Contains(t, body, `id="task-form-error"`)
}

func TestUI_SchedulePreviewPartial_ValidCron(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/partials/schedule-preview?cron=%2A%2F5+%2A+%2A+%2A+%2A")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "Every 5 minutes")
	assert.Contains(t, body, "schedule-next", "preview should list upcoming fire times")
	assert.NotContains(t, body, "schedule-error")
}

// An unparseable expression is *content*, not a failed request: htmx only
// swaps 2xx responses, so a 400 here would leave the user typing into a
// field that never says anything is wrong.
func TestUI_SchedulePreviewPartial_InvalidCronRendersInlineError(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/partials/schedule-preview?cron=not-a-cron")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "schedule-error")
	assert.Contains(t, body, "invalid cron expression")
	assert.Contains(t, body, `role="alert"`)
}

func TestUI_SchedulePreviewPartial_EmptyCronShowsHint(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/partials/schedule-preview?cron=")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Type a cron expression")
}

func TestUI_SubmitTask_OneTimeCreatesScheduledTask(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	future := time.Now().Add(2 * time.Hour)
	form := url.Values{}
	form.Set("type", "echo_task")
	form.Set("scheduleEnabled", "1")
	form.Set("scheduleMode", "once")
	form.Set("notBefore", future.Format("2006-01-02T15:04"))

	w := postForm(r, "/ui/tasks", form)
	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	list := taskScheduleRows(t, r)
	if assert.Len(t, list, 1) {
		assert.Equal(t, "SCHEDULED", list[0].State)
		assert.InDelta(t, future.Unix(), list[0].ScheduledTs, 60,
			"a bare datetime-local value should be read as server-local time")
		assert.Contains(t, list[0].Description, "Once, at")
	}

	// The flash tells the user the task is waiting on a schedule rather
	// than queued — the refreshed rows alone don't make that obvious.
	assert.Contains(t, w.Header().Get("HX-Trigger"), "SCHEDULED - Once, at")
}

// The hidden notBeforeISO field carries the instant resolved in the
// *browser's* timezone, and wins over the bare datetime-local value —
// otherwise a browser and server in different zones disagree about what
// "2:30 PM" meant.
func TestUI_SubmitTask_NotBeforeISOWinsOverLocalValue(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	future := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Minute)
	form := url.Values{}
	form.Set("type", "echo_task")
	form.Set("scheduleEnabled", "1")
	form.Set("scheduleMode", "once")
	// Deliberately disagreeing values; the ISO one should win.
	form.Set("notBefore", "2035-01-01T00:00")
	form.Set("notBeforeISO", future.Format(time.RFC3339))

	w := postForm(r, "/ui/tasks", form)
	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	list := taskScheduleRows(t, r)
	if assert.Len(t, list, 1) {
		assert.Equal(t, "SCHEDULED", list[0].State)
		assert.Equal(t, future.Unix(), list[0].ScheduledTs)
	}
}

func TestUI_SubmitTask_RepeatingCreatesRecurringTemplate(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	form := url.Values{}
	form.Set("type", "echo_task")
	form.Set("scheduleEnabled", "1")
	form.Set("scheduleMode", "repeating")
	form.Set("cron", "*/5 * * * *")
	// A stale value left behind in the hidden "one time" panel must not
	// reach applySchedule as a second, conflicting schedule.
	form.Set("notBefore", "2035-01-01T00:00")

	w := postForm(r, "/ui/tasks", form)
	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	list := taskScheduleRows(t, r)
	if assert.Len(t, list, 1) {
		assert.Equal(t, "RECURRING", list[0].State)
		assert.Equal(t, "*/5 * * * *", list[0].CronExpr)
		assert.Equal(t, "Every 5 minutes", list[0].Description)
		assert.Zero(t, list[0].ScheduledTs, "the ignored notBefore must not be applied")
	}
	assert.Contains(t, w.Header().Get("HX-Trigger"), "RECURRING - Every 5 minutes")
}

// Unchecked "Schedule task?" (or scripting off, which submits the fields
// empty rather than not at all) still means "run it now".
func TestUI_SubmitTask_NoScheduleStaysWaiting(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	form := url.Values{}
	form.Set("type", "echo_task")
	form.Set("scheduleMode", "once")
	form.Set("notBefore", "")
	form.Set("cron", "")

	w := postForm(r, "/ui/tasks", form)
	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	list := taskScheduleRows(t, r)
	if assert.Len(t, list, 1) {
		assert.Equal(t, "WAITING", list[0].State)
	}
	assert.Contains(t, w.Header().Get("HX-Trigger"), `"scheduled":""`,
		"an unscheduled task should not flash a schedule message")
}

// POST /task/ treats an already-past notBefore as "run now"; through a
// date picker that's almost always a mistake, so the form rejects it —
// with a message the user can actually see (HX-Trigger, since htmx won't
// swap a 4xx response body).
func TestUI_SubmitTask_PastNotBeforeIsRejected(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	form := url.Values{}
	form.Set("type", "echo_task")
	form.Set("scheduleEnabled", "1")
	form.Set("scheduleMode", "once")
	form.Set("notBefore", time.Now().Add(-2*time.Hour).Format("2006-01-02T15:04"))

	w := postForm(r, "/ui/tasks", form)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "in the past")
	assert.Contains(t, w.Header().Get("HX-Trigger"), "task-form-error")

	assert.Len(t, taskScheduleRows(t, r), 0, "a rejected submit must not create a task")
}

func TestUI_SubmitTask_InvalidCronIsRejected(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	form := url.Values{}
	form.Set("type", "echo_task")
	form.Set("scheduleEnabled", "1")
	form.Set("scheduleMode", "repeating")
	form.Set("cron", "not-a-cron")

	w := postForm(r, "/ui/tasks", form)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid cron expression")
	assert.Contains(t, w.Header().Get("HX-Trigger"), "task-form-error")
	assert.Len(t, taskScheduleRows(t, r), 0)
}

// With no scheduleMode to disambiguate (a hand-rolled POST rather than the
// form), both fields are passed through and the API's own mutual-exclusion
// rule applies.
func TestUI_SubmitTask_BothFieldsWithoutModeIsRejected(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	form := url.Values{}
	form.Set("type", "echo_task")
	form.Set("notBefore", "2035-01-01T00:00")
	form.Set("cron", "*/5 * * * *")

	w := postForm(r, "/ui/tasks", form)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "mutually exclusive")
	assert.Len(t, taskScheduleRows(t, r), 0)
}

func TestUI_SubmitTask_UnknownScheduleModeIsRejected(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	form := url.Values{}
	form.Set("type", "echo_task")
	form.Set("scheduleMode", "sometimes")

	w := postForm(r, "/ui/tasks", form)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "unknown schedule mode")
}

func TestUI_FormErrorPartial_RendersMessage(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/partials/form-error?error=start+time+is+in+the+past")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "flash-error")
	assert.Contains(t, body, `role="alert"`)
	assert.Contains(t, body, "start time is in the past")

	// No message → nothing rendered, so a later successful submit clears
	// a stale error.
	empty := getUI(r, "/ui/partials/form-error?error=")
	assert.Equal(t, http.StatusOK, empty.Code)
	assert.NotContains(t, empty.Body.String(), "flash-error")
}

// The scheduled flash rides the same follow-up request as the task-type
// warnings; both can render at once.
func TestUI_TaskTypeWarningsPartial_RendersScheduledFlash(t *testing.T) {
	cleanup := setupDocsTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/partials/task-type-warnings?type=greet_task&scheduled=RECURRING+-+Every+5+minutes")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `hx-swap-oob="innerHTML:#flash-area"`)
	assert.Contains(t, body, "flash-success")
	assert.Contains(t, body, "RECURRING - Every 5 minutes")
}
