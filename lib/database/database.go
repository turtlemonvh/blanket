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
	StopWorker(workerId objectid.ObjectId) (worker.WorkerConf, error)
	// StartWorker is StopWorker's counterpart: it clears Stopped and bumps
	// LastHeardTs atomically. Needed because UpdateWorker no longer lets a
	// worker clear its own Stopped flag by re-registering; see the
	// field-level merge documented on the bolt implementation.
	StartWorker(workerId objectid.ObjectId) (worker.WorkerConf, error)
	CleanupStalledWorkers() error
	// Task functions
	GetTask(taskId objectid.ObjectId) (tasks.Task, error)
	DeleteTask(taskId objectid.ObjectId) error
	GetTasks(tc *TaskSearchConf) ([]tasks.Task, int, error)
	SaveTask(t *tasks.Task) error
	RunTask(taskId objectid.ObjectId, fields *TaskRunConfig) error
	FinishTask(taskId objectid.ObjectId, fields *TaskFinishConfig) error
	UpdateTaskProgress(taskId objectid.ObjectId, progress int) error
	CleanupStalledTasks() error
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
// options struct rather than a bare state string so the terminal
// transition can grow fields without another signature change — the
// fencing token added in turtlemonvh/blanket#23 phase 1 is the first, and
// turtlemonvh/blanket#27 adds the child process's exit code next.
type TaskFinishConfig struct {
	// State is the terminal state to move the task to; one of
	// tasks.ValidTerminalTaskStates.
	State string
	// RunId is the worker's fencing token for the run being reported.
	// Empty is legacy-permissive; see RunTask/FinishTask.
	RunId string
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
