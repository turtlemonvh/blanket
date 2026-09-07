package bolt

/*

Storage-level tests for the restart state machine
(turtlemonvh/blanket#23 phase 5).

The headline one is TestStopWorkersForRestart_IsOneTransaction. Everything
else here is ordinary CRUD; that one is the invariant the whole phase rests
on, and the only way to assert it is to make the transaction fail *after*
its callback has already mutated the record, and then check that nothing at
all was written — neither the record nor any worker.

*/

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/worker"
)

func makeWorker(t *testing.T, DB database.BlanketDB, stopped bool) worker.WorkerConf {
	t.Helper()
	w := worker.WorkerConf{
		Id:         objectid.NewObjectId(),
		Pid:        4242,
		PidStartTs: 1,
		Stopped:    stopped,
		StartedTs:  time.Now().Unix(),
	}
	require.NoError(t, DB.UpdateWorker(&w))
	return w
}

func TestRestartRecord_AbsentReadsAsIdle(t *testing.T) {
	DB, closer := NewTestDB()
	defer closer()

	rr, err := DB.RestartRecord()
	require.NoError(t, err)
	assert.Equal(t, "", rr.State)
	assert.False(t, rr.Active(), "an absent record is IDLE, not an error")
	assert.False(t, rr.PausesSpawn())
}

func TestUpdateRestartRecord_WritesAndThenDeletesOnIdle(t *testing.T) {
	DB, closer := NewTestDB()
	defer closer()

	rr, err := DB.UpdateRestartRecord(func(rr *database.RestartRecord) error {
		rr.State = database.RestartStatePaused
		rr.Id = "abc"
		rr.DeadlineTs = 99
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, database.RestartStatePaused, rr.State)
	assert.NotZero(t, rr.UpdatedTs, "every write stamps the record")

	stored, err := DB.RestartRecord()
	require.NoError(t, err)
	assert.Equal(t, database.RestartStatePaused, stored.State)
	assert.True(t, stored.PausesSpawn())

	// Settling back to IDLE removes the key rather than storing an inert
	// document, so "is a restart in flight?" stays answerable by presence.
	_, err = DB.UpdateRestartRecord(func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateIdle
		return nil
	})
	require.NoError(t, err)

	stored, err = DB.RestartRecord()
	require.NoError(t, err)
	assert.Equal(t, "", stored.State)
	assert.Equal(t, "", stored.Id, "clearing the record must not leave the old id behind")
}

// A rejected transition must leave the record exactly as it was. This is
// what lets a handler return 409 without also having to undo anything.
func TestUpdateRestartRecord_CallbackErrorWritesNothing(t *testing.T) {
	DB, closer := NewTestDB()
	defer closer()

	_, err := DB.UpdateRestartRecord(func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateStaged
		rr.Id = "first"
		return nil
	})
	require.NoError(t, err)

	boom := errors.New("refused")
	_, err = DB.UpdateRestartRecord(func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateExecing
		rr.Id = "second"
		return boom
	})
	require.ErrorIs(t, err, boom)

	stored, err := DB.RestartRecord()
	require.NoError(t, err)
	assert.Equal(t, database.RestartStateStaged, stored.State)
	assert.Equal(t, "first", stored.Id)
}

// The central invariant: the record's move to DRAINING and every worker's
// stop-plus-respawn-intent are one commit.
func TestStopWorkersForRestart_IsOneTransaction(t *testing.T) {
	DB, closer := NewTestDB()
	defer closer()

	running := makeWorker(t, DB, false)

	_, err := DB.UpdateRestartRecord(func(rr *database.RestartRecord) error {
		rr.State = database.RestartStatePaused
		return nil
	})
	require.NoError(t, err)

	boom := errors.New("refused")
	_, stopped, err := DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.Empty(t, stopped)

	// Neither half landed. A version of this that wrote the workers first
	// and the record second would pass the record check and fail here,
	// which is exactly the bug this asserts against.
	rr, err := DB.RestartRecord()
	require.NoError(t, err)
	assert.Equal(t, database.RestartStatePaused, rr.State, "the record must not have advanced")

	w, err := DB.GetWorker(running.Id)
	require.NoError(t, err)
	assert.False(t, w.Stopped, "no worker may be stopped by a transaction that did not commit")
	assert.False(t, w.RespawnIntent)
}

func TestStopWorkersForRestart_StopsRunningWorkersWithIntent(t *testing.T) {
	DB, closer := NewTestDB()
	defer closer()

	running := makeWorker(t, DB, false)
	alreadyStopped := makeWorker(t, DB, true)

	rr, stopped, err := DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		rr.Id = "r1"
		return nil
	})
	require.NoError(t, err)
	require.Len(t, stopped, 1)
	assert.Equal(t, running.Id, stopped[0].Id)
	assert.Equal(t, 1, rr.DrainedWorkers, "the count is written in the same transaction as the stops")

	got, err := DB.GetWorker(running.Id)
	require.NoError(t, err)
	assert.True(t, got.Stopped)
	assert.True(t, got.RespawnIntent)
	assert.Equal(t, worker.StopReasonRestart, got.StoppedReason)
	assert.Equal(t, 0, got.RespawnAttempts, "a fresh drain restores the full generation budget")

	// A worker that was already stopped was stopped by somebody else, for
	// a reason this restart knows nothing about. Bringing it back
	// afterwards would be the restart inventing a worker.
	got, err = DB.GetWorker(alreadyStopped.Id)
	require.NoError(t, err)
	assert.False(t, got.RespawnIntent, "an already-stopped worker must not acquire a respawn intent")
}

