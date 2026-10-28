package server

// Regression tests for turtlemonvh/blanket#278: replays of the
// counterexamples formal/restart/BlanketRestart.tla found on the code as it
// was, one model step per HTTP call or boot step, asserting only what the
// worker record then says.
//
// "The drained worker is still running" is this test process: its own pid
// and start time are the one pair proclive.IsAlive reports as conclusively
// alive on any box. "It has exited" is addWorker's pid 4242 with an
// impossible start time, which is conclusively dead.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/timing"
	"github.com/turtlemonvh/blanket/worker"
)

func addLiveWorker(t *testing.T, s *ServerConfig) worker.WorkerConf {
	t.Helper()
	pid, started := selfPidAndStart(t)
	w := worker.WorkerConf{Id: objectid.NewObjectId(), Pid: pid, PidStartTs: started, StartedTs: time.Now().Unix()}
	require.NoError(t, s.DB.UpdateWorker(&w))
	return w
}

// workerExits re-registers w with a pid that is conclusively dead: the
// drained process has finished its task, seen it is stopped and gone.
func workerExits(t *testing.T, s *ServerConfig, id objectid.ObjectId) {
	t.Helper()
	gone := worker.WorkerConf{Id: id, Pid: 4242, PidStartTs: 1}
	require.NoError(t, s.DB.UpdateWorker(&gone))
}

func countingSpawns(s *ServerConfig) *int {
	n := 0
	s.spawnWorkerFn = func(w *worker.WorkerConf) (worker.WorkerConf, error) { n++; return *w, nil }
	return &n
}

// Model trace NoLostWorker, the upgrade path: the drain times out with a
// straggler (drained:false), blanket upgrade goes on to exec, and the next
// process boots while the straggler is still finishing its task. The boot
// pass used to clear the straggler's intent ("already running") and leave
// it stopped, so it exited after its task and never came back.
func TestRespawn_BootKeepsTheIntentOfADrainStraggler(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()
	spawns := countingSpawns(s)
	s.DrainTimeout = 300 * time.Millisecond

	w := addLiveWorker(t, s)

	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/begin", "")))
	code, body := doOps(t, r, "POST", "/ops/restart/drain", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, body["drained"], "the straggler is reported, and the CLI goes on")
	// No listener in a unit test, so exec answers 503 -- after it has
	// committed EXECING, which is all the next boot reads.
	doOps(t, r, "POST", "/ops/restart/exec", "")

	// What Serve does in the next process.
	s.adoptRestartRecordOnBoot()
	s.respawnWorkers("boot")

	got, err := s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.Equal(t, 0, *spawns, "the straggler is still running; nothing is forked for it yet")
	assert.True(t, got.Stopped && got.RespawnIntent,
		"the straggler stays drained and keeps its intent, so it is brought back once it exits")

	// It finishes its task and exits; the next periodic pass brings it back.
	workerExits(t, s, w.Id)
	s.respawnWorkers("periodic")

	got, err = s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.Equal(t, 1, *spawns)
	assert.False(t, got.Stopped)
	assert.False(t, got.RespawnIntent)
}

// Model trace NoLostWorker, the abort path: Begin, Drain, Abort while the
// drained worker has not exited yet. Abort used to clear its intent and
// leave it stopped; it now undoes the drain, so the worker carries on.
func TestRespawn_AbortUndrainsAWorkerThatIsStillRunning(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()
	spawns := countingSpawns(s)

	w := addLiveWorker(t, s)

	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/begin", "")))
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/drain?wait=false", "")))
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/abort", "")))

	got, err := s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.Equal(t, 0, *spawns)
	assert.False(t, got.Stopped, "the abort undid the drain; the worker's next poll carries on")
	assert.True(t, got.RespawnIntent,
		"the intent stays until a pass sees the worker running, in case it had already seen the stop and is exiting")

	// It carries on: the next pass sees it running and not stopped.
	s.respawnWorkers("periodic")
	got, err = s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.Equal(t, 0, *spawns)
	assert.False(t, got.RespawnIntent)
}

// The other half of the abort case: the worker had already seen the stop
// and exited by the time the abort's un-drain landed.
func TestRespawn_AnUndrainedWorkerThatExitedAnywayIsBroughtBack(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()
	spawns := countingSpawns(s)

	w := addLiveWorker(t, s)
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/begin", "")))
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/drain?wait=false", "")))
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/abort", "")))

	workerExits(t, s, w.Id)
	s.respawnWorkers("periodic")

	got, err := s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.Equal(t, 1, *spawns)
	assert.False(t, got.RespawnIntent)
}

