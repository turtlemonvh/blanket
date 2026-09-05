package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/tasks"
)

// createRecurringTemplate submits a RECURRING template (cron "*/5 * * * *")
// and returns the decoded task record.
func createRecurringTemplate(t *testing.T, r http.Handler) tasks.Task {
	t.Helper()
	w := postJSON(r, "/task/", `{"type": "echo_task", "cron": "*/5 * * * *"}`)
	assert.Equal(t, http.StatusCreated, w.Code)
	var tmpl tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &tmpl))
	assert.Equal(t, "RECURRING", tmpl.State)
	return tmpl
}

// --- PUT /task/:id/pause ---

func TestPauseTask_Recurring(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)

	w := putNoBody(r, "/task/"+tmpl.Id.Hex()+"/pause")
	assert.Equal(t, http.StatusOK, w.Code)

	updated, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "PAUSED", updated.State)
	assert.Greater(t, updated.PausedTs, int64(0))
}

func TestPauseTask_WrongState(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	created := postTask(r, "echo_task")
	var createdTask tasks.Task
	assert.NoError(t, json.Unmarshal(created.Body.Bytes(), &createdTask))

	w := putNoBody(r, "/task/"+createdTask.Id.Hex()+"/pause")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "RECURRING")
}

func TestPauseTask_MissingTask(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := putNoBody(r, "/task/aaaaaaaaaaaaaaaaaaaaaaaa/pause")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- PUT /task/:id/resume ---

func TestResumeTask_Paused(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	assert.Equal(t, http.StatusOK, putNoBody(r, "/task/"+tmpl.Id.Hex()+"/pause").Code)

	paused, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "PAUSED", paused.State)

	w := putNoBody(r, "/task/"+tmpl.Id.Hex()+"/resume")
	assert.Equal(t, http.StatusOK, w.Code)

	resumed, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "RECURRING", resumed.State)
	assert.Equal(t, int64(0), resumed.PausedTs)
	// Recomputed from "now", so it should be >= the moment just before we
	// paused -- specifically, not stuck at whatever NextFireTs was
	// computed at original submission time (which is already in the
	// future too, so just assert it's still in the future and non-zero).
	assert.Greater(t, resumed.NextFireTs, time.Now().Unix()-1)
}

func TestResumeTask_WrongState(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)

	w := putNoBody(r, "/task/"+tmpl.Id.Hex()+"/resume")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "PAUSED")
}

func TestResumeTask_MissingTask(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := putNoBody(r, "/task/aaaaaaaaaaaaaaaaaaaaaaaa/resume")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- PUT /task/:id/schedule ---

func TestChangeSchedule_CronOnRecurring(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	firstNextFire := tmpl.NextFireTs

	// "0 0 1 1 *" (once a year, Jan 1 00:00) can't coincidentally land on
	// the same next-fire instant as "*/5 * * * *" the way two nearby
	// short-period expressions occasionally can.
	w := putJSON(r, "/task/"+tmpl.Id.Hex()+"/schedule", `{"cron": "0 0 1 1 *"}`)
	assert.Equal(t, http.StatusOK, w.Code)

	updated, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "RECURRING", updated.State)
	assert.Equal(t, "0 0 1 1 *", updated.CronExpr)
	assert.NotEqual(t, firstNextFire, updated.NextFireTs)
}

func TestChangeSchedule_CronOnPaused_DoesNotTouchNextFire(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	assert.Equal(t, http.StatusOK, putNoBody(r, "/task/"+tmpl.Id.Hex()+"/pause").Code)
	paused, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)

	w := putJSON(r, "/task/"+tmpl.Id.Hex()+"/schedule", `{"cron": "0 * * * *"}`)
	assert.Equal(t, http.StatusOK, w.Code)

	updated, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "PAUSED", updated.State)
	assert.Equal(t, "0 * * * *", updated.CronExpr)
	// NextFireTs is only meaningful once RECURRING again; resume
	// recomputes it. Changing schedule while paused should leave it as-is
	// rather than computing a (soon stale) value.
	assert.Equal(t, paused.NextFireTs, updated.NextFireTs)
}

func TestChangeSchedule_NotBeforeOnScheduled(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "1h"}`)
	assert.Equal(t, http.StatusCreated, w.Code)
	var task tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &task))

	changeW := putJSON(r, "/task/"+task.Id.Hex()+"/schedule", `{"notBefore": "2h"}`)
	assert.Equal(t, http.StatusOK, changeW.Code)

	updated, err := s.DB.GetTask(task.Id)
	assert.NoError(t, err)
	assert.Equal(t, "SCHEDULED", updated.State)
	assert.Greater(t, updated.ScheduledTs, task.ScheduledTs)
}

func TestChangeSchedule_NotBeforeMustBeFuture(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "1h"}`)
	var task tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &task))

	changeW := putJSON(r, "/task/"+task.Id.Hex()+"/schedule", `{"notBefore": "-1h"}`)
	assert.Equal(t, http.StatusBadRequest, changeW.Code)
}

