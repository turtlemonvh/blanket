package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

// claimAnyTask registers a worker matching echo_task's tags and attempts a
// claim, returning the recorder so callers can assert on status code (200
// with a task body, or 204 for an empty/ineligible queue).
func claimAnyTask(t *testing.T, s *ServerConfig, r http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	wconf := worker.WorkerConf{
		Id:      objectid.NewObjectId(),
		Tags:    []string{"bash", "unix"},
		Stopped: false,
	}
	assert.NoError(t, s.DB.UpdateWorker(&wconf))

	req, _ := http.NewRequest("POST", fmt.Sprintf("/task/claim/%s", wconf.Id.Hex()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// --- POST /task/ scheduling fields ---

func TestPostTask_NotBeforeFuture_StartsScheduled(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "1h"}`)
	assert.Equal(t, http.StatusCreated, w.Code)

	var task tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &task))
	assert.Equal(t, "SCHEDULED", task.State)
	assert.Greater(t, task.ScheduledTs, time.Now().Unix())

	// Not eligible for claim yet -- it was never added to the queue.
	claimW := claimAnyTask(t, s, r)
	assert.Equal(t, http.StatusNoContent, claimW.Code)
}

func TestPostTask_NotBeforePast_StartsWaitingImmediately(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "-1h"}`)
	assert.Equal(t, http.StatusCreated, w.Code)

	var task tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &task))
	assert.Equal(t, "WAITING", task.State)

	// Already queued -- claimable right away, same as an unscheduled task.
	claimW := claimAnyTask(t, s, r)
	assert.Equal(t, http.StatusOK, claimW.Code)
}

func TestPostTask_InvalidNotBefore(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "not-a-time"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPostTask_Cron_StartsRecurring(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "cron": "*/5 * * * *"}`)
	assert.Equal(t, http.StatusCreated, w.Code)

	var task tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &task))
	assert.Equal(t, "RECURRING", task.State)
	assert.Equal(t, "*/5 * * * *", task.CronExpr)
	assert.Greater(t, task.NextFireTs, time.Now().Unix())

	// A template never runs itself.
	claimW := claimAnyTask(t, s, r)
	assert.Equal(t, http.StatusNoContent, claimW.Code)
}

func TestPostTask_InvalidCron(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "cron": "not a cron expr"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPostTask_NotBeforeAndCronMutuallyExclusive(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "10m", "cron": "*/5 * * * *"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- cancelling a SCHEDULED task ---

func TestCancelTaskById_Scheduled(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "1h"}`)
	assert.Equal(t, http.StatusCreated, w.Code)
	var task tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &task))
	assert.Equal(t, "SCHEDULED", task.State)

	assert.NoError(t, s.cancelTaskById(context.Background(), task.Id, false))

	updated, err := s.DB.GetTask(task.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", updated.State)

	// A cancelled SCHEDULED task must never be promoted, even once its
	// (now moot) ScheduledTs is in the past.
	s.runSchedulerTick(time.Now().Add(2 * time.Hour))
	updated, err = s.DB.GetTask(task.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", updated.State)
}

// --- promoteDueScheduledTasks ---

func TestPromoteDueScheduledTasks_PromotesDueTask(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()
	r := s.GetRouter()

	now := time.Now()
	w := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "1s"}`)
	assert.Equal(t, http.StatusCreated, w.Code)
	var task tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &task))
	assert.Equal(t, "SCHEDULED", task.State)

	// Not due yet.
	s.promoteDueScheduledTasks(now)
	stillScheduled, err := s.DB.GetTask(task.Id)
	assert.NoError(t, err)
	assert.Equal(t, "SCHEDULED", stillScheduled.State)
	assert.Equal(t, http.StatusNoContent, claimAnyTask(t, s, r).Code)

	// Fast-forward past ScheduledTs.
	s.promoteDueScheduledTasks(now.Add(2 * time.Second))
	promoted, err := s.DB.GetTask(task.Id)
	assert.NoError(t, err)
	assert.Equal(t, "WAITING", promoted.State)

	claimW := claimAnyTask(t, s, r)
	assert.Equal(t, http.StatusOK, claimW.Code)
	var claimed tasks.Task
	assert.NoError(t, json.Unmarshal(claimW.Body.Bytes(), &claimed))
	assert.Equal(t, task.Id, claimed.Id)
}

func TestPromoteDueScheduledTasks_IdempotentAcrossTicks(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "notBefore": "10m"}`)
	assert.Equal(t, http.StatusCreated, w.Code)
	var task tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &task))
	assert.Equal(t, "SCHEDULED", task.State)

	// Advance well past ScheduledTs and run the tick three times, as if
	// three ticks landed back-to-back or a crash caused a re-run.
	due := time.Now().Add(time.Hour)
	s.promoteDueScheduledTasks(due)
	s.promoteDueScheduledTasks(due)
	s.promoteDueScheduledTasks(due)

	// Running the tick multiple times must not duplicate the queue entry
	// or error; exactly one claim should succeed and a second should find
	// an empty queue.
	first := claimAnyTask(t, s, r)
	assert.Equal(t, http.StatusOK, first.Code)
	second := claimAnyTask(t, s, r)
	assert.Equal(t, http.StatusNoContent, second.Code)
}

