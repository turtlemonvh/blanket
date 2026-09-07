package bolt

import (
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
	bolt "go.etcd.io/bbolt"
	"time"
)

const (
	BOLTDB_WORKER_BUCKET = "workers"
	BOLTDB_TASK_BUCKET   = "tasks"
	FAR_FUTURE_SECONDS   = int64(60 * 60 * 24 * 365 * 100)
)

var (
	IdBytes = lib.IdBytes
)

// Concrete functions
type BlanketBoltDB struct {
	db *bolt.DB
}

// NewBlanketBoltDB wraps an open bolt handle, creating the buckets a
// blanket database must have (`workers`, `tasks`, and — since
// turtlemonvh/blanket#23 phase 4 — `meta`) if any are missing.
//
// It deliberately does *not* check the schema version or run migrations:
// those can fail, and this signature cannot report that. Production goes
// through OpenBlanketBoltDB below, which does both; this remains the entry
// point for test fixtures and anything else holding a database it built
// itself.
func NewBlanketBoltDB(db *bolt.DB) database.BlanketDB {
	if err := ensureBuckets(db); err != nil {
		log.Fatal(err)
	}
	return &BlanketBoltDB{db}
}

// OpenBlanketBoltDB is the production entry point: it runs the whole
// open-time sequence (buckets, schema version, migration marker, mandatory
// backup, pending migrations, lock-holder record — see
// lib/bolt/migrations.go) and only then hands back a usable database.
//
// The error is worth propagating rather than fataling on, because several
// of its shapes have specific exit semantics: *ErrMigrationInProgress
// means "exit non-zero and wait for the binary swap", *ErrSchemaTooNew and
// *ErrMigrationIncomplete mean "stop and tell the operator what to run".
func OpenBlanketBoltDB(db *bolt.DB, opts *PrepareOptions) (database.BlanketDB, error) {
	if err := PrepareDatabase(db, opts); err != nil {
		return nil, err
	}
	return &BlanketBoltDB{db}, nil
}

// WORKERS

// Get all workers
func (DB *BlanketBoltDB) GetWorkers() ([]worker.WorkerConf, error) {
	var err error
	ws := []worker.WorkerConf{}

	err = DB.db.View(func(tx *bolt.Tx) error {
		var err error

		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return MakeBucketDNEError(BOLTDB_WORKER_BUCKET)
		}

		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			w := worker.WorkerConf{}
			err = json.Unmarshal(v, &w)
			if err != nil {
				return err
			}
			ws = append(ws, w)
		}
		return nil
	})

	return ws, err
}

func (DB *BlanketBoltDB) GetWorker(workerId objectid.ObjectId) (worker.WorkerConf, error) {
	w := worker.WorkerConf{}
	err := DB.db.View(func(tx *bolt.Tx) error {
		result, err := fetchWorkerBytes(workerId, tx)
		if err != nil {
			return err
		}
		return json.Unmarshal(result, &w)
	})
	return w, err
}

// UpdateWorker upserts a worker record, merging at the field level rather
// than overwriting the whole document (turtlemonvh/blanket#23 phase 1).
//
// Two parties write to a worker record and they own different fields:
//
//   - The **worker process** owns what it observes about itself: Pid,
//     Logfile, StartedTs, Tags, CheckInterval, Daemon. It sends these on
//     registration and whenever they change.
//   - The **server** owns lifecycle facts the worker must not contradict:
//     Stopped and LastHeardTs today; StoppedReason and the respawn fields
//     join them in phase 3/5.
//
// Before this merge existed, UpdateWorker wrote the caller's whole struct,
// so a worker re-registering (WorkerConf.MustRegister always sends
// Stopped=false) silently undid a `PUT /worker/:id/stop` that had landed a
// moment earlier — the worker would carry on claiming tasks after the
// operator had stopped it. That's a live bug independent of the upgrade
// work.
//
// The server-owned fields are only preserved when a record already exists;
// creating a worker straight from a struct (as the tests and the launch
// path do) still writes exactly what it was given.
func (DB *BlanketBoltDB) UpdateWorker(w *worker.WorkerConf) error {
	return DB.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return MakeBucketDNEError(BOLTDB_WORKER_BUCKET)
		}

		merged := *w
		if existing := b.Get(IdBytes(w.Id)); existing != nil {
			var current worker.WorkerConf
			if err := json.Unmarshal(existing, &current); err != nil {
				return err
			}
			// Server-owned fields survive a worker's own update.
			merged.Stopped = current.Stopped
			merged.LastHeardTs = current.LastHeardTs
			merged.StoppedReason = current.StoppedReason
			// Lost is server-owned too, but a re-registering worker is
			// itself evidence of life, so this is the one server-owned
			// field an update clears rather than preserves. Leaving it set
			// would strand the badge on a worker that plainly came back.
			merged.Lost = false
		}

		bts, err := json.Marshal(&merged)
		if err != nil {
			return err
		}
		if err := b.Put(IdBytes(w.Id), bts); err != nil {
			return err
		}
		// Hand the caller back what was actually stored, so it isn't
		// operating on a view the database just declined to accept.
		*w = merged
		return nil
	})
}

