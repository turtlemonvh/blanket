package server

/*

Handler and state-machine tests for /ops/restart/*
(turtlemonvh/blanket#23 phase 5).

What can be tested here is the *decision* half of the machine: which
transitions are legal, what each one does to the record and to the workers,
who is refused, and which workers a respawn pass picks up. What cannot is
the half that needs a second process — a crash between two transitions, a
re-exec that keeps the pid, an exit code a supervisor acts on. That is
scripts/restart_machine.sh, which parametrizes a real binary over every
state with BLANKET_TEST_CRASH_AT.

The spawn hook (ServerConfig.spawnWorkerFn) is what makes the respawn tests
possible at all: the real path resolves os.Executable() and forks it, which
under `go test` is the test binary.

*/

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/proclive"
	"github.com/turtlemonvh/blanket/worker"
)

// opsRequest builds a request that satisfies opsGuard, so these tests are
// about the state machine rather than about the guard (which has its own
// coverage in serve_ops_test.go).
func opsRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	var req *http.Request
	var err error
	if body == "" {
		req, err = http.NewRequest(method, url, nil)
	} else {
		req, err = http.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	require.NoError(t, err)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set(OpsHeader, "1")
	return req
}

func doOps(t *testing.T, r *gin.Engine, method, url, body string) (int, map[string]interface{}) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, opsRequest(t, method, url, body))
	out := map[string]interface{}{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func restartTestServer(t *testing.T) (*ServerConfig, *gin.Engine, func()) {
	t.Helper()
	s, cleanup := NewTestServer()
	// A resolved mode, so these tests do not depend on whether the machine
	// running them happens to look supervised.
	s.ExecMode = database.ExecModeExit
	return s, s.GetRouter(), cleanup
}

func addWorker(t *testing.T, s *ServerConfig, stopped bool) worker.WorkerConf {
	t.Helper()
	w := worker.WorkerConf{
		Id:      objectid.NewObjectId(),
		Pid:     4242,
		Stopped: stopped,
		// A start time that cannot match pid 4242's real one, so
		// proclive.IsAlive answers "conclusively dead" rather than
		// depending on what else is running on the test box.
		PidStartTs: 1,
		StartedTs:  time.Now().Unix(),
	}
	require.NoError(t, s.DB.UpdateWorker(&w))
	return w
}

// ---------------------------------------------------------------------------
// Transitions
// ---------------------------------------------------------------------------

func TestRestart_HappyPathThroughEveryState(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()
	addWorker(t, s, false)

	code, body := doOps(t, r, "GET", "/ops/restart/status", "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, database.RestartStateIdle, body["restart"].(map[string]interface{})["state"])
	assert.Equal(t, false, body["active"])

	code, body = doOps(t, r, "POST", "/ops/restart/begin", `{"reason":"upgrade to 0.4.0"}`)
	require.Equal(t, http.StatusOK, code)
	rr := body["restart"].(map[string]interface{})
	assert.Equal(t, database.RestartStateStaged, rr["state"])
	assert.Equal(t, "upgrade to 0.4.0", rr["reason"])
	assert.NotEmpty(t, rr["id"])
	assert.False(t, s.spawnIsPaused(), "staging alone must not stop the box from working")

	// The backup endpoint advances the record: the state records a backup
	// the server took, not one a caller claimed.
	s.BackupDir = t.TempDir()
	code, _ = doOps(t, r, "POST", "/ops/backup", "")
	require.Equal(t, http.StatusOK, code)
	stored, err := s.DB.RestartRecord()
	require.NoError(t, err)
	assert.Equal(t, database.RestartStateBackedUp, stored.State)
	assert.NotEmpty(t, stored.BackupPath, "the record must name the file, or 'restore the backup' is useless advice")

	code, body = doOps(t, r, "POST", "/ops/restart/pause", "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, body["spawnPaused"])
	assert.True(t, s.spawnIsPaused())

	code, _ = doOps(t, r, "POST", "/ops/restart/swapped", "")
	require.Equal(t, http.StatusOK, code)

	code, body = doOps(t, r, "POST", "/ops/restart/drain?wait=false", "")
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, body["stopped"], 1)

	// exec on a router built without a running listener reports that
	// rather than pretending, but it has still moved the record.
	code, _ = doOps(t, r, "POST", "/ops/restart/exec", "")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	stored, err = s.DB.RestartRecord()
	require.NoError(t, err)
	assert.Equal(t, database.RestartStateExecing, stored.State)
}

func TestRestart_TransitionsAreForwardOnly(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()

	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/begin", "")))
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/pause", "")))

	// Backwards, and sideways onto a state already passed.
	code, body := doOps(t, r, "POST", "/ops/restart/pause", "")
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, body["error"], "forward-only")

	// Beginning a second restart while one is in flight is a conflict with
	// a specific instruction, not a generic refusal.
	code, body = doOps(t, r, "POST", "/ops/restart/begin", "")
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, body["error"], "abort it first")

	_ = s
}

