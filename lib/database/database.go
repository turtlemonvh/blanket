package database

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cast"
	"github.com/turtlemonvh/blanket/lib"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
	"strconv"
	"strings"
	"time"
)

/*

- Define interface
- Use interface methods in all requests
- Write tests

NOTES:
- All databases must use bson primary keys assigned by the application

FIXME:
- atomic updates to certain worker fields so whole object isn't overwritten each time
	- could do this with a hash of the object in a generic way, but that requires transactions, which mongo doesn't have
- add "not found" errors
- make sure bolt is only referenced here and in the queue file
- will need to query queue for any tasks in WAITING state
- add specific functions for heartbeats, since they need to check if they're getting killed AND set a value
	- we don't want either thing (heartbeat or kill message) to overwrite what is there
	- whenever we set a single field, we need isolation (like for updateTaskProgress)

*/

type BlanketDB interface {
	// Worker functions
	GetWorkers() ([]worker.WorkerConf, error)
	GetWorker(workerId objectid.ObjectId) (worker.WorkerConf, error)
	DeleteWorker(workerId objectid.ObjectId) error
	UpdateWorker(worker *worker.WorkerConf) error
	// StopWorker atomically marks a worker as stopped and bumps its
	// LastHeardTs, returning the updated record. Atomic in the sense that
	// the read, mutate, and write happen inside a single transaction —
	// unlike UpdateWorker, which requires the caller to already hold the
	// full desired state and simply overwrites the record.
	//
	// reason is recorded as StoppedReason and decides what happens to a
	// pending respawn intent (turtlemonvh/blanket#23 phase 5):
	// worker.StopReasonSelf preserves it, because a drained worker exiting
	// is the drain working; anything else — an operator's stop, which
	// sends no reason at all — clears it, because an explicit decision to
	// take a worker down mid-restart has to outrank the restart's plan to
	// bring it back.
	StopWorker(workerId objectid.ObjectId, reason string) (worker.WorkerConf, error)
	// StartWorker is StopWorker's counterpart: it clears Stopped and bumps
	// LastHeardTs atomically. Needed because UpdateWorker no longer lets a
	// worker clear its own Stopped flag by re-registering; see the
	// field-level merge documented on the bolt implementation.
	StartWorker(workerId objectid.ObjectId) (worker.WorkerConf, error)
	// HeartbeatWorker records that the server just heard from a worker:
	// it stamps LastHeardTs from the *server's* clock and clears Lost,
	// returning the updated record so the handler can answer with the
	// worker's current Stopped flag (turtlemonvh/blanket#23 phase 3).
	//
	// Deliberately takes no timestamp argument. A worker-supplied one
	// would make every staleness calculation in the reaper a measure of
	// the worker's clock skew rather than its liveness.
	HeartbeatWorker(workerId objectid.ObjectId) (worker.WorkerConf, error)
	// CleanupStalledWorkers marks or stops workers that have stopped
	// heartbeating; see ReapOptions for why it takes one.
	CleanupStalledWorkers(opts *ReapOptions) (ReapReport, error)
	// Task functions
	GetTask(taskId objectid.ObjectId) (tasks.Task, error)
	DeleteTask(taskId objectid.ObjectId) error
	GetTasks(tc *TaskSearchConf) ([]tasks.Task, int, error)
	SaveTask(t *tasks.Task) error
	RunTask(taskId objectid.ObjectId, fields *TaskRunConfig) error
	FinishTask(taskId objectid.ObjectId, fields *TaskFinishConfig) error
	UpdateTaskProgress(taskId objectid.ObjectId, progress int) error
	// CleanupStalledTasks recovers or requeues tasks whose worker went
	// away; see ReapOptions.
	CleanupStalledTasks(opts *ReapOptions) (ReapReport, error)

	// Meta functions (turtlemonvh/blanket#23 phase 4). These read and
	// write the `meta` bucket, which describes the installation rather
	// than the work in it; see lib/database/meta.go.
	//
	// SchemaVersion reports the schema the file on disk is written in.
	// An unstamped database reads as InitialSchemaVersion.
	SchemaVersion() (int, error)
	// SetServerInstance records which server process currently owns this
	// database and when it started.
	SetServerInstance(ServerInstance) error
	// ServerInstance returns the last recorded pair. A database no server
	// has opened yet returns the zero value and no error.
	ServerInstance() (ServerInstance, error)
	// LockHolder returns the process recorded as holding the bolt lock.
	// Note that reading this through an open handle can only ever return
	// *your own* record: see bolt.ReadLockHolderSidecar for the one a
	// process locked *out* has to use.
	LockHolder() (LockHolder, error)
	// ClearLockHolder is called on a clean close. A record left behind is
	// how a crash is told from a shutdown.
	ClearLockHolder() error
	// MigrationMarker returns the in-flight migration marker, or nil when
	// none is set.
	MigrationMarker() (*MigrationMarker, error)
	// RestartRecord reads the restart state machine's server-owned record;
	// an absent one decodes to the zero value, which reads as IDLE.
	RestartRecord() (RestartRecord, error)
	// SetRestartRecord overwrites the record wholesale. Only boot-time
	// reconciliation and tests use it — a *transition* goes through
	// UpdateRestartRecord, which reads and writes in one transaction.
	SetRestartRecord(RestartRecord) error
	// UpdateRestartRecord applies fn to the stored record and writes the
	// result back in a single transaction. fn returning an error aborts
	// the write, so a rejected transition leaves the record untouched.
	// Setting the state to IDLE deletes the record.
	UpdateRestartRecord(fn func(*RestartRecord) error) (RestartRecord, error)
	// StopWorkersForRestart is the transaction the restart machine's
	// central invariant is about (turtlemonvh/blanket#23 phase 5): it
	// applies fn to the restart record *and* stops every running worker
	// with a respawn intent, in one commit. Returns the updated record and
	// the workers it stopped, so the caller can signal those processes
	// afterwards — outside the transaction, since no database write can be
	// made to agree with a signal.
	StopWorkersForRestart(reason string, fn func(*RestartRecord) error) (RestartRecord, []worker.WorkerConf, error)
	// ClaimWorkerRespawn clears one worker's Stopped flag, counts the
	// attempt and stamps the clock, in one transaction immediately before
	// the server spawns it. Counting before the fork rather than after is
	// what makes the generation cap survive a spawn that kills the server.
	ClaimWorkerRespawn(workerId objectid.ObjectId) (worker.WorkerConf, error)
	// ClearWorkerRespawn drops a respawn intent. An empty reason means the
	// spawn succeeded; a non-empty one records why the server gave up and
	// leaves the worker stopped.
	ClearWorkerRespawn(workerId objectid.ObjectId, reason string) (worker.WorkerConf, error)

	// Backup writes a consistent copy of the database into dir and
	// returns the path written, after a free-space precheck and before
	// pruning older backups down to the retention count. Consistent in
	// the strong sense: it is taken inside a read transaction, so it is a
	// point-in-time image even while the server is serving.
	Backup(dir string) (string, error)
}

