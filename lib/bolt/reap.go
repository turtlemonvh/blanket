package bolt

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/queue"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
	bolt "go.etcd.io/bbolt"
)

/*

The reapers (turtlemonvh/blanket#23 phase 3).

These three functions were `// FIXME: Implement me` stubs returning nil
since the beginning of the project, and nothing scheduled them. They are
what reconciles the state a crash leaves behind: a worker that was killed,
a task whose worker died mid-run, a queue entry whose claim was never
acked.

The governing rule, from the design review, is that **a false positive
here destroys real work**. Rewriting a finished three-hour render as ERROR
is worse than leaving it RUNNING forever, because the first is silent and
the second is visible. So every decision below fails safe toward "alive":

  - Nothing acts on an inconclusive liveness answer (lib/proclive returns
    one explicitly rather than guessing).
  - A stale heartbeat alone never rewrites a task. It marks a worker Lost
    for the UI, and that is all.
  - A task's outcome is *read* from the worker's outcome journal, never
    inferred. No journal and no other conclusive evidence means the task
    is left exactly as it is, and the pass logs that it did so.
  - A worker record is never deleted. Stopping it is enough to free its
    tasks, and the record keeps the operator's link to its logs.

Every input -- the clock, pid liveness, the journal reader, the requeue
callback -- arrives in a database.ReapOptions, which is what makes each
row of the table testable with no processes, no sleeping, and no
filesystem.

The decision table itself is documented for users in docs/task_flow.md
("The reaper"); keep the two in sync.

*/

// reapScanLimit bounds how many candidate rows one pass examines, the same
// way scheduler.maxScheduled bounds the scheduler's own queries. A pass
// that finds more than this simply picks the rest up next time: the reaper
// is a background reconciler, and an unbounded scan holding a bolt
// transaction open is a worse failure than a slow convergence.
const reapScanLimit = 5000

// fullIdRange is the id span "every task", for the bounded queries below.
func fullIdRange() (objectid.ObjectId, objectid.ObjectId) {
	return objectid.NewObjectIdWithTime(time.Unix(0, 0)),
		objectid.NewObjectIdWithTime(time.Unix(database.FAR_FUTURE_SECONDS, 0))
}

// CleanupStalledWorkers marks or stops workers that have stopped
// heartbeating.
//
// The decision, in full:
//
//	heartbeat fresh                     -> nothing
//	stale + pid conclusively alive      -> Lost (UI only). Never stopped,
//	                                       never deleted, tasks untouched.
//	stale + pid conclusively dead       -> Lost + Stopped, with a reason
//	stale + liveness inconclusive       -> Lost; Stopped only once the
//	                                       staleness passes the *longer*
//	                                       WorkerDeadAfter threshold
//
// The last row is the same-host caveat made concrete. Pid liveness only
// works for a process on this machine; wherever it cannot answer -- an
// unsupported platform, a record with no PidStartTs, a future off-host
// worker -- the reaper falls back to heartbeat staleness alone, and
// demands much more of it before acting, because it is much weaker
// evidence.
//
// Stopping rather than deleting is deliberate. Deleting loses the link to
// the worker's logfile just when somebody wants to read it, and Stopped is
// already enough for CleanupStalledTasks to treat the worker's tasks as
// orphaned.
func (DB *BlanketBoltDB) CleanupStalledWorkers(opts *database.ReapOptions) (database.ReapReport, error) {
	opts = opts.WithDefaults()
	report := database.ReapReport{}

	ws, err := DB.GetWorkers()
	if err != nil {
		return report, err
	}

	for i := range ws {
		w := ws[i]
		if w.Stopped {
			// Already out of the claim loop; an operator (or an earlier
			// pass) has dealt with it.
			continue
		}

		staleness := opts.StaleFor(w.LastHeardTs)
		if !database.Exceeds(staleness, opts.WorkerStaleAfter) {
			continue
		}

		alive, conclusive := opts.IsAlive(w.Pid, w.PidStartTs)

		stop := false
		reason := ""
		switch {
		case alive && conclusive:
			// The process is demonstrably there and demonstrably ours. It
			// has stopped talking to us -- wedged, paused, or unable to
			// reach the server -- which is worth surfacing and nothing
			// more. Killing it could orphan a running task's child.
			reason = ""
		case !alive && conclusive:
			stop = true
			reason = "reaper: worker process is gone"
		default:
			// Inconclusive. Only heartbeat staleness is left, so it has to
			// be much staler before this counts as evidence.
			if database.Exceeds(staleness, opts.WorkerDeadAfter) {
				stop = true
				reason = "reaper: no heartbeat and pid liveness is inconclusive"
			}
		}

		if w.Lost && !stop {
			// Already marked; nothing changes.
			continue
		}

		updated, err := ModifyWorkerInBoltTransaction(DB.db, &w.Id, func(cur *worker.WorkerConf) error {
			cur.Lost = true
			if stop {
				cur.Stopped = true
				cur.StoppedReason = reason
			}
			return nil
		})
		if err != nil {
			// A worker deleted between the scan and this write is not an
			// error worth failing the pass over.
			log.WithFields(log.Fields{
				"workerId": w.Id.Hex(),
				"err":      err.Error(),
			}).Warn("reaper: could not update a stalled worker")
			continue
		}

		if !w.Lost {
			report.WorkersMarkedLost++
		}
		if stop {
			report.WorkersStopped++
		}
		log.WithFields(log.Fields{
			"workerId":  updated.Id.Hex(),
			"pid":       updated.Pid,
			"staleness": staleness.String(),
			"stopped":   stop,
			"reason":    reason,
		}).Warn("reaper: worker has stopped heartbeating")
	}

	return report, nil
}

