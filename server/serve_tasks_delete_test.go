package server

// DELETE /task/:id's still-running guard (turtlemonvh/blanket#252):
// a task that may have a live process on a worker is a 400 and survives;
// everything else is deletable. See database.CheckTaskDeletable.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
)

func deleteTaskReq(r http.Handler, id objectid.ObjectId) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("DELETE", fmt.Sprintf("/task/%s", id.Hex()), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// newRunningTask creates an echo_task and walks it to RUNNING through the
// worker-facing handlers.
func newRunningTask(t *testing.T) (*ServerConfig, *gin.Engine, objectid.ObjectId, func()) {
	t.Helper()
	cleanupType := setupTestTaskType(t)
	s, scleanup := NewTestServer()
	r := s.GetRouter()

	created := postTask(r, "echo_task")
	require.Equal(t, http.StatusCreated, created.Code)
	var createdTask tasks.Task
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &createdTask))
	putTaskInRunningState(t, s, r, createdTask)

	return s, r, createdTask.Id, func() {
		scleanup()
		cleanupType()
	}
}

func TestDeleteTask_RunningIs400AndSurvives(t *testing.T) {
	s, r, id, cleanup := newRunningTask(t)
	defer cleanup()

	w := deleteTaskReq(r, id)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.JSONEq(t, `{"error": "Cannot delete a task that is still running; cancel it first"}`, w.Body.String())

	got, err := s.DB.GetTask(id)
	require.NoError(t, err)
	assert.Equal(t, "RUNNING", got.State)
}

func TestDeleteTask_ClaimedIs400(t *testing.T) {
	s, r, id, cleanup := newClaimedTask(t)
	defer cleanup()

	assert.Equal(t, http.StatusBadRequest, deleteTaskReq(r, id).Code)

	got, err := s.DB.GetTask(id)
	require.NoError(t, err)
	assert.Equal(t, "CLAIMED", got.State)
}

// A run the worker finished, in any terminal state, deletes normally.
func TestDeleteTask_FinishedIs200(t *testing.T) {
	for _, state := range tasks.ValidTerminalTaskStates {
		t.Run(state, func(t *testing.T) {
			s, r, id, cleanup := newRunningTask(t)
			defer cleanup()

			require.Equal(t, http.StatusOK, putTransition(r, id, "finish", "state="+state))

			assert.Equal(t, http.StatusOK, deleteTaskReq(r, id).Code)
			_, err := s.DB.GetTask(id)
			assert.Error(t, err)
		})
	}
}

// The non-terminal states with no process behind them are deletable:
// WAITING (queued; a later claim finds no record and drops the queue
// entry), SCHEDULED, and the RECURRING/PAUSED templates.
func TestDeleteTask_DeletableNonTerminalStates(t *testing.T) {
	for _, state := range []string{"WAITING", "SCHEDULED", "RECURRING", "PAUSED"} {
		t.Run(state, func(t *testing.T) {
			cleanupType := setupTestTaskType(t)
			defer cleanupType()
			s, scleanup := NewTestServer()
			defer scleanup()
			r := s.GetRouter()

			tsk, err := s.createTask(context.Background(), "echo_task", nil)
			require.NoError(t, err)
			tsk.State = state
			require.NoError(t, s.DB.SaveTask(&tsk))

			assert.Equal(t, http.StatusOK, deleteTaskReq(r, tsk.Id).Code)
			_, err = s.DB.GetTask(tsk.Id)
			assert.Error(t, err)
		})
	}
}

// A force cancel flips a RUNNING task to STOPPED at once, but its process
// lives until the worker sees that. The delete waits for the worker's
// finish report, which is what clears StopPendingTs; deleting earlier
// would leave the worker reading a 404 and the process orphaned.
func TestDeleteTask_ForceCancelledWaitsForWorker(t *testing.T) {
	s, r, id, cleanup := newRunningTask(t)
	defer cleanup()

	require.Equal(t, http.StatusOK, putTransition(r, id, "cancel", "force=true"))
	got, err := s.DB.GetTask(id)
	require.NoError(t, err)
	assert.Equal(t, "STOPPED", got.State)
	assert.NotZero(t, got.StopPendingTs)

	w := deleteTaskReq(r, id)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "confirms it has stopped")

	// The worker's report after the kill: ERROR from cmd.Wait(). The state
	// stays STOPPED (first terminal state wins), the stamp clears.
	require.Equal(t, http.StatusOK, putTransition(r, id, "finish", "state=ERROR"))
	got, err = s.DB.GetTask(id)
	require.NoError(t, err)
	assert.Equal(t, "STOPPED", got.State)
	assert.Zero(t, got.StopPendingTs)

	assert.Equal(t, http.StatusOK, deleteTaskReq(r, id).Code)
}