// --- fireDueRecurringTasks ---

func TestFireDueRecurringTasks_SpawnsChildAndAdvancesNextFire(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "cron": "*/5 * * * *"}`)
	assert.Equal(t, http.StatusCreated, w.Code)
	var tmpl tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &tmpl))
	assert.Equal(t, "RECURRING", tmpl.State)
	firstNextFire := tmpl.NextFireTs

	// Not due yet.
	s.fireDueRecurringTasks(time.Now())
	stillTemplate, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "RECURRING", stillTemplate.State)
	assert.Equal(t, firstNextFire, stillTemplate.NextFireTs)

	// Fast-forward past the first fire time.
	fireTime := time.Unix(firstNextFire, 0).Add(time.Second)
	s.fireDueRecurringTasks(fireTime)

	// Template persists as RECURRING (it never runs itself) with an
	// advanced NextFireTs.
	advanced, err := s.DB.GetTask(tmpl.Id)
	assert.NoError(t, err)
	assert.Equal(t, "RECURRING", advanced.State)
	assert.Greater(t, advanced.NextFireTs, firstNextFire)

	// Exactly one child was spawned, linked to the template, WAITING, and
	// claimable.
	searchConf := testAllStatesSearchConf()
	found, n, err := s.DB.GetTasks(&searchConf)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, n, 2) // template + child
	var child *tasks.Task
	for i := range found {
		if found[i].Id != tmpl.Id {
			child = &found[i]
		}
	}
	if assert.NotNil(t, child, "expected a spawned child task") {
		assert.Equal(t, tmpl.Id, child.ParentId)
		assert.Equal(t, "WAITING", child.State)
		assert.Equal(t, tmpl.TypeId, child.TypeId)
		assert.NotEqual(t, tmpl.Id, child.Id)
	}

	claimW := claimAnyTask(t, s, r)
	assert.Equal(t, http.StatusOK, claimW.Code)
	var claimed tasks.Task
	assert.NoError(t, json.Unmarshal(claimW.Body.Bytes(), &claimed))
	assert.Equal(t, tmpl.Id, claimed.ParentId)

	// Firing again before the (now advanced) NextFireTs is a no-op.
	s.fireDueRecurringTasks(fireTime)
	afterNoopConf := testAllStatesSearchConf()
	afterNoop, _, err := s.DB.GetTasks(&afterNoopConf)
	assert.NoError(t, err)
	assert.Equal(t, len(found), len(afterNoop))
}

// TestRecurringTask_StoppedByDelete confirms the documented way to stop a
// recurring template -- DELETE /task/:id -- actually prevents further
// fires (rather than e.g. leaving an orphaned template the scheduler keeps
// reviving).
func TestRecurringTask_StoppedByDelete(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()
	r := s.GetRouter()

	w := postJSON(r, "/task/", `{"type": "echo_task", "cron": "*/5 * * * *"}`)
	assert.Equal(t, http.StatusCreated, w.Code)
	var tmpl tasks.Task
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &tmpl))

	delReq, _ := http.NewRequest("DELETE", fmt.Sprintf("/task/%s", tmpl.Id.Hex()), nil)
	delW := httptest.NewRecorder()
	r.ServeHTTP(delW, delReq)
	assert.Equal(t, http.StatusOK, delW.Code)

	_, err := s.DB.GetTask(tmpl.Id)
	assert.Error(t, err)

	// Scheduler tick well past the original fire time finds nothing to do.
	s.fireDueRecurringTasks(time.Unix(tmpl.NextFireTs, 0).Add(time.Hour))
	claimW := claimAnyTask(t, s, r)
	assert.Equal(t, http.StatusNoContent, claimW.Code)
}

// --- startBackgroundLoops ---

func TestStartBackgroundLoops_StopsCleanly(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	s.SchedulerInterval = time.Millisecond

	stop := s.startBackgroundLoops(context.Background())

	// Let at least one tick happen, then stop and confirm it returns
	// promptly instead of hanging.
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop() did not return promptly")
	}
}

func testAllStatesSearchConf() database.TaskSearchConf {
	smallest, largest := fullIdRange()
	return database.TaskSearchConf{
		Limit:      schedulerScanLimit,
		SmallestId: smallest,
		LargestId:  largest,
	}
}
