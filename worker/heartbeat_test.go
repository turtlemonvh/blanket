package worker_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/worker"
)

// Worker-side heartbeat (turtlemonvh/blanket#23 phase 3).
//
// Two things matter from this side: the call goes through lib/httpx, so it
// inherits phase 1's timeouts and full-jitter retry rather than hanging a
// worker on a wedged server; and a `stopped` response ends the worker
// within one check interval, which is what makes a drain fast enough to be
// usable during an upgrade.

func TestHeartbeat_ReportsServerState(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	hb, err := h.work.Heartbeat()
	require.NoError(t, err)
	assert.False(t, hb.Stopped)
	assert.NotEmpty(t, hb.ServerInstanceId)
	assert.NotZero(t, hb.LastHeardTs, "the server stamps this from its own clock")

	h.stopWorkerViaAPI()

	hb, err = h.work.Heartbeat()
	require.NoError(t, err)
	assert.True(t, hb.Stopped, "a stop must be visible on the next heartbeat")
}

// TestHeartbeat_RetriesThroughOutage: the heartbeat is an httpx call like
// every other worker->server call, so a dead connection is retried rather
// than surfacing immediately. Pre-phase-1 there was no shared client at
// all, and a call like this against a wedged server would have hung
// forever with no timeout.
func TestHeartbeat_RetriesThroughOutage(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.outage.inject("/heartbeat", outageHijack, 2)
	hb, err := h.work.Heartbeat()
	require.NoError(t, err)
	assert.Equal(t, 2, h.outage.brokenCount(), "both broken attempts should have been retried")
	assert.NotEmpty(t, hb.ServerInstanceId)
}

// TestHeartbeat_GivesUpAtItsDeadline: a heartbeat that can never land must
// return, and quickly. Its budget is deliberately the shortest of the
// worker's retry budgets -- a late heartbeat is worth less than the next
// one, and the claim loop must not be held up waiting for it.
func TestHeartbeat_GivesUpAtItsDeadline(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	restore := worker.HeartbeatRetryDeadline
	worker.HeartbeatRetryDeadline = 300 * time.Millisecond
	defer func() { worker.HeartbeatRetryDeadline = restore }()

	h.outage.inject("/heartbeat", outageStatus, -1)
	started := time.Now()
	_, err := h.work.Heartbeat()
	elapsed := time.Since(started)

	assert.Error(t, err)
	assert.Less(t, elapsed, 10*time.Second, "should have given up at its deadline")
}

// TestProcessTasks_ExitsOnStoppedHeartbeat is the drain path: the worker
// learns it has been stopped and ends the loop. The check interval here is
// the minimum, so "within one interval" is a real bound rather than a
// generous one.
func TestProcessTasks_ExitsOnStoppedHeartbeat(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.work.CheckInterval = worker.MIN_CHECK_INTERVAL_SECONDS

	done := make(chan error, 1)
	go func() { done <- h.work.ProcessTasks() }()

	// Let the loop get going, then stop the worker server-side.
	time.Sleep(time.Second)
	h.stopWorkerViaAPI()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("ProcessTasks did not exit after the worker was stopped")
	}
}

// TestHeartbeat_UnknownWorkerIsNotRetried: a heartbeat for a deleted worker
// is a 404, which httpx classifies as non-retryable. Retrying it would burn
// the budget on a worker that is never coming back into the database.
func TestHeartbeat_UnknownWorkerIsNotRetried(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	stray := h.work
	stray.Id = objectid.NewObjectId()

	started := time.Now()
	_, err := stray.Heartbeat()
	elapsed := time.Since(started)

	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, httpx.StatusCodeOf(err))
	assert.Less(t, elapsed, 3*time.Second, "a 404 must not be retried")
}
