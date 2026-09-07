package server

/*

The reaper loop (turtlemonvh/blanket#23 phase 3).

Three cleanup routines exist in the storage layer -- stalled workers,
stalled tasks, unacked queue entries (lib/bolt/reap.go) -- and this is what
schedules them. It is a `go s.reaperLoop(ctx)` inside startBackgroundLoops,
alongside the scheduler, deliberately *not* an init() ticker: there is
already one of those in serve_metrics.go, permanently leaked with no way to
stop it, and a loop that rewrites task state must be cancellable at
shutdown. It is cancelled at step (e) of the teardown, before storage
closes (see server/lifecycle.go).

# The restart problem

Everything this loop does keys off "how long since we heard from this
worker". During a restart -- which is the entire point of issue #23 -- the
answer is "a while, for all of them at once", through no fault of any
worker. The first pass after a restart is therefore the single most
dangerous moment in the system, and three separate layers defend it:

 1. Staleness is measured from max(LastHeardTs, baseline), where the
    baseline is the later of when this server process started serving and
    when it last noticed the clock jump. Every worker gets a full,
    fresh staleness window after a restart. This re-arms automatically if a
    second restart follows.

 2. Wall-clock versus monotonic divergence between two passes means time
    passed that this process did not experience -- a closed laptop lid, a
    suspended VM, an NTP step. Layer 1 does not cover it, because the
    server slept too and its start time is now far in the past. The pass is
    skipped *and* the baseline is re-armed, so the workers get their full
    window from the moment the machine woke rather than from a start time
    that predates the sleep.

 3. While a restart is pending (ServerConfig.SetRestartPending, which
    phase 5's state machine drives), no pass runs at all.

# The off switch

reaper.enabled ships true. It exists because the failure mode this code
guards against -- a false positive that destroys real task state -- is
exactly the failure mode the code could itself cause, and an operator
debugging a suspected false positive should be able to stop it in one
config line rather than by downgrading.

*/

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/proclive"
	"github.com/turtlemonvh/blanket/lib/timing"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

// Defaults for the reaper's knobs. All are unscaled: timeMultiplier is
// applied through lib/timing at the point of use, so a compressed test run
// moves the interval and every threshold together and cannot false-reap.
//
// The thresholds are deliberately generous relative to a worker's default
// 2s check interval. A worker has to miss a great many heartbeats before
// anything happens, because the cost of waiting is a stale row in the UI
// and the cost of acting early is destroyed work.
const (
	// DefaultReaperInterval is how often a pass runs.
	DefaultReaperInterval = 30 * time.Second

	// DefaultReaperWorkerStaleAfter is when a silent worker is marked
	// Lost in the UI. Nothing is stopped or rewritten at this threshold.
	DefaultReaperWorkerStaleAfter = 2 * time.Minute

	// DefaultReaperWorkerDeadAfter is the *only* threshold that can stop a
	// worker on heartbeat evidence alone -- when pid liveness could not
	// answer. Five times the stale threshold, because heartbeat silence is
	// much weaker evidence than a conclusively dead process.
	DefaultReaperWorkerDeadAfter = 10 * time.Minute

	// DefaultReaperTaskStaleAfter is how long a CLAIMED/RUNNING task may
	// go without an update before the reaper looks at it. Looking is not
	// acting: what happens next depends entirely on the outcome journal.
	DefaultReaperTaskStaleAfter = 5 * time.Minute

	// DefaultReaperMaxRequeues is the poison-task cap.
	DefaultReaperMaxRequeues = 3

	// clockDivergenceTolerance is how far wall-clock and monotonic elapsed
	// time may disagree between two passes before the pass is skipped as
	// untrustworthy. Small clock adjustments (NTP slew) are well inside
	// it; a suspend, a VM pause, or an NTP step is not.
	clockDivergenceTolerance = 5 * time.Second
)

// reaperInterval and the threshold accessors resolve the configured value,
// its default, and the time multiplier in one place.
func (s *ServerConfig) reaperInterval() time.Duration {
	return timing.Scale(orDefault(s.ReaperInterval, DefaultReaperInterval))
}