// CleanupStalledTasks recovers tasks whose worker went away.
//
// The order of checks is the whole design: **read the journal before
// guessing**, and stop as soon as the evidence runs out.
//
//	journal exited/reported -> apply the real outcome through the same
//	                           idempotent FinishTask path the worker
//	                           uses, carrying the journal's runId
//	journal running, child alive or inconclusive -> leave alone. A task's
//	                           child can outlive its worker, and killing
//	                           it (or recording an outcome for it) while
//	                           it is still writing output is the worst
//	                           thing this code could do.
//	journal running, child conclusively dead -> leave alone, and log. The
//	                           child died without recording an outcome, so
//	                           there is no outcome to recover; anything
//	                           written here would be a guess. An operator
//	                           can cancel it.
//	no journal, CLAIMED, no pid, worker conclusively gone -> the task
//	                           provably never started, so requeue it,
//	                           subject to the requeue cap.
//	anything else            -> leave alone, and log.
func (DB *BlanketBoltDB) CleanupStalledTasks(opts *database.ReapOptions) (database.ReapReport, error) {
	opts = opts.WithDefaults()
	report := database.ReapReport{}

	smallest, largest := fullIdRange()
	candidates, _, err := FindTasksInBoltDB(DB.db, BOLTDB_TASK_BUCKET, &database.TaskSearchConf{
		Limit:             reapScanLimit,
		SmallestId:        smallest,
		LargestId:         largest,
		AllowedTaskStates: map[string]bool{"CLAIMED": true, "RUNNING": true},
	})
	if err != nil {
		return report, err
	}

	for i := range candidates {
		t := candidates[i]

		staleness := opts.StaleFor(t.LastUpdatedTs)
		if !database.Exceeds(staleness, opts.TaskStaleAfter) {
			continue
		}

		j, jerr := opts.ReadJournal(t.ResultDir)
		switch {
		case jerr == nil && j != nil:
			if !DB.applyJournal(&t, j, opts, &report) {
				report.LeftAlone++
			}
		case t.State == "CLAIMED" && t.Pid == 0 && DB.workerIsGone(t.WorkerId, opts):
			// Claimed, never started (no journal is written until after
			// cmd.Start(), and no pid was ever recorded), and the worker
			// that claimed it is not coming back.
			DB.requeueOrFail(&t, opts, &report)
		default:
			log.WithFields(log.Fields{
				"taskId":    t.Id.Hex(),
				"state":     t.State,
				"staleness": staleness.String(),
				"journal":   jerrString(jerr),
			}).Info("reaper: stale task left alone; no conclusive evidence of its outcome")
			report.LeftAlone++
		}
	}

	return report, nil
}