func TestRestart_TransitionsWithNoRestartInFlightAre409(t *testing.T) {
	_, r, cleanup := restartTestServer(t)
	defer cleanup()

	for _, path := range []string{
		"/ops/restart/pause",
		"/ops/restart/swapped",
		"/ops/restart/drain",
		"/ops/restart/exec",
		"/ops/restart/abort",
	} {
		code, body := doOps(t, r, "POST", path, "")
		assert.Equal(t, http.StatusConflict, code, path)
		assert.NotEmpty(t, body["error"], path)
	}
}

// Skipping states forward is legal and is the normal case: a routine
// restart takes no backup, swaps no binary and drains nothing.
func TestRestart_MaySkipStraightFromStagedToExecing(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()

	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/begin", "")))
	code, _ := doOps(t, r, "POST", "/ops/restart/exec", "")
	assert.Equal(t, http.StatusServiceUnavailable, code, "no listener, but the transition itself is legal")

	stored, err := s.DB.RestartRecord()
	require.NoError(t, err)
	assert.Equal(t, database.RestartStateExecing, stored.State)
}

func TestRestart_EveryRouteIsBehindTheOpsGuard(t *testing.T) {
	_, r, cleanup := restartTestServer(t)
	defer cleanup()

	for method, path := range map[string]string{
		"GET":  "/ops/restart/status",
		"POST": "/ops/restart/begin",
	} {
		// Loopback, but no header.
		req, err := http.NewRequest(method, path, nil)
		require.NoError(t, err)
		req.RemoteAddr = "127.0.0.1:1"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code, "%s %s without the header", method, path)

		// Header, but not loopback.
		req, err = http.NewRequest(method, path, nil)
		require.NoError(t, err)
		req.RemoteAddr = "10.1.2.3:9999"
		req.Header.Set(OpsHeader, "1")
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code, "%s %s from off-box", method, path)
	}
}

// ---------------------------------------------------------------------------
// The pause
// ---------------------------------------------------------------------------

func TestRestart_PausedRefusesWorkerSpawnWith409(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()
	existing := addWorker(t, s, true)

	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/begin", "")))
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/pause", "")))

	for name, req := range map[string]*http.Request{
		"a new worker":    httptest.NewRequest("POST", "/worker/", strings.NewReader(`{"tags":["a"]}`)),
		"an existing one": httptest.NewRequest("PUT", "/worker/"+existing.Id.Hex()+"/restart", nil),
		"and again for POST": httptest.NewRequest("POST", "/worker/",
			strings.NewReader(`{"tags":["b"],"checkInterval":1}`)),
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusConflict, w.Code, name)
		assert.Contains(t, w.Body.String(), "worker spawn is paused", name)
	}

	// The refusal must not have started the worker down the restart path
	// halfway: a 409 that had already cleared Stopped would leave a record
	// claiming a worker is running with no process behind it.
	got, err := s.DB.GetWorker(existing.Id)
	require.NoError(t, err)
	assert.True(t, got.Stopped, "a refused restart must not clear the Stopped flag")

	// And the pause lifts on abort.
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/abort", "")))
	assert.False(t, s.spawnIsPaused())
}