// Model trace DebtPaid: a respawn skipped by the minimum interval was
// retried only at the next boot or abort. The periodic loop pays it while
// the server is up.
func TestRespawn_PeriodicLoopPaysASkippedRespawn(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	spawns := countingSpawns(s)

	w := addWorker(t, s, false)
	_, _, err := s.DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		return nil
	})
	require.NoError(t, err)
	s.adoptRestartRecordOnBoot()

	s.RespawnRetryInterval = 20 * time.Millisecond
	stop := s.startBackgroundLoops(t.Context())
	defer stop()

	require.Eventually(t, func() bool {
		got, err := s.DB.GetWorker(w.Id)
		return err == nil && !got.RespawnIntent
	}, 5*time.Second, 10*time.Millisecond, "the periodic pass respawns an owed worker without a restart")
	assert.Equal(t, 1, *spawns)
}

// No respawn while a restart is in flight: the drain owns the intents.
func TestRespawn_NothingIsForkedWhileARestartIsInFlight(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()
	spawns := countingSpawns(s)

	w := addWorker(t, s, false)
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/begin", "")))
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/drain?wait=false", "")))

	s.respawnWorkers("periodic")
	assert.Equal(t, 0, *spawns)

	_, err := s.DB.ClaimWorkerRespawn(w.Id)
	assert.ErrorIs(t, err, database.ErrRespawnDeferred, "the claim transaction re-checks it too")
}

// A process forked for a worker but not yet registered is neither running
// nor gone: a pass must not fork a second one.
func TestRespawn_AWorkerStillRegisteringIsNotForkedAgain(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	spawns := countingSpawns(s)

	w := addWorker(t, s, false)
	_, _, err := s.DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		return nil
	})
	require.NoError(t, err)
	s.adoptRestartRecordOnBoot()

	// The previous process claimed and forked, recorded the fork, and died
	// before the child registered.
	_, err = s.DB.ClaimWorkerRespawn(w.Id)
	require.NoError(t, err)
	pid, started := selfPidAndStart(t)
	_, err = s.DB.RecordWorkerSpawn(w.Id, pid, started)
	require.NoError(t, err)

	// Past the minimum interval, so that guard is not what holds the
	// second fork back: in the code as it was, a server that came back
	// after the interval (or whose child was slow to register) forked one.
	prev := timing.Multiplier()
	timing.SetMultiplier(1e-9)
	t.Cleanup(func() { timing.SetMultiplier(prev) })

	s.respawnWorkers("boot")

	got, err := s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.Equal(t, 0, *spawns)
	assert.True(t, got.RespawnIntent, "left for a later pass, which will find it registered")

	_, err = s.DB.ClaimWorkerRespawn(w.Id)
	assert.ErrorIs(t, err, database.ErrRespawnDeferred, "the claim transaction re-checks it too")
}

// Model trace NoDuplicateWorker: PUT /worker/:id/restart on a stopped
// worker whose process is still finishing its task forked a second
// process for the same id.
func TestRestartWorker_RefusesWhileTheProcessIsStillRunning(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()
	forks := 0
	s.startDaemonFn = func(w *worker.WorkerConf) error { forks++; return nil }

	w := addLiveWorker(t, s)
	_, err := s.DB.StopWorker(w.Id, "")
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("PUT", "/worker/"+w.Id.Hex()+"/restart", nil))
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, 0, forks)

	got, err := s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.True(t, got.Stopped, "a refused restart leaves the record as it was")
}

// Restarting a stopped worker by hand takes over any respawn a drain still
// owes, so a later pass does not fork a second process for it.
func TestRestartWorker_TakesOverARespawnIntent(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()
	spawns := countingSpawns(s)
	s.startDaemonFn = func(w *worker.WorkerConf) error {
		// The child registers straight away.
		reg := worker.WorkerConf{Id: w.Id, Pid: 4243, PidStartTs: 1}
		return s.DB.UpdateWorker(&reg)
	}

	w := addWorker(t, s, false)
	_, _, err := s.DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		return nil
	})
	require.NoError(t, err)
	s.adoptRestartRecordOnBoot()

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("PUT", "/worker/"+w.Id.Hex()+"/restart", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got, err := s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.False(t, got.RespawnIntent)
	s.respawnWorkers("periodic")
	assert.Equal(t, 0, *spawns)
}