// The reason on a stop decides whether a respawn intent survives it: a
// worker reporting its own shutdown is the drain working, an operator's
// stop is a decision that outranks the drain.
func TestStopWorker_ReasonDecidesTheRespawnIntent(t *testing.T) {
	for name, tc := range map[string]struct {
		reason     string
		wantIntent bool
	}{
		"the worker's own shutdown report": {worker.StopReasonSelf, true},
		"an operator's stop (no reason)":   {"", false},
		"some other attributed stop":       {"reaper: pid is gone", false},
	} {
		t.Run(name, func(t *testing.T) {
			DB, closer := NewTestDB()
			defer closer()

			w := makeWorker(t, DB, false)
			_, _, err := DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
				rr.State = database.RestartStateDraining
				return nil
			})
			require.NoError(t, err)

			stopped, err := DB.StopWorker(w.Id, tc.reason)
			require.NoError(t, err)
			assert.True(t, stopped.Stopped)
			assert.Equal(t, tc.wantIntent, stopped.RespawnIntent)
		})
	}
}

// A respawned worker registers itself *while* its intent is still set --
// the intent is only cleared once the spawn is confirmed -- so the field
// merge must not let that registration erase the server's bookkeeping.
func TestUpdateWorker_PreservesRespawnBookkeeping(t *testing.T) {
	DB, closer := NewTestDB()
	defer closer()

	w := makeWorker(t, DB, false)
	_, _, err := DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		return nil
	})
	require.NoError(t, err)

	claimed, err := DB.ClaimWorkerRespawn(w.Id)
	require.NoError(t, err)
	require.Equal(t, 1, claimed.RespawnAttempts)

	// What the respawned process sends on registration: its own fields,
	// and nothing it could know about the restart.
	reregistered := worker.WorkerConf{Id: w.Id, Pid: 5150, PidStartTs: 7, StartedTs: time.Now().Unix()}
	require.NoError(t, DB.UpdateWorker(&reregistered))

	got, err := DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.True(t, got.RespawnIntent, "registration must not clear an intent the server has not confirmed yet")
	assert.Equal(t, 1, got.RespawnAttempts)
	assert.NotZero(t, got.LastRespawnTs)
	assert.Equal(t, 5150, got.Pid, "the worker still owns its own pid")
}

func TestClaimWorkerRespawn_CountsBeforeTheForkAndClearsThePid(t *testing.T) {
	DB, closer := NewTestDB()
	defer closer()

	w := makeWorker(t, DB, false)
	_, _, err := DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		return nil
	})
	require.NoError(t, err)

	claimed, err := DB.ClaimWorkerRespawn(w.Id)
	require.NoError(t, err)
	assert.False(t, claimed.Stopped, "the claim clears Stopped so the new process can register")
	assert.True(t, claimed.RespawnIntent, "the intent survives the claim; only a confirmed spawn clears it")
	assert.Equal(t, 1, claimed.RespawnAttempts)
	assert.NotZero(t, claimed.LastRespawnTs)
	assert.Zero(t, claimed.Pid, "the dead pid is cleared, or the wait for the new process ends instantly")
	assert.Zero(t, claimed.PidStartTs)

	// A second claim counts again. The cap converges even when every
	// attempt takes the server down with it, because the counting happens
	// before the fork.
	claimed, err = DB.ClaimWorkerRespawn(w.Id)
	require.NoError(t, err)
	assert.Equal(t, 2, claimed.RespawnAttempts)
}

func TestClaimWorkerRespawn_LeavesAClearedIntentAlone(t *testing.T) {
	DB, closer := NewTestDB()
	defer closer()

	w := makeWorker(t, DB, true)

	claimed, err := DB.ClaimWorkerRespawn(w.Id)
	require.NoError(t, err)
	assert.False(t, claimed.RespawnIntent)
	assert.True(t, claimed.Stopped, "no intent, no claim: the worker stays stopped")
	assert.Equal(t, 0, claimed.RespawnAttempts)
}

func TestClearWorkerRespawn_ReasonRecordsWhyTheWorkerStayedDown(t *testing.T) {
	DB, closer := NewTestDB()
	defer closer()

	w := makeWorker(t, DB, false)
	_, _, err := DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateDraining
		return nil
	})
	require.NoError(t, err)

	// The success case: the spawn worked, so there is nothing to explain.
	cleared, err := DB.ClearWorkerRespawn(w.Id, "")
	require.NoError(t, err)
	assert.False(t, cleared.RespawnIntent)
	assert.True(t, cleared.Stopped, "clearing an intent does not by itself start a worker")

	// The give-up case: the operator has to be able to find out why a
	// worker did not come back.
	cleared, err = DB.ClearWorkerRespawn(w.Id, "restart: gave up respawning after 3 attempts")
	require.NoError(t, err)
	assert.False(t, cleared.RespawnIntent)
	assert.True(t, cleared.Stopped)
	assert.Contains(t, cleared.StoppedReason, "gave up respawning")
}

func TestRestartStateRank_IsForwardOnlyAndTolerantOfNonsense(t *testing.T) {
	assert.Equal(t, 0, database.RestartStateRank(""), "an absent record ranks as IDLE")
	assert.Equal(t, 0, database.RestartStateRank(database.RestartStateIdle))
	assert.Less(t, database.RestartStateRank(database.RestartStateStaged),
		database.RestartStateRank(database.RestartStateBackedUp))
	assert.Less(t, database.RestartStateRank(database.RestartStatePaused),
		database.RestartStateRank(database.RestartStateSwapped))
	assert.Less(t, database.RestartStateRank(database.RestartStateDraining),
		database.RestartStateRank(database.RestartStateExecing))
	assert.Equal(t, -1, database.RestartStateRank("BANANA"))
	assert.Equal(t, -1, database.RestartStateRank(database.RestartStateVerified),
		"VERIFIED is reached only by the next process's boot, never by a transition")
}