// Before PAUSED there is nothing to protect, and refusing spawn for the
// length of a backup would be a cost with nothing bought by it.
func TestRestart_StagedDoesNotPauseSpawn(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()

	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/begin", "")))
	assert.False(t, s.spawnIsPaused())

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/worker/", strings.NewReader(`{"tags":["a"],"checkInterval":0.1}`)))
	assert.NotEqual(t, http.StatusConflict, w.Code, "STAGED must not refuse a spawn")
}

// ---------------------------------------------------------------------------
// Drain, abort, and the watchdog
// ---------------------------------------------------------------------------

func TestRestart_DrainStopsWorkersAndAbortBringsThemBack(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()

	var spawned []objectid.ObjectId
	s.spawnWorkerFn = func(w *worker.WorkerConf) (worker.WorkerConf, error) {
		spawned = append(spawned, w.Id)
		return *w, nil
	}

	running := addWorker(t, s, false)
	stoppedBefore := addWorker(t, s, true)

	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/begin", "")))
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/drain?wait=false", "")))

	got, err := s.DB.GetWorker(running.Id)
	require.NoError(t, err)
	assert.True(t, got.Stopped)
	assert.True(t, got.RespawnIntent)

	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/abort", "")))

	assert.Equal(t, []objectid.ObjectId{running.Id}, spawned,
		"abort after a drain must bring back exactly what the drain stopped")

	got, err = s.DB.GetWorker(running.Id)
	require.NoError(t, err)
	assert.False(t, got.RespawnIntent, "a confirmed spawn clears the intent")

	got, err = s.DB.GetWorker(stoppedBefore.Id)
	require.NoError(t, err)
	assert.False(t, got.RespawnIntent, "a worker stopped before the restart is not the restart's to restart")
}

func TestRestart_DrainModeNeverRefuses(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()
	s.DrainMode = database.DrainModeNever
	addWorker(t, s, false)

	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/begin", "")))
	code, body := doOps(t, r, "POST", "/ops/restart/drain", "")
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, body["error"], "drain-mode=never")
}

// A driver that is killed must not leave the server unable to spawn a
// worker with no way out but the restart that just failed.
func TestRestart_WatchdogAbortsAnAbandonedRestart(t *testing.T) {
	s, r, cleanup := restartTestServer(t)
	defer cleanup()

	var spawned []objectid.ObjectId
	s.spawnWorkerFn = func(w *worker.WorkerConf) (worker.WorkerConf, error) {
		spawned = append(spawned, w.Id)
		return *w, nil
	}
	running := addWorker(t, s, false)

	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/begin", "")))
	require.Equal(t, http.StatusOK, mustCode(doOps(t, r, "POST", "/ops/restart/drain?wait=false", "")))
	require.True(t, s.spawnIsPaused())

	// Backdate the deadline: the driver has stopped calling in.
	_, err := s.DB.UpdateRestartRecord(func(rr *database.RestartRecord) error {
		rr.DeadlineTs = time.Now().Unix() - 1
		return nil
	})
	require.NoError(t, err)
	rr, err := s.DB.RestartRecord()
	require.NoError(t, err)
	s.applyRestartRecord(rr)

	require.True(t, s.restartDeadlinePassed(time.Now()))
	_, err = s.abortRestart("watchdog (test)")
	require.NoError(t, err)

	assert.False(t, s.spawnIsPaused(), "the abandoned pause must lift")
	assert.Equal(t, []objectid.ObjectId{running.Id}, spawned,
		"the watchdog's abort respawns what the drain stopped, exactly as the manual abort does")

	stored, err := s.DB.RestartRecord()
	require.NoError(t, err)
	assert.False(t, stored.Active())
}

func TestRestart_DeadlineIsRefreshedByEveryTransition(t *testing.T) {
	_, r, cleanup := restartTestServer(t)
	defer cleanup()

	_, body := doOps(t, r, "POST", "/ops/restart/begin", `{"deadlineSeconds": 120}`)
	first := int64(body["restart"].(map[string]interface{})["deadlineTs"].(float64))

	time.Sleep(1100 * time.Millisecond)
	_, body = doOps(t, r, "POST", "/ops/restart/pause", "")
	second := int64(body["restart"].(map[string]interface{})["deadlineTs"].(float64))

	assert.Greater(t, second, first,
		"the deadline is a watchdog, not a budget: a driver still calling in is never timed out")
}

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