// StartWorker is the server-side counterpart to StopWorker: it clears the
// Stopped flag and bumps LastHeardTs in a single transaction.
//
// This is required by the field-level merge in UpdateWorker. A stopped
// worker's record keeps Stopped=true no matter what the worker itself
// sends, so restarting one (PUT /worker/:id/restart) has to be an explicit
// server-side act rather than something the relaunched worker does to
// itself by re-registering.
func (DB *BlanketBoltDB) StartWorker(workerId objectid.ObjectId) (worker.WorkerConf, error) {
	return ModifyWorkerInBoltTransaction(DB.db, &workerId, func(w *worker.WorkerConf) error {
		w.Stopped = false
		w.StoppedReason = ""
		w.Lost = false
		w.LastHeardTs = time.Now().Unix()
		return nil
	})
}

// HeartbeatWorker records that the server just heard from a worker
// (turtlemonvh/blanket#23 phase 3): LastHeardTs is stamped from the
// server's own clock and the Lost flag, if the reaper had set one, is
// cleared.
//
// The timestamp is deliberately not a parameter. Every staleness
// calculation in the reaper is `now - LastHeardTs`, and both halves have
// to come from the same clock or the answer measures the difference
// between two machines' idea of the time rather than whether the worker is
// alive. (Workers are same-host today, so the skew would be zero — but the
// reaper deletes state on the strength of this number, and "it happens to
// be the same clock" is not a property worth depending on.)
//
// Returns the updated record so the handler can answer with the worker's
// current Stopped flag: that round trip is what lets a drain take effect
// within one check interval instead of one poll of the whole config.
func (DB *BlanketBoltDB) HeartbeatWorker(workerId objectid.ObjectId) (worker.WorkerConf, error) {
	return ModifyWorkerInBoltTransaction(DB.db, &workerId, func(w *worker.WorkerConf) error {
		w.LastHeardTs = time.Now().Unix()
		w.Lost = false
		return nil
	})
}

// StopWorker atomically marks a worker as stopped and bumps its
// LastHeardTs in a single bolt transaction, avoiding the read-then-write
// race a separate GetWorker/UpdateWorker pair would have (e.g. a
// concurrent self-registration from the worker overwriting the stop with
// stale data, or vice versa).
func (DB *BlanketBoltDB) StopWorker(workerId objectid.ObjectId) (worker.WorkerConf, error) {
	return ModifyWorkerInBoltTransaction(DB.db, &workerId, func(w *worker.WorkerConf) error {
		w.Stopped = true
		// An explicit stop is its own explanation, and it overrides
		// whatever the reaper may have written earlier.
		w.StoppedReason = ""
		w.LastHeardTs = time.Now().Unix()
		return nil
	})
}

func (DB *BlanketBoltDB) DeleteWorker(workerId objectid.ObjectId) error {
	return DB.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return MakeBucketDNEError(BOLTDB_WORKER_BUCKET)
		}
		return b.Delete(IdBytes(workerId))
	})
}

// Tasks

