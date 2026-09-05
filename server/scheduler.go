package server

/*

Task scheduling (turtlemonvh/blanket#61).

Two kinds of "not eligible yet" tasks live in the main task database
without ever touching the claimable queue until this loop says so:

  - SCHEDULED: a one-shot task submitted with a future ScheduledTs.
    promoteDueScheduledTasks flips it to WAITING and adds it to the queue
    once due.
  - RECURRING: a template task carrying a CronExpr. It is never itself
    queued. fireDueRecurringTasks spawns a fresh child task (its own id,
    log, and result dir) at every cron fire and advances the template's
    NextFireTs. Only templates in state RECURRING are ever fired -- a
    template that has been PAUSED (PUT /task/:id/pause) or STOPPED
    (PUT /task/:id/cancel) or deleted (DELETE /task/:id) is excluded by
    fireDueRecurringTasks' own AllowedTaskStates filter, so nothing extra
    is needed here to keep them from firing. See cancelTaskById,
    pauseTaskById, and resumeTaskById in serve_tasks.go / serve_schedule.go.

startBackgroundLoops is the single place new periodic maintenance loops
get wired up -- see the FIXME this replaces in Serve() (server/server.go).
turtlemonvh/blanket#23 phase 3 adds a reaper loop (stalled workers/tasks,
unclaimed-queue cleanup) alongside this one; that issue should add its
`go s.xLoop(ctx)` call here rather than growing its own start/stop
plumbing.

*/

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
)

// DefaultSchedulerInterval is how often the scheduler loop checks for due
// SCHEDULED tasks and RECURRING templates when ServerConfig.SchedulerInterval
// is unset (zero).
const DefaultSchedulerInterval = 2 * time.Second

// DefaultSchedulerMaxScheduled is ServerConfig.SchedulerMaxScheduled's
// value when unset (zero) -- i.e. the default for the scheduler.maxScheduled
// config key. It serves two purposes, both bounded by the same number:
//
//   - It caps how many SCHEDULED/RECURRING/PAUSED tasks a single scheduler
//     tick will scan (promoteDueScheduledTasks / fireDueRecurringTasks).
//     There's no pagination beyond this -- fine for the expected scale of
//     this feature (a handful to a few hundred pending schedules/
//     templates up to a few thousand), but a deployment leaning on
//     scheduling much more heavily than that would need these loops to
//     page through results instead.
//   - It's the limit POST /task/ enforces (via scheduledLiveCount) on how
//     many SCHEDULED+RECURRING+PAUSED tasks may be live at once, returning
//     429 once a new notBefore-future or cron submission would reach it.
//     Using the same number for both means the scheduler's own bounded
//     scan is guaranteed to see every live scheduled/recurring task --
//     there's never more of them than the scan limit allows for.
const DefaultSchedulerMaxScheduled = 10000

// maxScheduledLimit returns s.SchedulerMaxScheduled, or
// DefaultSchedulerMaxScheduled if it's unset (zero or negative).
func (s *ServerConfig) maxScheduledLimit() int {
	if s.SchedulerMaxScheduled <= 0 {
		return DefaultSchedulerMaxScheduled
	}
	return s.SchedulerMaxScheduled
}

// scheduledLiveCount returns the number of currently "live" scheduled
// tasks -- SCHEDULED (delayed one-shot) plus RECURRING/PAUSED (templates)
// -- capped at `limit`. Because database.TaskSearchConf.Limit makes
// FindTasksInBoltDB stop scanning as soon as it has found that many
// matches (see lib/bolt/database_util.go), this is a bounded query, not a
// full table scan, regardless of how many rows exist beyond the cap: the
// worst case is exactly `limit` records examined, the same bound the
// scheduler's own tick already scans up to.
func (s *ServerConfig) scheduledLiveCount(limit int) (int, error) {
	smallest, largest := fullIdRange()
	_, n, err := s.DB.GetTasks(&database.TaskSearchConf{
		JustCounts:        true,
		Limit:             limit,
		AllowedTaskStates: map[string]bool{"SCHEDULED": true, "RECURRING": true, "PAUSED": true},
		SmallestId:        smallest,
		LargestId:         largest,
	})
	return n, err
}

// startBackgroundLoops launches the server's periodic maintenance
// goroutines and returns a function that stops them all and blocks until
// they've exited. Safe to call more than once (each call starts its own
// independent set of loops with its own stop function); ServerConfig.Serve
// calls it exactly once per server instance.
func (s *ServerConfig) startBackgroundLoops(ctx context.Context) func() {
	loopCtx, cancel := context.WithCancel(ctx)

	interval := s.SchedulerInterval
	if interval <= 0 {
		interval = DefaultSchedulerInterval
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.schedulerLoop(loopCtx, interval)
	}()

	return func() {
		cancel()
		<-done
	}
}

// schedulerLoop periodically promotes due SCHEDULED tasks into the queue
// and fires due RECURRING templates. It runs as a single goroutine, so
// ticks never overlap: each tick's work finishes before the next tick's
// select case can fire.
func (s *ServerConfig) schedulerLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSchedulerTick(time.Now())
		}
	}
}

// runSchedulerTick performs one pass of both scheduling checks. Exported
// as its own method (rather than inlined in schedulerLoop) so tests can
// drive it directly with a fixed `now`, instead of racing a real ticker.
func (s *ServerConfig) runSchedulerTick(now time.Time) {
	s.promoteDueScheduledTasks(now)
	s.fireDueRecurringTasks(now)
}