// A boot is the strongest evidence there is that the process which wrote
// the record is gone. Whatever state it was in, this one starts clean —
// otherwise a crashed driver could leave an install permanently unable to
// start a worker.
func TestRestart_BootClearsTheRecordFromEveryState(t *testing.T) {
	for _, state := range []string{
		database.RestartStateStaged,
		database.RestartStateBackedUp,
		database.RestartStatePaused,
		database.RestartStateSwapped,
		database.RestartStateDraining,
		database.RestartStateExecing,
	} {
		t.Run(state, func(t *testing.T) {
			s, cleanup := NewTestServer()
			defer cleanup()

			require.NoError(t, s.DB.SetRestartRecord(database.RestartRecord{
				State:          state,
				Id:             "r1",
				FromInstanceId: "some-previous-process",
				DeadlineTs:     time.Now().Unix() + 300,
			}))

			s.adoptRestartRecordOnBoot()

			stored, err := s.DB.RestartRecord()
			require.NoError(t, err)
			assert.False(t, stored.Active(), "the record must not survive a boot")
			assert.False(t, s.spawnIsPaused(), "a booted server is never born paused")
			assert.False(t, s.restartPending())
		})
	}
}

// The record is the plan; the intents are the debt. Clearing the plan on
// boot must not clear what is owed to specific workers.
func TestRestart_BootRespawnsWhatTheDrainStopped(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	var spawned []objectid.ObjectId
	s.spawnWorkerFn = func(w *worker.WorkerConf) (worker.WorkerConf, error) {
		spawned = append(spawned, w.Id)
		return *w, nil
	}

	drained := addWorker(t, s, false)
	untouched := addWorker(t, s, true)
	_, _, err := s.DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		return nil
	})
	require.NoError(t, err)

	s.adoptRestartRecordOnBoot()
	s.respawnWorkers("boot")

	assert.Equal(t, []objectid.ObjectId{drained.Id}, spawned)

	got, err := s.DB.GetWorker(drained.Id)
	require.NoError(t, err)
	assert.False(t, got.Stopped)
	assert.False(t, got.RespawnIntent)

	got, err = s.DB.GetWorker(untouched.Id)
	require.NoError(t, err)
	assert.True(t, got.Stopped)
}

// ---------------------------------------------------------------------------
// The respawn storm guards
// ---------------------------------------------------------------------------

func TestRespawn_GenerationCapStopsTrying(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	spawns := 0
	s.spawnWorkerFn = func(w *worker.WorkerConf) (worker.WorkerConf, error) {
		spawns++
		return *w, nil
	}

	// The respawn bookkeeping is server-owned, so it has to be built
	// through the paths that own it rather than by writing the struct:
	// UpdateWorker deliberately drops a caller's version of these fields.
	w := addWorker(t, s, false)
	_, _, err := s.DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		return nil
	})
	require.NoError(t, err)
	for i := 0; i < MaxRespawnAttempts; i++ {
		claimed, err := s.DB.ClaimWorkerRespawn(w.Id)
		require.NoError(t, err)
		require.Equal(t, i+1, claimed.RespawnAttempts)
	}
	// The cap is checked before the minimum interval (see respawnWorker),
	// so a worker that has exhausted its budget is reported as such rather
	// than deferred forever by the interval guard.
	s.respawnWorkers("test")

	assert.Equal(t, 0, spawns, "past the cap, a worker that cannot start stops being started")
	got, err := s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.False(t, got.RespawnIntent)
	assert.Contains(t, got.StoppedReason, "gave up respawning",
		"the operator must find a reason, not an absence")
}