// applyJournal acts on a task that has an outcome journal. Reports whether
// it did anything.
func (DB *BlanketBoltDB) applyJournal(t *tasks.Task, j *worker.OutcomeJournal, opts *database.ReapOptions, report *database.ReapReport) bool {
	// A journal describing a different attempt says nothing about this
	// one. (Empty on either side is legacy-permissive, matching the
	// server's own fencing-token rule.)
	if runIdConflict(t.RunId, j.RunId) {
		log.WithFields(log.Fields{
			"taskId":       t.Id.Hex(),
			"taskRunId":    t.RunId,
			"journalRunId": j.RunId,
		}).Info("reaper: outcome journal describes a different run; leaving the task alone")
		return false
	}

	switch j.State {
	case worker.OutcomeStateExited, worker.OutcomeStateReported:
		// The outcome is known and was never recorded. This is the case
		// the journal exists for.
		state := "SUCCESS"
		if j.ExitCode != 0 {
			state = "ERROR"
		}
		// The journal's -1 means "killed by a signal", which the task
		// record spells as a null exitCode -- there is no exit status of
		// the task's own to report.
		var exitCode *int
		if j.ExitCode >= 0 {
			code := j.ExitCode
			exitCode = &code
		}

		if err := DB.FinishTask(t.Id, &database.TaskFinishConfig{
			State:    state,
			RunId:    j.RunId,
			ExitCode: exitCode,
		}); err != nil {
			log.WithFields(log.Fields{
				"taskId": t.Id.Hex(),
				"err":    err.Error(),
			}).Warn("reaper: could not apply a recovered outcome")
			return false
		}

		log.WithFields(log.Fields{
			"taskId":   t.Id.Hex(),
			"state":    state,
			"exitCode": j.ExitCode,
			"runId":    j.RunId,
		}).Warn("reaper: recovered a task outcome from its worker's journal")
		report.TasksRecovered++
		return true

	case worker.OutcomeStateRunning:
		alive, conclusive := opts.IsAlive(j.Pid, j.PidStartTs)
		log.WithFields(log.Fields{
			"taskId":     t.Id.Hex(),
			"childPid":   j.Pid,
			"childAlive": alive,
			"conclusive": conclusive,
		}).Info("reaper: task's worker is gone but its outcome is unknown; leaving the task alone")
		return false

	default:
		return false
	}
}

// workerIsGone reports whether a task's worker is conclusively not coming
// back: its record has been deleted, or its process is conclusively dead.
//
// A worker merely marked Stopped is *not* enough on its own -- a stopping
// worker finishes its current task first -- so the pid still has to say so.
func (DB *BlanketBoltDB) workerIsGone(workerId objectid.ObjectId, opts *database.ReapOptions) bool {
	if workerId.IsZero() {
		return false
	}

	w, err := DB.GetWorker(workerId)
	if err != nil {
		var notFound database.ItemNotFoundError
		// The record is gone, so nothing will ever report for this task.
		return errors.As(err, &notFound)
	}

	alive, conclusive := opts.IsAlive(w.Pid, w.PidStartTs)
	return conclusive && !alive
}

// requeueOrFail puts a task that never started back on the queue, or fails
// it once it has used up its requeue budget.
//
// The cap is a poison-task guard. A task that kills whichever worker
// claims it -- an OOM, a driver that panics the box -- would otherwise be
// requeued forever, taking down every worker in turn, and the requeues
// themselves are the only trace. Failing it after MaxRequeues attempts
// makes it visible and stops the cycle.
func (DB *BlanketBoltDB) requeueOrFail(t *tasks.Task, opts *database.ReapOptions, report *database.ReapReport) {
	if opts.MaxRequeues > 0 && t.RequeueCount >= opts.MaxRequeues {
		if err := DB.FinishTask(t.Id, &database.TaskFinishConfig{State: "ERROR"}); err != nil {
			log.WithFields(log.Fields{
				"taskId": t.Id.Hex(),
				"err":    err.Error(),
			}).Warn("reaper: could not fail a task that exhausted its requeues")
			return
		}
		log.WithFields(log.Fields{
			"taskId":        t.Id.Hex(),
			"requeueCount":  t.RequeueCount,
			"requeueCapKey": "reaper.maxRequeues",
		}).Warn("reaper: task claimed and lost too many times; failing it instead of requeueing")
		report.TasksFailed++
		return
	}

	if opts.Requeue == nil {
		report.LeftAlone++
		return
	}

	// Reset the record to a claimable state first: if the process dies
	// between this write and the queue add, the task is a WAITING row that
	// is not in the queue -- recoverable and inert -- whereas the other
	// order would leave a claimable queue entry pointing at a task still
	// marked CLAIMED by a dead worker.
	var requeued tasks.Task
	err := ModifyTaskInBoltTransaction(DB.db, &t.Id, func(cur *tasks.Task) error {
		cur.State = "WAITING"
		cur.WorkerId = objectid.ObjectId{}
		cur.Pid = 0
		// A fresh attempt gets a fresh fencing token; the old one belonged
		// to a run that never happened.
		cur.RunId = ""
		cur.RequeueCount++
		requeued = *cur
		return nil
	})
	if err != nil {
		log.WithFields(log.Fields{
			"taskId": t.Id.Hex(),
			"err":    err.Error(),
		}).Warn("reaper: could not reset a task for requeue")
		return
	}

	if err := opts.Requeue(&requeued); err != nil {
		log.WithFields(log.Fields{
			"taskId": t.Id.Hex(),
			"err":    err.Error(),
		}).Warn("reaper: could not add a reset task back to the queue")
		return
	}

	log.WithFields(log.Fields{
		"taskId":       t.Id.Hex(),
		"requeueCount": requeued.RequeueCount,
		"workerId":     t.WorkerId.Hex(),
	}).Warn("reaper: requeued a task whose worker died before it started")
	report.TasksRequeued++
}