// fullIdRange returns the smallest/largest ObjectId bounds
// FindTasksInBoltDB-backed searches use to mean "every id", matching the
// range lib/bolt/queue.go's ClaimTask uses for the same purpose.
func fullIdRange() (objectid.ObjectId, objectid.ObjectId) {
	return objectid.NewObjectIdWithTime(time.Unix(0, 0)),
		objectid.NewObjectIdWithTime(time.Unix(database.FAR_FUTURE_SECONDS, 0))
}

// promoteDueScheduledTasks moves every SCHEDULED task whose ScheduledTs
// has passed into the WAITING state and onto the claimable queue.
//
// Promotion is safe to run more than once for the same task: re-saving
// the DB record with State "WAITING" is a plain overwrite, and
// queue.AddTask upserts by task id, so an overlapping tick (or a crash
// between the two calls, retried on the next tick since the task no
// longer matches the SCHEDULED filter... see the ordering note below)
// can't double-queue a task or leave it stuck.
//
// Order matters for crash safety: the DB record is flipped to WAITING
// before the queue add. If the process dies in between, claimTask's
// existing claim path still works correctly -- it always overwrites the
// claimed task's state unconditionally -- but the task would sit as
// WAITING-in-DB-only until a human noticed. Since AddTask is a cheap,
// side-effect-free upsert, we treat "already flipped to WAITING" as the
// point of no return and always attempt the queue add right after, rather
// than trying to make the two-step transition atomic across two separate
// storage abstractions (BlanketDB and BlanketQueue).
func (s *ServerConfig) promoteDueScheduledTasks(now time.Time) {
	smallest, largest := fullIdRange()
	due, _, err := s.DB.GetTasks(&database.TaskSearchConf{
		Limit:             s.maxScheduledLimit(),
		AllowedTaskStates: map[string]bool{"SCHEDULED": true},
		SmallestId:        smallest,
		LargestId:         largest,
	})
	if err != nil {
		log.WithField("err", err).Error("scheduler: error listing SCHEDULED tasks")
		return
	}

	for _, t := range due {
		if t.ScheduledTs > now.Unix() {
			continue
		}

		t.State = "WAITING"
		t.LastUpdatedTs = now.Unix()
		if err := s.DB.SaveTask(&t); err != nil {
			log.WithFields(log.Fields{"taskId": t.Id.Hex(), "err": err}).Error("scheduler: error promoting scheduled task")
			continue
		}
		if err := s.Q.AddTask(&t); err != nil {
			log.WithFields(log.Fields{"taskId": t.Id.Hex(), "err": err}).Error("scheduler: error queueing promoted task")
			continue
		}

		log.WithFields(log.Fields{"taskId": t.Id.Hex(), "type": t.TypeId}).Info("scheduler: promoted scheduled task to WAITING")
		s.TaskEvents.Notify()
	}
}

// fireDueRecurringTasks spawns one child task for each RECURRING template
// whose NextFireTs has passed, then advances the template's NextFireTs to
// its next cron occurrence.
func (s *ServerConfig) fireDueRecurringTasks(now time.Time) {
	smallest, largest := fullIdRange()
	templates, _, err := s.DB.GetTasks(&database.TaskSearchConf{
		Limit:             s.maxScheduledLimit(),
		AllowedTaskStates: map[string]bool{"RECURRING": true},
		SmallestId:        smallest,
		LargestId:         largest,
	})
	if err != nil {
		log.WithField("err", err).Error("scheduler: error listing RECURRING task templates")
		return
	}

	for i := range templates {
		tmpl := templates[i]
		if tmpl.NextFireTs > now.Unix() {
			continue
		}
		if err := s.fireOnce(&tmpl, now); err != nil {
			log.WithFields(log.Fields{"taskId": tmpl.Id.Hex(), "err": err}).Error("scheduler: error firing recurring task template")
		}
	}
}

// fireOnce spawns a single child run of tmpl (fresh id, own log/result
// dir, tmpl's type/env/tags, ParentId set to tmpl.Id) and advances tmpl's
// NextFireTs.
//
// Order matters here too: the child is created and queued first, and
// NextFireTs only advances after that succeeds. If the server dies
// mid-fire, the template is found due again on restart and fires an
// equivalent child a second time rather than silently skipping a fire --
// an at-least-once guarantee, not exactly-once. That's the right default
// for a task runner (a missed scheduled run is usually worse than an
// occasional duplicate one), but callers whose task type isn't idempotent
// should account for it.
func (s *ServerConfig) fireOnce(tmpl *tasks.Task, now time.Time) error {
	child, err := s.newTaskForType(tmpl.TypeId, tmpl.ExecEnv)
	if err != nil {
		return err
	}
	child.ParentId = tmpl.Id
	child.Tags = append([]string{}, tmpl.Tags...)

	if err := s.enqueueTask(context.Background(), &child); err != nil {
		return err
	}
	log.WithFields(log.Fields{
		"templateId": tmpl.Id.Hex(),
		"childId":    child.Id.Hex(),
		"type":       tmpl.TypeId,
	}).Info("scheduler: fired recurring task template")

	next, err := tasks.NextCronFire(tmpl.CronExpr, now)
	if err != nil {
		return err
	}
	tmpl.NextFireTs = next.Unix()
	tmpl.LastUpdatedTs = now.Unix()
	return s.DB.SaveTask(tmpl)
}
