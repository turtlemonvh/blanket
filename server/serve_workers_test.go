package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/worker"
)

func TestLaunchWorkerAndWait_RejectsLowCheckInterval(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := &worker.WorkerConf{Tags: []string{"exec:bash"}, CheckInterval: 0.1}
	_, err := s.launchWorkerAndWait(context.Background(), w)
	assert.ErrorIs(t, err, worker.ErrCheckIntervalTooLow)
}

func TestStopWorkerById(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	err := s.stopWorkerById(context.Background(), w.Id, false)
	assert.NoError(t, err)

	updated, err := s.DB.GetWorker(w.Id)
	assert.NoError(t, err)
	assert.True(t, updated.Stopped)
}

// TestStopWorkerById_UpdatesLastHeardTs is the regression test for the
// "Update lastHeardTs too" FIXME: stopping a worker should bump
// LastHeardTs, not just flip Stopped.
func TestStopWorkerById_UpdatesLastHeardTs(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}}
	assert.NoError(t, s.DB.UpdateWorker(&w))
	assert.Zero(t, w.LastHeardTs)

	assert.NoError(t, s.stopWorkerById(context.Background(), w.Id, false))

	updated, err := s.DB.GetWorker(w.Id)
	assert.NoError(t, err)
	assert.NotZero(t, updated.LastHeardTs)
}

// TestStopWorkerById_ForceWithNoPid covers the force path with no live
// process to signal (Pid == 0, the zero-value state for a freshly-created
// worker record) — it must not error just because there's nothing to kill.
func TestStopWorkerById_ForceWithNoPid(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	err := s.stopWorkerById(context.Background(), w.Id, true)
	assert.NoError(t, err)

	updated, err := s.DB.GetWorker(w.Id)
	assert.NoError(t, err)
	assert.True(t, updated.Stopped)
}

// TestStopWorkerById_ForceSignalFailureDoesNotFailCall covers a force stop
// against a pid that can't actually be signaled — an implausibly large pid
// with no live process behind it, so the signal delivery fails with "no
// such process". (Deliberately not pid 1: under a root/container test
// runner that can be a real, live process worth not touching.) The DB
// update is the primary contract; a failed signal delivery should be
// logged, not surfaced as a call failure.
func TestStopWorkerById_ForceSignalFailureDoesNotFailCall(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}, Pid: 2147483647}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	err := s.stopWorkerById(context.Background(), w.Id, true)
	assert.NoError(t, err)

	updated, err := s.DB.GetWorker(w.Id)
	assert.NoError(t, err)
	assert.True(t, updated.Stopped)
}

func TestDeleteWorkerById(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}, Stopped: true}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	err := s.deleteWorkerById(context.Background(), w.Id)
	assert.NoError(t, err)

	_, err = s.DB.GetWorker(w.Id)
	assert.Error(t, err)
}

// TestDeleteWorker_RejectsUnstoppedWorker is the regression test for the
// missing-return bug in the deleteWorker handler: a failed "is this worker
// stopped" check logged a 400 but fell through and deleted the worker
// anyway. It should now leave the worker record in place.
func TestDeleteWorker_RejectsUnstoppedWorker(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}, Stopped: false}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	body, err := json.Marshal(&w)
	assert.NoError(t, err)

	req, _ := http.NewRequest("DELETE", "/worker/"+w.Id.Hex(), bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// The worker record must still be present — the pre-fix handler
	// deleted it here despite the 400 above.
	_, err = s.DB.GetWorker(w.Id)
	assert.NoError(t, err)
}

// TestDeleteWorker_AllowsStoppedWorker is the companion happy-path case:
// a stopped worker is still deletable through the full HTTP handler.
func TestDeleteWorker_AllowsStoppedWorker(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}, Stopped: true}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	body, err := json.Marshal(&w)
	assert.NoError(t, err)

	req, _ := http.NewRequest("DELETE", "/worker/"+w.Id.Hex(), bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	_, err = s.DB.GetWorker(w.Id)
	assert.Error(t, err)
}
