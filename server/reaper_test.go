package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/worker"
)

// The reaper's grace layers (turtlemonvh/blanket#23 phase 3).
//
// The decision table itself lives in lib/bolt/reap_test.go, where it is a
// pure table test. What is tested here is the part that only makes sense
// with a server around it: when a pass is allowed to run at all.
//
// All three layers exist for the same reason -- during a restart, a
// suspend, or an upgrade, every worker looks stale at once through no
// fault of its own, and that is precisely the moment this loop is most
// likely to destroy something.

// deadWorker registers a worker whose pid is long gone and whose last
// heartbeat is ancient: the reaper acts on it the moment it is allowed to.
func deadWorker(t *testing.T, s *ServerConfig) worker.WorkerConf {
	t.Helper()
	w := worker.WorkerConf{
		Id: objectid.NewObjectId(),
		// Pid 0 never resolves to a process, so liveness is inconclusive
		// and the longer WorkerDeadAfter threshold applies.
		Pid:         0,
		LastHeardTs: time.Now().Add(-24 * time.Hour).Unix(),
	}
	require.NoError(t, s.DB.UpdateWorker(&w))
	return w
}

func TestReaperClock_SkipsAPassWhenTheClockJumps(t *testing.T) {
	now := time.Now()
	rc := &reaperClock{baselineTs: now.Add(-time.Hour).Unix(), lastPass: now}

	// A normal pass: wall and monotonic agree.
	run, baseline := rc.beginPass(now.Add(30*time.Second), 30*time.Second, 30*time.Second)
	assert.True(t, run)
	assert.Equal(t, now.Add(-time.Hour).Unix(), baseline, "an ordinary pass leaves the baseline alone")

	// The laptop lid case: two hours of wall-clock time passed while the
	// process experienced 30 seconds.
	woke := now.Add(2 * time.Hour)
	run, baseline = rc.beginPass(woke, 2*time.Hour, 30*time.Second)
	assert.False(t, run, "a pass across a suspend must not run")
	assert.Equal(t, woke.Unix(), baseline,
		"the baseline re-arms, so workers get a full window from the moment the machine woke")

	// And the next ordinary pass runs again, against the new baseline.
	run, baseline = rc.beginPass(woke.Add(30*time.Second), 30*time.Second, 30*time.Second)
	assert.True(t, run)
	assert.Equal(t, woke.Unix(), baseline)
}

func TestReaperClock_ToleratesSmallDrift(t *testing.T) {
	now := time.Now()
	rc := &reaperClock{lastPass: now}

	// NTP slew: a second of correction over a 30s interval is normal and
	// must not stop the reaper from ever running.
	run, _ := rc.beginPass(now.Add(30*time.Second), 31*time.Second, 30*time.Second)
	assert.True(t, run)
}

// TestReaper_StalenessIsMeasuredFromServerStart is grace layer 1 at the
// server level: a worker that has not been heard from in a day is *not*
// reaped by a server that has only been up for a moment, because nobody
// could have reported to it yet.
func TestReaper_StalenessIsMeasuredFromServerStart(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := deadWorker(t, s)

	// s.StartedTs() is now, so every worker is inside its first window.
	report := s.reapOnce(s.StartedTs())
	assert.True(t, report.Empty(), "the first pass after a restart must find nothing")

	stored, err := s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.False(t, stored.Lost)

	// With a baseline from before the worker went silent, the same pass
	// reaches the same worker and acts.
	report = s.reapOnce(time.Now().Add(-48 * time.Hour).Unix())
	assert.Equal(t, 1, report.WorkersMarkedLost)
	assert.Equal(t, 1, report.WorkersStopped)

	stored, err = s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.True(t, stored.Lost)
	assert.True(t, stored.Stopped)
	assert.Contains(t, stored.StoppedReason, "reaper")
}

// TestReaper_RestartPendingSuppressesThePass is grace layer 3, driven
// through the real loop. Phase 5's restart state machine sets this flag
// while it is stopping and respawning workers -- exactly when they all
// look stale.
func TestReaper_RestartPendingSuppressesThePass(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	s.ReaperEnabled = true
	s.ReaperInterval = 20 * time.Millisecond
	// Thresholds low enough that the ancient worker qualifies immediately.
	s.ReaperWorkerStaleAfter = time.Second
	s.ReaperWorkerDeadAfter = time.Second
	s.SchedulerInterval = time.Hour // keep the scheduler out of the way

	w := deadWorker(t, s)
	s.SetRestartPending(true)

	// Back-date the baseline by starting the loop against a server whose
	// StartedTs is already old is not possible (it is set lazily to now),
	// so lean on the ancient LastHeardTs plus a stale threshold of 1s: one
	// second after start the worker qualifies -- unless a restart is
	// pending.
	stop := s.startBackgroundLoops(context.Background())
	defer stop()

	time.Sleep(1500 * time.Millisecond)
	stored, err := s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.False(t, stored.Lost, "no pass may run while a restart is pending")

	s.SetRestartPending(false)

	require.Eventually(t, func() bool {
		stored, err := s.DB.GetWorker(w.Id)
		return err == nil && stored.Lost
	}, 3*time.Second, 25*time.Millisecond, "the reaper should resume once the restart clears")
}

// TestReaper_NotStartedUnlessEnabled: the loop is opt-in at the
// ServerConfig level (command/serve.go passes the reaper.enabled config
// key, which defaults to true), so a hand-built server in a test never
// gets a goroutine rewriting its task state.
func TestReaper_NotStartedUnlessEnabled(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	s.ReaperEnabled = false
	s.SchedulerInterval = time.Hour
	w := deadWorker(t, s)

	stop := s.startBackgroundLoops(context.Background())
	time.Sleep(200 * time.Millisecond)
	stop()

	stored, err := s.DB.GetWorker(w.Id)
	require.NoError(t, err)
	assert.False(t, stored.Lost)
}

// TestReaper_StopsWithTheBackgroundLoops: the stop function returned by
// startBackgroundLoops must not return until every loop has exited --
// storage is closed immediately after it in the shutdown sequence (step
// (f) of server/lifecycle.go), and a pass still running against a closed
// bolt handle would panic.
func TestReaper_StopsWithTheBackgroundLoops(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	s.ReaperEnabled = true
	s.ReaperInterval = 10 * time.Millisecond
	s.SchedulerInterval = 10 * time.Millisecond

	stop := s.startBackgroundLoops(context.Background())
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startBackgroundLoops' stop function did not return; a loop is still running")
	}
}

func TestReapOptions_UseDefaultsAndScaling(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	opts := s.reapOptions(0)
	assert.Equal(t, DefaultReaperWorkerStaleAfter, opts.WorkerStaleAfter)
	assert.Equal(t, DefaultReaperWorkerDeadAfter, opts.WorkerDeadAfter)
	assert.Equal(t, DefaultReaperTaskStaleAfter, opts.TaskStaleAfter)
	assert.Equal(t, DefaultReaperMaxRequeues, opts.MaxRequeues)
	assert.Less(t, opts.WorkerStaleAfter, opts.WorkerDeadAfter,
		"the inconclusive-liveness threshold must be the more patient of the two")
	assert.NotNil(t, opts.IsAlive)
	assert.NotNil(t, opts.ReadJournal)
	assert.NotNil(t, opts.Requeue)
}