// jerrString renders a journal-read error for a log field without making
// "there is no journal" look like a failure.
func jerrString(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, os.ErrNotExist), errors.Is(err, database.ErrNoJournal):
		return "none"
	default:
		return err.Error()
	}
}

// CleanupUnclaimedTasks reconciles queue entries whose claim was never
// acked.
//
// ClaimTask stamps a queue entry with the claiming worker's id and only
// deletes it once the claim is acked. An entry left stamped is one of two
// completely different situations, and telling them apart needs a
// cross-bucket read -- which is why the test fixtures had to be fixed to
// share one database file before this could be written at all:
//
//	the task exists in the database at CLAIMED or beyond
//	    -> the claim really happened and only the ack was lost. Drop the
//	       queue entry. Re-running it would duplicate work that may
//	       already have had side effects.
//	the task is missing, or still WAITING/SCHEDULED
//	    -> the claim never completed. Clear the worker id so somebody else
//	       can pick it up.
func (Q *BlanketBoltQueue) CleanupUnclaimedTasks(opts *database.ReapOptions) (database.ReapReport, error) {
	opts = opts.WithDefaults()
	report := database.ReapReport{}
	nowTs := opts.NowTs()

	err := Q.db.Update(func(tx *bolt.Tx) error {
		qb, err := fetchTaskQueueBucket(tx)
		if err != nil {
			return err
		}
		tb, err := fetchTaskBucket(tx)
		if err != nil {
			return err
		}

		type pending struct {
			id   objectid.ObjectId
			drop bool
			task tasks.Task
		}
		var actions []pending

		scanned := 0
		c := qb.Cursor()
		for k, v := c.First(); k != nil && scanned < reapScanLimit; k, v = c.Next() {
			scanned++

			qt := tasks.Task{}
			if err := json.Unmarshal(v, &qt); err != nil {
				continue
			}
			if qt.WorkerId.IsZero() {
				// Unclaimed and waiting to be claimed: the normal case.
				continue
			}
			if !database.Exceeds(opts.StaleFor(qt.LastUpdatedTs), opts.TaskStaleAfter) {
				continue
			}

			claimLanded := false
			if stored := tb.Get(IdBytes(qt.Id)); stored != nil {
				dbTask := tasks.Task{}
				if err := json.Unmarshal(stored, &dbTask); err == nil {
					switch dbTask.State {
					case "WAITING", "SCHEDULED", "RECURRING", "PAUSED":
					default:
						claimLanded = true
					}
				}
			}

			actions = append(actions, pending{id: qt.Id, drop: claimLanded, task: qt})
		}

		// Mutating the bucket while its cursor is live is undefined in
		// bbolt, so the writes happen after the scan.
		for _, a := range actions {
			if a.drop {
				if err := qb.Delete(IdBytes(a.id)); err != nil {
					return err
				}
				report.QueueEntriesDropped++
				log.WithFields(log.Fields{
					"taskId": a.id.Hex(),
				}).Info("reaper: dropped a queue entry whose claim landed but was never acked")
				continue
			}

			freed := a.task
			freed.WorkerId = objectid.ObjectId{}
			freed.LastUpdatedTs = nowTs
			bts, err := json.Marshal(&freed)
			if err != nil {
				return err
			}
			if err := qb.Put(IdBytes(a.id), bts); err != nil {
				return err
			}
			report.QueueEntriesFreed++
			log.WithFields(log.Fields{
				"taskId":   a.id.Hex(),
				"workerId": a.task.WorkerId.Hex(),
			}).Warn("reaper: released a queue entry whose claim never completed")
		}
		return nil
	})

	return report, err
}

// Compile-time proof that the two concrete types still satisfy the
// interfaces after the signature change.
var (
	_ database.BlanketDB = (*BlanketBoltDB)(nil)
	_ queue.BlanketQueue = (*BlanketBoltQueue)(nil)
)