var (
	IdBytes = lib.IdBytes
)

const (
	FAR_FUTURE_SECONDS = int64(60 * 60 * 24 * 365 * 100)
)

// FIXME: will have to capitalize all these since used outside this module
type TaskSearchConf struct {
	JustCounts        bool
	JustUnclaimed     bool
	Limit             int
	Offset            int
	ReverseSort       bool
	RequiredTags      []string
	MaxTags           []string
	SmallestId        objectid.ObjectId
	LargestId         objectid.ObjectId
	AllowedTaskStates map[string]bool
	AllowedTaskTypes  map[string]bool
	// FilterParentId, when true, restricts results to tasks whose
	// ParentId equals ParentId -- e.g. GET /task/?parentId=<id> lists a
	// RECURRING/PAUSED/STOPPED series' past child runs. False (the
	// zero value) means "no parentId filter", not "filter for a zero
	// ParentId".
	FilterParentId bool
	ParentId       objectid.ObjectId
}

type ItemNotFoundError string

func (e ItemNotFoundError) Error() string {
	return fmt.Sprintf("Item not found: %s", string(e))
}

// parseFilterTime parses a user-supplied date/time in one of several formats
// and falls back to unix seconds. Returns (time, true) on success.
// Supported shapes: unix int, RFC3339, "2006-01-02T15:04" (datetime-local),
// "2006-01-02", "2006/01/02 15:04" (legacy Angular UI format).
func parseFilterTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(sec, 0), true
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04",
		"2006-01-02",
		"2006/01/02 15:04",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// collectCSV reads all values for a query key (supports both `k=a,b` and