func orDefault(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

// reapOptions builds the inputs for one pass. Everything the storage layer
// needs to decide arrives here, so the decision logic itself touches
// neither the clock nor the OS.
func (s *ServerConfig) reapOptions(baselineTs int64) *database.ReapOptions {
	maxRequeues := s.ReaperMaxRequeues
	if maxRequeues <= 0 {
		maxRequeues = DefaultReaperMaxRequeues
	}

	return &database.ReapOptions{
		Now:              time.Now,
		IsAlive:          proclive.IsAlive,
		ReadJournal:      worker.ReadOutcomeJournal,
		Requeue:          func(t *tasks.Task) error { return s.Q.AddTask(t) },
		BaselineTs:       baselineTs,
		WorkerStaleAfter: timing.Scale(orDefault(s.ReaperWorkerStaleAfter, DefaultReaperWorkerStaleAfter)),
		WorkerDeadAfter:  timing.Scale(orDefault(s.ReaperWorkerDeadAfter, DefaultReaperWorkerDeadAfter)),
		TaskStaleAfter:   timing.Scale(orDefault(s.ReaperTaskStaleAfter, DefaultReaperTaskStaleAfter)),
		MaxRequeues:      maxRequeues,
	}
}

// reaperClock carries the two grace layers that are about time: the
// staleness baseline, and the last pass's timestamp used to detect a clock
// jump.
//
// Split out from the loop so both are testable without a ticker. The loop
// reads the clock; this decides what the reading means.
type reaperClock struct {
	baselineTs int64
	lastPass   time.Time
}

// beginPass decides whether a pass may run, given how much wall-clock and
// monotonic time elapsed since the previous one, and returns the baseline
// the pass should measure staleness from.
//
// A large disagreement between the two means time passed that this process
// did not experience: the machine slept, the VM was paused, or the clock
// was stepped. "How long since we heard from this worker" is then a
// question the pass cannot answer honestly, so it is skipped -- and the
// baseline is re-armed to now, so the workers get a full, fresh staleness
// window from the moment the machine woke rather than from a start time
// that predates the sleep. Grace layer 1 alone does not cover this: the
// server slept too, and its start time is now hours in the past.
func (rc *reaperClock) beginPass(now time.Time, wall, mono time.Duration) (bool, int64) {
	rc.lastPass = now

	drift := wall - mono
	if drift < 0 {
		drift = -drift
	}
	if drift > timing.Scale(clockDivergenceTolerance) {
		rc.baselineTs = now.Unix()
		log.WithFields(log.Fields{
			"wallElapsed":      wall.String(),
			"monotonicElapsed": mono.String(),
		}).Warn("reaper: wall clock and monotonic clock disagree (a suspend, or a clock step); skipping this pass and re-arming the staleness baseline")
		return false, rc.baselineTs
	}

	return true, rc.baselineTs
}

// elapsedSince reports how much wall-clock and monotonic time passed
// between two readings of time.Now().
//
// A time.Time from time.Now() carries both; Sub uses the monotonic
// reading, and Round(0) strips it so the same subtraction uses the wall
// clock. That pair is the entire suspend/clock-step detector.
func elapsedSince(prev, now time.Time) (wall, mono time.Duration) {
	return now.Round(0).Sub(prev.Round(0)), now.Sub(prev)
}

// reaperLoop runs a pass every interval until ctx is cancelled.
func (s *ServerConfig) reaperLoop(ctx context.Context, interval time.Duration) {
	// The baseline starts at this process's start time (grace layer 1) and
	// is re-armed whenever the clock jumps (layer 2).
	clock := &reaperClock{baselineTs: s.StartedTs(), lastPass: time.Now()}

	log.WithFields(log.Fields{
		"interval":         interval,
		"workerStaleAfter": timing.Scale(orDefault(s.ReaperWorkerStaleAfter, DefaultReaperWorkerStaleAfter)),
		"workerDeadAfter":  timing.Scale(orDefault(s.ReaperWorkerDeadAfter, DefaultReaperWorkerDeadAfter)),
		"taskStaleAfter":   timing.Scale(orDefault(s.ReaperTaskStaleAfter, DefaultReaperTaskStaleAfter)),
	}).Info("reaper: started")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("reaper: stopped")
			return
		case now := <-ticker.C:
			// Grace layer 2: time the process did not experience.
			wall, mono := elapsedSince(clock.lastPass, now)
			run, baselineTs := clock.beginPass(now, wall, mono)
			if !run {
				continue
			}

			// Grace layer 3: a restart is in flight; every worker is about
			// to look stale for reasons that have nothing to do with them.
			if s.restartPending() {
				log.Debug("reaper: a restart is pending; skipping this pass")
				continue
			}

			s.reapOnce(baselineTs)
		}
	}
}

// reapOnce runs the three cleanup routines and logs what changed.
//
// Order matters a little: workers first, so a worker that has just been
// found conclusively dead is already marked before the task pass asks
// whether a task's worker is gone, and the two agree within one pass
// instead of two.
func (s *ServerConfig) reapOnce(baselineTs int64) database.ReapReport {
	opts := s.reapOptions(baselineTs)
	report := database.ReapReport{}

	if s.DB != nil {
		r, err := s.DB.CleanupStalledWorkers(opts)
		if err != nil {
			log.WithField("err", err).Warn("reaper: worker pass failed")
		}
		report.Add(r)

		r, err = s.DB.CleanupStalledTasks(opts)
		if err != nil {
			log.WithField("err", err).Warn("reaper: task pass failed")
		}
		report.Add(r)
	}

	if s.Q != nil {
		r, err := s.Q.CleanupUnclaimedTasks(opts)
		if err != nil {
			log.WithField("err", err).Warn("reaper: queue pass failed")
		}
		report.Add(r)
	}

	if !report.Empty() {
		log.WithFields(log.Fields{
			"workersMarkedLost":   report.WorkersMarkedLost,
			"workersStopped":      report.WorkersStopped,
			"tasksRecovered":      report.TasksRecovered,
			"tasksRequeued":       report.TasksRequeued,
			"tasksFailed":         report.TasksFailed,
			"queueEntriesFreed":   report.QueueEntriesFreed,
			"queueEntriesDropped": report.QueueEntriesDropped,
			"leftAlone":           report.LeftAlone,
		}).Warn("reaper: pass made changes")
		if report.WorkersMarkedLost > 0 || report.WorkersStopped > 0 {
			s.WorkerEvents.Notify()
		}
		if report.TasksRecovered > 0 || report.TasksRequeued > 0 || report.TasksFailed > 0 {
			s.TaskEvents.Notify()
		}
	}

	return report
}
