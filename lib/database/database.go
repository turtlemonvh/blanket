package database

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cast"
	"github.com/turtlemonvh/blanket/lib"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
	"gopkg.in/mgo.v2/bson"
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
	GetWorker(workerId bson.ObjectId) (worker.WorkerConf, error)
	DeleteWorker(workerId bson.ObjectId) error
	UpdateWorker(worker *worker.WorkerConf) error
	CleanupStalledWorkers() error
	// Task functions
	GetTask(taskId bson.ObjectId) (tasks.Task, error)
	DeleteTask(taskId bson.ObjectId) error
	GetTasks(tc *TaskSearchConf) ([]tasks.Task, int, error)
	SaveTask(t *tasks.Task) error
	RunTask(taskId bson.ObjectId, fields *TaskRunConfig) error
	FinishTask(taskId bson.ObjectId, newState string) error
	UpdateTaskProgress(taskId bson.ObjectId, progress int) error
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
	SmallestId        bson.ObjectId
	LargestId         bson.ObjectId
	AllowedTaskStates map[string]bool
	AllowedTaskTypes  map[string]bool
}

type ItemNotFoundError string

func (e ItemNotFoundError) Error() string {
	return fmt.Sprintf("Item not found: %s", string(e))
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

	// Should be unix timestamps, in seconds
	startTimeSent := c.Query("createdAfter")
	endTimeSent := c.Query("createdBefore")

	startTime := time.Unix(0, 0)
	endTime := time.Unix(FAR_FUTURE_SECONDS, 0)
	startTimeSentInt, err := strconv.ParseInt(startTimeSent, 10, 64)
	if err == nil {
		startTime = time.Unix(startTimeSentInt, 0)
	}
	endTimeSentInt, err := strconv.ParseInt(endTimeSent, 10, 64)
	if err == nil {
		endTime = time.Unix(endTimeSentInt, 0)
	}
	tc.SmallestId = bson.NewObjectIdWithTime(startTime)
	tc.LargestId = bson.NewObjectIdWithTime(endTime)

	// Filtering based on tags, states, types
	tags := c.Query("requiredTags")
	if tags != "" {
		tc.RequiredTags = strings.Split(tags, ",")
	}

	maxTags := c.Query("maxTags")
	if maxTags != "" {
		tc.MaxTags = strings.Split(maxTags, ",")
	}

	sentAllowedStates := c.Query("states")
	tc.AllowedTaskStates = make(map[string]bool)
	if sentAllowedStates != "" {
		for _, tstate := range strings.Split(sentAllowedStates, ",") {
			tc.AllowedTaskStates[tstate] = true
		}
	}

	sentAllowedTypes := c.Query("types")
	tc.AllowedTaskTypes = make(map[string]bool)
	if sentAllowedTypes != "" {
		for _, ttype := range strings.Split(sentAllowedTypes, ",") {
			tc.AllowedTaskTypes[ttype] = true
		}
	}

	return tc
}

type TaskRunConfig struct {
	Timeout       int
	LastUpdatedTs int64
	Pid           int
	TypeDigest    string
}