func (DB *BlanketBoltDB) GetTask(taskId objectid.ObjectId) (tasks.Task, error) {
	var err error
	task := tasks.Task{}
	err = DB.db.View(func(tx *bolt.Tx) error {
		b, err := fetchTaskBucket(tx)
		if err != nil {
			return err
		}
		task, err = fetchTaskFromBucket(&taskId, b)
		return err
	})
	return task, err
}

func (DB *BlanketBoltDB) GetTasks(tc *database.TaskSearchConf) ([]tasks.Task, int, error) {
	return FindTasksInBoltDB(DB.db, BOLTDB_TASK_BUCKET, tc)
}

func (DB *BlanketBoltDB) DeleteTask(taskId objectid.ObjectId) error {
	return DB.db.Update(func(tx *bolt.Tx) error {
		b, err := fetchTaskBucket(tx)
		if err != nil {
			return err
		}
		return b.Delete(IdBytes(taskId))
	})
}

// progress is a number [0:100]
// Should also update task.LastUpdatedTs
func (DB *BlanketBoltDB) UpdateTaskProgress(taskId objectid.ObjectId, progress int) error {
	return ModifyTaskInBoltTransaction(DB.db, &taskId, func(t *tasks.Task) error {
		t.Progress = progress
		return nil
	})
}

// This will be called on a task pulled out of the queue
// Any task that, for any reason, happens to exist with the same id should be overwritten
func (DB *BlanketBoltDB) SaveTask(t *tasks.Task) error {
	// Just save in database
	return DB.db.Update(func(tx *bolt.Tx) error {
		bucket, err := fetchTaskBucket(tx)
		if err != nil {
			return err
		}
		return saveTaskToBucket(t, bucket)
	})
}

// runIdConflict reports whether a caller's fencing token contradicts the
// one already stored on a task.
//
// An empty token on either side is **legacy-permissive**: a worker built
// before turtlemonvh/blanket#23 phase 1 sends no token at all, and a task
// record written by one carries none, so a mixed-version install has to
// keep working. Only two tokens that are both present and different mean
// "two runners believe they own this task". This leniency is temporary —
// the schema bump in phase 4 is where the token becomes mandatory.
func runIdConflict(stored, incoming string) bool {
	return stored != "" && incoming != "" && stored != incoming
}

// isTerminalState reports whether a task has already reached one of
// tasks.ValidTerminalTaskStates.
func isTerminalState(state string) bool {
	for _, s := range tasks.ValidTerminalTaskStates {
		if state == s {
			return true
		}
	}
	return false
}

// RunTask moves a CLAIMED task to RUNNING, and is idempotent.
//
// The worker retries this call (turtlemonvh/blanket#23 phase 1), so it has
// to tolerate a replay after a lost response:
//
//   - CLAIMED: the normal transition. The caller's RunId is recorded and
//     becomes the task's fencing token for this run.
//   - RUNNING with a matching (or legacy-empty) RunId: a replay. The record
//     is left exactly as it is and the call succeeds — the caller already
//     got what it asked for, it just never heard so.
//   - RunId mismatch: ErrRunIdMismatch (409). Two runners; the caller must
//     not retry.
//   - Anything else, including a task a user already stopped:
//     ErrTaskStateConflict (409). Retrying can't fix it, and a user-issued
//     STOPPED must not be undone by a worker that started late.
func (DB *BlanketBoltDB) RunTask(taskId objectid.ObjectId, fields *database.TaskRunConfig) error {
	// Set lots of fields
	return ModifyTaskInBoltTransaction(DB.db, &taskId, func(t *tasks.Task) error {
		if runIdConflict(t.RunId, fields.RunId) {
			return fmt.Errorf("%w: task '%s' is running as '%s', caller claims '%s'",
				database.ErrRunIdMismatch, taskId.Hex(), t.RunId, fields.RunId)
		}

		switch t.State {
		case "CLAIMED":
			// Normal transition; fall through.
		case "RUNNING":
			// Idempotent replay of a transition already applied.
			log.WithFields(log.Fields{
				"taskId": taskId.Hex(),
				"runId":  t.RunId,
			}).Debug("ignoring repeated run transition for a task already RUNNING")
			return nil
		default:
			return fmt.Errorf("%w: task found in state '%s', expected 'CLAIMED'",
				database.ErrTaskStateConflict, t.State)
		}

		t.State = "RUNNING"
		t.Progress = 0
		t.Timeout = int64(fields.Timeout)
		t.LastUpdatedTs = int64(fields.LastUpdatedTs)
		t.Pid = fields.Pid
		t.TypeDigest = fields.TypeDigest
		t.RunId = fields.RunId
		return nil
	})
}

