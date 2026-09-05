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

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
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

// ---------------------------------------------------------------------------
// Scheduling UI (turtlemonvh/blanket#98): the Upcoming page, the series
// detail view, and the series card / row backlink on tasks that belong to
// a series.
//
// These drive the handlers directly; the browser-side behaviour (htmx
// swaps, confirm dialogs, the live cron preview) is covered by
// tests/e2e/specs/scheduling.spec.ts.
// ---------------------------------------------------------------------------

// putForm PUTs url-encoded form values, the way htmx posts the schedule
// editor.
func putForm(r http.Handler, path string, values url.Values) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("PUT", path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// createScheduledTask submits a one-shot task with a future notBefore and
// returns its id.
func createScheduledTask(t *testing.T, r http.Handler) string {
	t.Helper()
	w := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "2h"}`)
	assert.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	var created tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.Equal(t, "SCHEDULED", created.State)
	return created.Id.Hex()
}

// seedChildRun creates a task and re-parents it onto templateId, standing
// in for what the scheduler's fireOnce would have produced. Test servers
// don't run the background scheduler loop, so seeding is both faster and
// deterministic.
func seedChildRun(t *testing.T, s *ServerConfig, r http.Handler, templateId objectid.ObjectId) objectid.ObjectId {
	t.Helper()
	w := postTask(r, "echo_task")
	assert.Equal(t, http.StatusCreated, w.Code)
	var child tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &child))

	stored, err := s.DB.GetTask(child.Id)
	assert.NoError(t, err)
	stored.ParentId = templateId
	assert.NoError(t, s.DB.SaveTask(&stored))
	return stored.Id
}

// --- /ui/upcoming ---

func TestUI_UpcomingPage_SplitsOneTimeFromSeries(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	oneTimeId := createScheduledTask(t, r)
	live := createRecurringTemplate(t, r)
	paused := createRecurringTemplate(t, r)
	assert.Equal(t, http.StatusOK, putNoBody(r, "/task/"+paused.Id.Hex()+"/pause").Code)
	cancelled := createRecurringTemplate(t, r)
	assert.Equal(t, http.StatusOK, putNoBody(r, "/task/"+cancelled.Id.Hex()+"/cancel").Code)

	w := getUI(r, "/ui/upcoming")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	// Both sections render, with their own headings.
	assert.Contains(t, body, ">One-time<")
	assert.Contains(t, body, ">Series<")

	// The one-shot task is listed under One-time with a friendly
	// description and a Cancel action pointed at the API.
	assert.Contains(t, body, `href="/ui/tasks/`+oneTimeId+`"`)
	assert.Contains(t, body, "Once, at ")
	assert.Contains(t, body, `hx-put="/task/`+oneTimeId+`/cancel"`)

	// Live and paused templates are both listed; the cancelled one is not.
	assert.Contains(t, body, `href="/ui/tasks/`+live.Id.Hex()+`"`)
	assert.Contains(t, body, `href="/ui/tasks/`+paused.Id.Hex()+`"`)
	assert.NotContains(t, body, cancelled.Id.Hex(),
		"a cancelled (STOPPED) template is not upcoming")

	// Status badges use the live/paused vocabulary, and a paused series
	// says when it was paused.
	assert.Contains(t, body, ">Live<")
	assert.Contains(t, body, ">Paused ")
	assert.Contains(t, body, "Every 5 minutes")
}

func TestUI_UpcomingPage_EmptyStates(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/upcoming")
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "No one-time tasks scheduled.")
	assert.Contains(t, body, "No scheduled series.")
}

func TestUI_UpcomingNavLinkIsPresent(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	assert.Contains(t, getUI(r, "/ui/").Body.String(), `href="/ui/upcoming"`)
}

// TestUI_UpcomingCancelOneTime covers the Upcoming page's only mutating
// control: cancelling a scheduled one-shot task and re-fetching the tbody
// (what the row's hx-on::after-request does in the browser).
func TestUI_UpcomingCancelOneTime(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	oneTimeId := createScheduledTask(t, r)
	assert.Contains(t, getUI(r, "/ui/partials/upcoming-onetime-rows").Body.String(), oneTimeId)

	assert.Equal(t, http.StatusOK, putNoBody(r, "/task/"+oneTimeId+"/cancel").Code)

	rows := getUI(r, "/ui/partials/upcoming-onetime-rows")
	assert.Equal(t, http.StatusOK, rows.Code)
	assert.NotContains(t, rows.Body.String(), oneTimeId)
	assert.Contains(t, rows.Body.String(), "No one-time tasks scheduled.")
}

// --- series detail at /ui/tasks/:id ---

func TestUI_SeriesDetail_RendersScheduleStatusAndActions(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	id := tmpl.Id.Hex()

	w := getUI(r, "/ui/tasks/"+id)
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	// The series template renders through series_detail.html, not the
	// ordinary task detail page — no log sections for a record that never
	// runs anything itself.
	assert.Contains(t, body, "Series Detail")
	assert.NotContains(t, body, "Live Log")
	assert.NotContains(t, body, "blanket.stdout.log")

	// Friendly schedule + raw cron + next fire.
	assert.Contains(t, body, "Every 5 minutes")
	assert.Contains(t, body, "*/5 * * * *")
	assert.Contains(t, body, "Next fire")

	// Status and the actions that apply to a live series.
	assert.Contains(t, body, ">Live<")
	assert.Contains(t, body, `hx-put="/ui/series/`+id+`/pause"`)
	assert.Contains(t, body, `hx-put="/ui/series/`+id+`/cancel"`)
	assert.Contains(t, body, `hx-put="/ui/series/`+id+`/schedule"`)
	assert.Contains(t, body, `hx-confirm=`)

	// The schedule editor and its live preview.
	assert.Contains(t, body, `name="cron"`)
	assert.Contains(t, body, `hx-get="/ui/partials/schedule-preview"`)

	// Past runs table, wired to the shared tasks-rows partial filtered by
	// parentId.
	assert.Contains(t, body, "Past runs")
	assert.Contains(t, body, "/ui/partials/tasks-rows?parentId="+id)
}

func TestUI_SeriesDetail_ListsPastRuns(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	childId := seedChildRun(t, s, r, tmpl.Id)
	// A task belonging to some *other* series must not show up here.
	other := createRecurringTemplate(t, r)
	strayId := seedChildRun(t, s, r, other.Id)

	w := getUI(r, "/ui/tasks/"+tmpl.Id.Hex())
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, childId.Hex())
	assert.NotContains(t, body, strayId.Hex(),
		"another series' runs must not leak into this one's Past runs")

	// The same rows come back from the partial htmx re-fetches, and there
	// the per-row "part of series" backlink is suppressed (every row on
	// this page belongs to the same series).
	rows := getUI(r, "/ui/partials/tasks-rows?parentId="+tmpl.Id.Hex())
	assert.Equal(t, http.StatusOK, rows.Code)
	assert.Contains(t, rows.Body.String(), childId.Hex())
	assert.NotContains(t, rows.Body.String(), "part of series")
}

func TestUI_SeriesDetail_PausedShowsPausedAtAndResume(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	assert.Equal(t, http.StatusOK, putNoBody(r, "/task/"+tmpl.Id.Hex()+"/pause").Code)

	body := getUI(r, "/ui/tasks/"+tmpl.Id.Hex()).Body.String()
	assert.Contains(t, body, ">Paused<")
	assert.Contains(t, body, "Paused at")
	assert.Contains(t, body, `hx-put="/ui/series/`+tmpl.Id.Hex()+`/resume"`)
	assert.NotContains(t, body, `hx-put="/ui/series/`+tmpl.Id.Hex()+`/pause"`)
}

func TestUI_SeriesDetail_CancelledHidesActions(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	assert.Equal(t, http.StatusOK, putNoBody(r, "/task/"+tmpl.Id.Hex()+"/cancel").Code)

	// The record is kept, so the page still resolves and still shows the
	// schedule and past runs — it just loses every lifecycle control.
	w := getUI(r, "/ui/tasks/"+tmpl.Id.Hex())
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, ">Cancelled<")
	assert.Contains(t, body, "Every 5 minutes")
	assert.Contains(t, body, "Past runs")
	assert.NotContains(t, body, "/pause")
	assert.NotContains(t, body, "/resume")
	assert.NotContains(t, body, "Cancel series")
	assert.NotContains(t, body, `name="cron"`, "no schedule editor on a cancelled series")
}

// --- /ui/series/:id/* lifecycle actions ---

func TestUI_SeriesActions_PauseResumeCancel(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	id := tmpl.Id.Hex()

	// Pause: the response is the re-rendered schedule block, already
	// showing the new status and the paused-at time.
	w := putNoBody(r, "/ui/series/"+id+"/pause")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `id="series-schedule"`)
	assert.Contains(t, w.Body.String(), ">Paused<")
	assert.Contains(t, w.Body.String(), "Paused at")
	stored, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "PAUSED", stored.State)
	assert.Greater(t, stored.PausedTs, int64(0))

	w = putNoBody(r, "/ui/series/"+id+"/resume")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), ">Live<")
	stored, err = s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "RECURRING", stored.State)

	w = putNoBody(r, "/ui/series/"+id+"/cancel")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), ">Cancelled<")
	stored, err = s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", stored.State)
}

// TestUI_SeriesActions_RejectedShowsInlineError covers the reason these
// endpoints exist at all rather than htmx PUTting the JSON API directly:
// a rejected action answers with the same block, carrying the server's
// message, so the user sees why nothing changed.
func TestUI_SeriesActions_RejectedShowsInlineError(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	// Resume on a series that isn't paused.
	w := putNoBody(r, "/ui/series/"+tmpl.Id.Hex()+"/resume")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "inline-error")
	assert.Contains(t, w.Body.String(), "PAUSED")
	assert.Contains(t, w.Body.String(), ">Live<", "state is unchanged")
}

func TestUI_SeriesActions_MissingIdIs404(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	assert.Equal(t, http.StatusNotFound,
		putNoBody(r, "/ui/series/aaaaaaaaaaaaaaaaaaaaaaaa/pause").Code)
	assert.Equal(t, http.StatusBadRequest,
		putNoBody(r, "/ui/series/nonsense/pause").Code)
}

func TestUI_SeriesChangeSchedule(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	id := tmpl.Id.Hex()

	form := url.Values{}
	form.Set("cron", "0 3 * * *")
	w := putForm(r, "/ui/series/"+id+"/schedule", form)
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "0 3 * * *")
	assert.NotContains(t, body, "inline-error")
	assert.NotContains(t, body, "Every 5 minutes")

	stored, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "0 3 * * *", stored.CronExpr)

	// An unparseable expression comes back inline, leaving the stored
	// schedule alone.
	bad := url.Values{}
	bad.Set("cron", "not a cron")
	w = putForm(r, "/ui/series/"+id+"/schedule", bad)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "inline-error")
	assert.Contains(t, w.Body.String(), "invalid cron expression")

	stored, err = s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "0 3 * * *", stored.CronExpr, "a rejected edit must not change the schedule")
}

func TestUI_SeriesSchedulePartial(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	w := getUI(r, "/ui/partials/series-schedule?id="+tmpl.Id.Hex())
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `id="series-schedule"`)
	assert.Contains(t, w.Body.String(), "Every 5 minutes")

	assert.Equal(t, http.StatusNotFound,
		getUI(r, "/ui/partials/series-schedule?id=aaaaaaaaaaaaaaaaaaaaaaaa").Code)
}

// --- schedule preview (temporary; see #97) ---

func TestUI_SchedulePreviewPartial(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	ok := getUI(r, "/ui/partials/schedule-preview?cron=%2A%2F10+%2A+%2A+%2A+%2A")
	assert.Equal(t, http.StatusOK, ok.Code)
	assert.Contains(t, ok.Body.String(), "Every 10 minutes")
	assert.Contains(t, ok.Body.String(), "next:")

	bad := getUI(r, "/ui/partials/schedule-preview?cron=nope")
	assert.Equal(t, http.StatusOK, bad.Code)
	assert.Contains(t, bad.Body.String(), "inline-error")

	empty := getUI(r, "/ui/partials/schedule-preview?cron=")
	assert.Equal(t, http.StatusOK, empty.Code)
	assert.Contains(t, empty.Body.String(), "Enter a cron expression")
}

// --- series card + row backlink on a child task ---

func TestUI_SeriesCard_OnChildTaskDetail(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	childId := seedChildRun(t, s, r, tmpl.Id)

	w := getUI(r, "/ui/tasks/"+childId.Hex())
	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "Part of a scheduled series")
	assert.Contains(t, body, `href="/ui/tasks/`+tmpl.Id.Hex()+`"`)
	assert.Contains(t, body, "echo_task · "+tmpl.Id.Hex())
	assert.Contains(t, body, "Every 5 minutes")
	assert.Contains(t, body, ">Live<")

	// The card tracks the series' current status.
	assert.Equal(t, http.StatusOK, putNoBody(r, "/task/"+tmpl.Id.Hex()+"/pause").Code)
	assert.Contains(t, getUI(r, "/ui/tasks/"+childId.Hex()).Body.String(), ">Paused<")

	assert.Equal(t, http.StatusOK, putNoBody(r, "/task/"+tmpl.Id.Hex()+"/resume").Code)
	assert.Equal(t, http.StatusOK, putNoBody(r, "/task/"+tmpl.Id.Hex()+"/cancel").Code)
	assert.Contains(t, getUI(r, "/ui/tasks/"+childId.Hex()).Body.String(), ">Cancelled<")
}

// A task with no parent gets no card at all.
func TestUI_SeriesCard_AbsentOnPlainTask(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := postTask(r, "echo_task")
	assert.Equal(t, http.StatusCreated, w.Code)
	var plain tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &plain))

	body := getUI(r, "/ui/tasks/"+plain.Id.Hex()).Body.String()
	assert.NotContains(t, body, "Part of a scheduled series")
}

// DELETE /task/:id removes a template's record outright (unlike cancel),
// which leaves its children pointing at an id that no longer resolves. The
// card should say so rather than linking into a 404.
func TestUI_SeriesCard_DeletedTemplate(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	childId := seedChildRun(t, s, r, tmpl.Id)

	req, _ := http.NewRequest("DELETE", "/task/"+tmpl.Id.Hex(), nil)
	del := httptest.NewRecorder()
	r.ServeHTTP(del, req)
	assert.Equal(t, http.StatusOK, del.Code)

	body := getUI(r, "/ui/tasks/"+childId.Hex()).Body.String()
	assert.Contains(t, body, "Part of a scheduled series")
	assert.Contains(t, body, "has since been deleted")
	assert.NotContains(t, body, `href="/ui/tasks/`+tmpl.Id.Hex()+`"`)
}

func TestUI_TasksRows_SeriesBacklink(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	seedChildRun(t, s, r, tmpl.Id)

	rows := getUI(r, "/ui/partials/tasks-rows")
	assert.Equal(t, http.StatusOK, rows.Code)
	body := rows.Body.String()
	assert.Contains(t, body, "part of series "+tmpl.Id.Hex()[:8])
	assert.Contains(t, body, `href="/ui/tasks/`+tmpl.Id.Hex()+`"`)
}