// `k=a&k=b`), splits on commas, trims, and drops empties.
func collectCSV(c *gin.Context, key string) []string {
	out := []string{}
	for _, raw := range c.QueryArray(key) {
		for _, v := range strings.Split(raw, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// Create a search configuration object out of a request context
func TaskSearchConfFromContext(c *gin.Context) *TaskSearchConf {
	tc := &TaskSearchConf{}

	tc.JustCounts = c.Query("count") == "true"

	tc.Limit = cast.ToInt(c.Query("limit"))
	tc.Offset = cast.ToInt(c.Query("offset"))

	// Default values for limit and offset
	if tc.Limit < 1 {
		tc.Limit = 500
	}
	if tc.Offset < 0 {
		tc.Offset = 0
	}

	tc.ReverseSort = c.Query("reverseSort") == "true"

	// Dates accept unix-seconds or any of the human layouts in parseFilterTime.
	startTime := time.Unix(0, 0)
	endTime := time.Unix(FAR_FUTURE_SECONDS, 0)
	if t, ok := parseFilterTime(c.Query("createdAfter")); ok {
		startTime = t
	}
	if t, ok := parseFilterTime(c.Query("createdBefore")); ok {
		endTime = t
	}
	tc.SmallestId = objectid.NewObjectIdWithTime(startTime)
	tc.LargestId = objectid.NewObjectIdWithTime(endTime)

	tc.RequiredTags = collectCSV(c, "requiredTags")
	tc.MaxTags = collectCSV(c, "maxTags")

	tc.AllowedTaskStates = make(map[string]bool)
	for _, s := range collectCSV(c, "states") {
		tc.AllowedTaskStates[s] = true
	}

	tc.AllowedTaskTypes = make(map[string]bool)
	for _, s := range collectCSV(c, "types") {
		tc.AllowedTaskTypes[s] = true
	}

	if pid := strings.TrimSpace(c.Query("parentId")); pid != "" && objectid.IsObjectIdHex(pid) {
		tc.FilterParentId = true
		tc.ParentId = objectid.ObjectIdHex(pid)
	}

	return tc
}

type TaskRunConfig struct {
	Timeout       int
	LastUpdatedTs int64
	Pid           int
	TypeDigest    string
	// RunId is the worker's fencing token for this execution attempt; see
	// tasks.Task.RunId. Empty means the caller is a worker predating the
	// token and is handled leniently (see RunTask).
	RunId string
}

// TaskFinishConfig carries everything PUT /task/:id/finish needs. It's an
// options struct rather than a bare state string — mirroring TaskRunConfig
// above — so the terminal transition can grow fields without churning the
// BlanketDB interface again. Two features added fields at once: the
// fencing token from turtlemonvh/blanket#23 phase 1 and the child
// process's exit code from turtlemonvh/blanket#27.
type TaskFinishConfig struct {
	// State is the terminal state to move the task to; one of
	// tasks.ValidTerminalTaskStates.
	State string
	// RunId is the worker's fencing token for the run being reported.
	// Empty is legacy-permissive; see RunTask/FinishTask.
	RunId string
	// ExitCode is the process exit status, when it's known. nil (the
	// zero value) leaves the task's ExitCode untouched — which is the
	// right behavior for the paths that have no process to report on
	// (PUT /task/:id/cancel, a task stopped before it ever ran) and for
	// an idempotent repeat finish, which must not clobber the exit code
	// the first report already stored.
	ExitCode *int
}

// FinishState is the common case: a terminal state with no exit code and
// no fencing token to record.
func FinishState(newState string) *TaskFinishConfig {
	return &TaskFinishConfig{State: newState}
}

// Errors that describe *why* a task transition was refused, so HTTP
// handlers can map them to a status code without string matching.
var (
	// ErrRunIdMismatch means the caller's fencing token doesn't match the
	// one stored on the task: two runners believe they own it. The caller
	// must not retry — this maps to 409.
	ErrRunIdMismatch = errors.New("task is owned by a different run (runId mismatch)")

	// ErrTaskStateConflict means the task exists but is not in a state this
	// transition can be applied from, in a way retrying will never fix
	// (e.g. asking to start a task a user has already stopped). Maps to
	// 409.
	ErrTaskStateConflict = errors.New("task is not in a state this transition can be applied from")
)