// Set task to a terminal state
// Checks that task is currently in an eligible source state
// Sets progress to 100 if the state is SUCCESS
//
// RECURRING and PAUSED are eligible sources (in addition to the original
// RUNNING/WAITING/SCHEDULED) so PUT /task/:id/cancel can stop a recurring
// template -- see cancelTaskById (server/serve_tasks.go) and
// docs/task_flow.md's Scheduling section. Cancelling a template clears
// PausedTs implicitly by leaving it as-is on the now-STOPPED record; it's
// only meaningful while State == "PAUSED".
// Idempotency (turtlemonvh/blanket#23 phase 1): the worker retries this
// call, and the first terminal state a task reaches is the one that
// sticks. A repeat — the same state again, or a late ERROR/SUCCESS
// arriving after a user's STOPPED — is accepted as a no-op rather than
// rejected, because rejecting it is what strands a task: the worker sees
// an error, retries into the same rejection, and eventually gives up
// having reported nothing. A RunId that contradicts the stored one is the
// one case that is still refused, with ErrRunIdMismatch.
//
// A no-op repeat deliberately leaves every stored field alone, including
// the ExitCode a first report may already have written
// (turtlemonvh/blanket#27) — a retry must never downgrade a recorded exit
// status to "unknown".
//
// CLAIMED is an eligible source state (turtlemonvh/blanket#23 phase 3).
// There are two ways a task reaches a terminal state without ever having
// been RUNNING, and both were rejected before: the worker's own
// "cmd.Start() failed, report ERROR" path, which runs before the RUNNING
// transition, and the reaper failing a task that has exhausted its
// requeues. Refusing them left the task CLAIMED forever, which is the
// stranding this whole issue is about.
func (DB *BlanketBoltDB) FinishTask(taskId objectid.ObjectId, fields *database.TaskFinishConfig) error {
	// Set lots of fields
	return ModifyTaskInBoltTransaction(DB.db, &taskId, func(t *tasks.Task) error {
		if runIdConflict(t.RunId, fields.RunId) {
			return fmt.Errorf("%w: task '%s' is running as '%s', caller claims '%s'",
				database.ErrRunIdMismatch, taskId.Hex(), t.RunId, fields.RunId)
		}

		if isTerminalState(t.State) {
			// First terminal state wins. In particular a user-issued
			// STOPPED beats the ERROR the worker reports a moment later
			// when the killed child process exits.
			if t.State != fields.State {
				log.WithFields(log.Fields{
					"taskId":    taskId.Hex(),
					"state":     t.State,
					"reported":  fields.State,
					"runId":     t.RunId,
					"callerRun": fields.RunId,
				}).Info("ignoring late finish transition; task already reached a terminal state")
			}
			return nil
		}

		switch t.State {
		case "RUNNING", "WAITING", "SCHEDULED", "RECURRING", "PAUSED", "CLAIMED":
		default:
			return fmt.Errorf("Task found in unexpected state; found '%s', expected one of 'RUNNING', 'WAITING', 'SCHEDULED', 'RECURRING', 'PAUSED', or 'CLAIMED'", t.State)
		}
		t.State = fields.State
		if t.State == "SUCCESS" {
			t.Progress = 100
		}
		// Only overwrite the exit code when the caller actually has one;
		// a cancel has no process to report on and shouldn't clobber an
		// exit code a racing finish already wrote.
		if fields.ExitCode != nil {
			t.ExitCode = fields.ExitCode
		}
		t.LastUpdatedTs = time.Now().Unix()
		return nil
	})
}