// A worker that never reports (it died) must not make the task
// undeletable: past StopAckGrace the delete goes through.
func TestDeleteTask_StopPendingExpires(t *testing.T) {
	s, r, id, cleanup := newRunningTask(t)
	defer cleanup()

	require.Equal(t, http.StatusOK, putTransition(r, id, "cancel", "force=true"))

	old := database.StopAckGrace
	database.StopAckGrace = 0
	defer func() { database.StopAckGrace = old }()

	assert.Equal(t, http.StatusOK, deleteTaskReq(r, id).Code)
	_, err := s.DB.GetTask(id)
	assert.Error(t, err)
}

// Cancelling a task that never ran (WAITING) leaves nothing pending.
func TestDeleteTask_CancelledWaitingIs200(t *testing.T) {
	cleanupType := setupTestTaskType(t)
	defer cleanupType()
	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, putTransition(r, tsk.Id, "cancel", ""))

	got, err := s.DB.GetTask(tsk.Id)
	require.NoError(t, err)
	assert.Zero(t, got.StopPendingTs)
	assert.Equal(t, http.StatusOK, deleteTaskReq(r, tsk.Id).Code)
}

// MCP blanket_cancel_task(force, delete) on a RUNNING task: the cancel
// lands at once, and the delete waits until the worker reports the kill.
func TestMcpCancelTask_DeleteRunningTask(t *testing.T) {
	s, r, id, cleanup := newRunningTask(t)
	defer cleanup()

	// Stand in for the worker: once it sees STOPPED, report the process
	// gone, as ProcessOne does after cmd.Wait() returns.
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for i := 0; i < 200; i++ {
			if got, err := s.DB.GetTask(id); err == nil && got.State == "STOPPED" {
				time.Sleep(300 * time.Millisecond)
				putTransition(r, id, "finish", "state=ERROR")
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	res, _, err := s.mcpCancelTask(context.Background(), nil, blanketCancelTaskArgs{Id: id.Hex(), Force: true, Delete: true})
	<-workerDone
	require.NoError(t, err)
	assert.NotNil(t, res)

	_, err = s.DB.GetTask(id)
	var nf database.ItemNotFoundError
	assert.ErrorAs(t, err, &nf)
}

// With no worker to confirm, the MCP delete gives up after its wait and
// says so; the task stays, cancelled.
func TestMcpCancelTask_DeleteRunningTaskNoWorker(t *testing.T) {
	s, _, id, cleanup := newRunningTask(t)
	defer cleanup()

	old := mcpDeleteWait
	mcpDeleteWait = 300 * time.Millisecond
	defer func() { mcpDeleteWait = old }()

	_, _, err := s.mcpCancelTask(context.Background(), nil, blanketCancelTaskArgs{Id: id.Hex(), Force: true, Delete: true})
	assert.ErrorIs(t, err, database.ErrTaskStopPending)
	assert.Contains(t, err.Error(), "cancelled but not deleted")

	got, err := s.DB.GetTask(id)
	require.NoError(t, err)
	assert.Equal(t, "STOPPED", got.State)
}

// The task list offers Delete only on a finished task, so the UI never
// sends a DELETE the still-running guard would refuse (#252).
func TestUI_TasksRows_DeleteOnlyOnFinished(t *testing.T) {
	s, r, running, cleanup := newRunningTask(t)
	defer cleanup()

	done, err := s.createTask(context.Background(), "echo_task", nil)
	require.NoError(t, err)
	require.NoError(t, s.DB.FinishTask(done.Id, database.FinishState("SUCCESS")))

	w := getUI(r, "/ui/partials/tasks-rows")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `hx-delete="/task/`+done.Id.Hex()+`"`)
	assert.NotContains(t, body, `hx-delete="/task/`+running.Hex()+`"`)
}

// Without force, MCP refuses to cancel a RUNNING task, and so never
// reaches the delete.
func TestMcpCancelTask_DeleteRunningWithoutForce(t *testing.T) {
	s, _, id, cleanup := newRunningTask(t)
	defer cleanup()

	_, _, err := s.mcpCancelTask(context.Background(), nil, blanketCancelTaskArgs{Id: id.Hex(), Delete: true})
	assert.ErrorIs(t, err, ErrRunningTaskRequiresForce)

	got, err := s.DB.GetTask(id)
	require.NoError(t, err)
	assert.Equal(t, "RUNNING", got.State)
}