func TestChangeSchedule_WrongStateForCron(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	created := postTask(r, "echo_task")
	var createdTask tasks.Task
	assert.NoError(t, json.Unmarshal(created.Body.Bytes(), &createdTask))

	w := putJSON(r, "/task/"+createdTask.Id.Hex()+"/schedule", `{"cron": "0 * * * *"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestChangeSchedule_WrongStateForNotBefore(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)

	w := putJSON(r, "/task/"+tmpl.Id.Hex()+"/schedule", `{"notBefore": "1h"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestChangeSchedule_InvalidCron(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)

	w := putJSON(r, "/task/"+tmpl.Id.Hex()+"/schedule", `{"cron": "not a cron expr"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid cron expression")
}

func TestChangeSchedule_BothFieldsRejected(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)

	w := putJSON(r, "/task/"+tmpl.Id.Hex()+"/schedule", `{"cron": "0 * * * *", "notBefore": "1h"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestChangeSchedule_NeitherFieldRejected(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)

	w := putJSON(r, "/task/"+tmpl.Id.Hex()+"/schedule", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestChangeSchedule_MissingTask(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := putJSON(r, "/task/aaaaaaaaaaaaaaaaaaaaaaaa/schedule", `{"cron": "0 * * * *"}`)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- PUT /task/:id/cancel on a template ---

func TestCancelTask_RecurringTemplate(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)

	w := putNoBody(r, "/task/"+tmpl.Id.Hex()+"/cancel")
	assert.Equal(t, http.StatusOK, w.Code)

	updated, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", updated.State)

	// A cancelled template must never fire again, even long past its
	// original NextFireTs.
	s.fireDueRecurringTasks(time.Unix(tmpl.NextFireTs, 0).Add(24 * time.Hour))
	stillStopped, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", stillStopped.State)
}

func TestCancelTask_PausedTemplate(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)
	assert.Equal(t, http.StatusOK, putNoBody(r, "/task/"+tmpl.Id.Hex()+"/pause").Code)

	w := putNoBody(r, "/task/"+tmpl.Id.Hex()+"/cancel")
	assert.Equal(t, http.StatusOK, w.Code)

	updated, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", updated.State)
}

// --- GET /schedule/describe ---

func TestDescribeSchedule_Valid(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	req, _ := http.NewRequest("GET", "/schedule/describe?cron="+"*%2F5+*+*+*+*", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Cron        string   `json:"cron"`
		Description string   `json:"description"`
		Next        []string `json:"next"`
	}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "*/5 * * * *", body.Cron)
	assert.NotEmpty(t, body.Description)
	assert.Len(t, body.Next, 3)
	for _, n := range body.Next {
		_, err := time.Parse(time.RFC3339, n)
		assert.NoError(t, err)
	}
}

func TestDescribeSchedule_InvalidCron(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	req, _ := http.NewRequest("GET", "/schedule/describe?cron=not-a-cron-expr", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDescribeSchedule_MissingParam(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	req, _ := http.NewRequest("GET", "/schedule/describe", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- scheduled/recurring/paused capacity limit (429) ---

func TestPostTask_ScheduledCapacityLimit(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	s.SchedulerMaxScheduled = 2
	r := s.GetRouter()

	// scheduler.maxScheduled=2: the first SCHEDULED submission is accepted
	// (live count 0 -> 1; 0+1 < 2). The second is rejected -- accepting it
	// would bring the live count to 2, reaching the limit (1+1 >= 2).
	w1 := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "1h"}`)
	assert.Equal(t, http.StatusCreated, w1.Code)

	w2 := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "1h"}`)
	assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	assert.Contains(t, w2.Body.String(), "scheduler.maxScheduled")

	// A plain (immediately WAITING) submission is unaffected by the limit.
	w3 := postTask(r, "echo_task")
	assert.Equal(t, http.StatusCreated, w3.Code)
}

// --- GET /task/?parentId=<id> ---

func TestTaskList_FilterByParentId(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tmpl := createRecurringTemplate(t, r)

	// Fire the template twice to spawn two children.
	fireTime := time.Unix(tmpl.NextFireTs, 0).Add(time.Second)
	s.fireDueRecurringTasks(fireTime)
	updated, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	fireTime2 := time.Unix(updated.NextFireTs, 0).Add(time.Second)
	s.fireDueRecurringTasks(fireTime2)

	// A plain, unrelated task should not show up in the filtered list.
	assert.Equal(t, http.StatusCreated, postTask(r, "echo_task").Code)

	req, _ := http.NewRequest("GET", "/task/?parentId="+tmpl.Id.Hex(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	var children []tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &children))
	assert.Len(t, children, 2)
	for _, c := range children {
		assert.Equal(t, tmpl.Id, c.ParentId)
	}
}