func TestRespawn_MinimumIntervalSkipsARecentAttempt(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	spawns := 0
	s.spawnWorkerFn = func(w *worker.WorkerConf) (worker.WorkerConf, error) {
		spawns++
		return *w, nil
	}

	w := addWorker(t, s, false)
	_, _, err := s.DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		return nil
	})
	require.NoError(t, err)
	// One attempt just now: this is a server restarting in a tight loop,
	// not a restart completing.
	_, err = s.DB.ClaimWorkerRespawn(w.Id)
	require.NoError(t, err)

	s.respawnWorkers("test")

	assert.Equal(t, 0, spawns, "two boots seconds apart must not each fork the fleet")
	got, err := s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.True(t, got.RespawnIntent, "the intent is left for a later pass, not dropped")
}

func TestRespawn_LiveWorkerIsNotSpawnedTwice(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	spawns := 0
	s.spawnWorkerFn = func(w *worker.WorkerConf) (worker.WorkerConf, error) {
		spawns++
		return *w, nil
	}

	// The at-least-once case: the previous process forked the worker and
	// died before clearing its intent, so the worker is already running.
	// This process's own pid, paired with its own start time, is the only
	// pair guaranteed to be conclusively alive on any test box.
	live := addWorker(t, s, false)
	_, _, err := s.DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		return nil
	})
	require.NoError(t, err)

	livePid, liveStart := selfPidAndStart(t)
	registered := worker.WorkerConf{Id: live.Id, Pid: livePid, PidStartTs: liveStart}
	require.NoError(t, s.DB.UpdateWorker(&registered))

	s.respawnWorkers("test")

	assert.Equal(t, 0, spawns, "a worker that is demonstrably running must not be spawned a second time")
	got, err := s.DB.GetWorker(live.Id)
	require.NoError(t, err)
	assert.False(t, got.RespawnIntent, "the intent is settled by observing the worker, not by forking another")
}

// ---------------------------------------------------------------------------
// Exec mode
// ---------------------------------------------------------------------------

func TestResolveExecMode(t *testing.T) {
	t.Setenv("BLANKET_SUPERVISED", "1")
	assert.Equal(t, database.ExecModeExit, resolveExecMode(database.ExecModeAuto),
		"under a supervisor, auto means exit and let it start the replacement")
	assert.Equal(t, database.ExecModeExit, resolveExecMode(""))

	t.Setenv("BLANKET_SUPERVISED", "0")
	// Not conclusive on its own: a test box may be running under systemd.
	// What matters is that an explicit mode is never reinterpreted.
	assert.Equal(t, database.ExecModeExit, resolveExecMode(database.ExecModeExit))

	// Nonsense falls back to auto rather than to a mode nobody asked for.
	assert.Contains(t,
		[]string{database.ExecModeExec, database.ExecModeExit},
		resolveExecMode("sideways"))
}

func TestSupervised_EnvOverrideWinsOverEverything(t *testing.T) {
	t.Setenv("INVOCATION_ID", "abc123")
	assert.True(t, Supervised(), "systemd sets INVOCATION_ID in every service's environment")

	t.Setenv("BLANKET_SUPERVISED", "false")
	assert.False(t, Supervised(), "the explicit override is what a deployment this cannot recognise uses")
}

func TestExecModeExitCode(t *testing.T) {
	// A restart that exits must not exit 0: the unit blanket installs says
	// Restart=on-failure, so a clean exit is exactly what leaves it down.
	assert.Equal(t, RestartExitCode, execModeExitCode(database.ExecModeExit))
	assert.NotEqual(t, 0, RestartExitCode)
	assert.Equal(t, 0, execModeExitCode(database.ExecModeExec),
		"a re-exec never exits; the pid stays and starts answering again")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustCode(code int, _ map[string]interface{}) int { return code }

// selfPidAndStart returns this process's pid and its start time as
// lib/proclive reads it — the only pair guaranteed to be conclusively
// alive on whatever box the tests are running on.
func selfPidAndStart(t *testing.T) (int, int64) {
	t.Helper()
	pid := os.Getpid()
	started, ok := proclive.StartTime(pid)
	if !ok {
		t.Skip("this platform cannot report a process start time; pid liveness is inconclusive here")
	}
	return pid, started
}
