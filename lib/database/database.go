package database

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/boltdb/bolt"
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
- will need to query queue for any tasks in WAIT state
- add specific functions for heartbeats, since they need to check if they're getting killed AND set a value
	- we don't want either thing (heartbeat or kill message) to overwrite what is there
	- whenever we set a single field, we need isolation (like for updateTaskProgress)

*/

type BlanketDB interface {
	// Worker functions
	GetWorkers() (string, error)
	GetWorker(workerId string) (string, error)
	DeleteWorker(workerId string) error
	UpdateWorker(worker *worker.WorkerConf) error
	CleanupStalledWorkers() error
	// Task functions
	GetTask(taskId bson.ObjectId) (tasks.Task, error)
	DeleteTask(taskId bson.ObjectId) error
	GetTasks(tc *TaskSearchConf) (string, int, error)
	StartTask(t *tasks.Task) error
	RunTask(taskId bson.ObjectId, fields *TaskRunConfig) error
	FinishTask(taskId bson.ObjectId, newState string) error
	UpdateTaskProgress(taskId bson.ObjectId, progress int) error
	CleanupStalledTasks() error
}

// Concrete functions
type BlanketBoltDB struct {
	db *bolt.DB
}

func NewBlanketBoltDB(db *bolt.DB) BlanketDB {
	return &BlanketBoltDB{db}
}

const (
	BOLTDB_WORKER_BUCKET = "workers"
	BOLTDB_TASK_BUCKET   = "tasks"
	FAR_FUTURE_SECONDS   = int64(60 * 60 * 24 * 365 * 100)
)

var (
	IdBytes = lib.IdBytes
)

// WORKERS

func (DB *BlanketBoltDB) GetWorkers() (string, error) {
	var err error

	result := "["
	isFirst := true

	err = DB.db.View(func(tx *bolt.Tx) error {
		var err error

		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return fmt.Errorf("Database format error: Bucket '%s' does not exist.", BOLTDB_WORKER_BUCKET)
		}

		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			// Create a worker object from bytes
			// We do this instead of just appending bytes as a form of validation, and to allow filtering later
			w := worker.WorkerConf{}
			err = json.Unmarshal(v, &w)
			if err != nil {
				return err
			}

			if !isFirst {
				result += ","
			}
			isFirst = false
			result += string(v)
		}
		return nil
	})
	result += "]"

	return result, err
}

func (DB *BlanketBoltDB) GetWorker(workerId string) (string, error) {
	result := ""
	err := DB.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return fmt.Errorf("Database format error: Bucket '%s' does not exist.", BOLTDB_WORKER_BUCKET)
		}
		result += string(b.Get([]byte(workerId)))
		return nil
	})
	return result, err
}

// FIXME: More granularity, because may be killed by setting field in database
// Should only every be called by one process, so just overwrites all values
// Should handle creation if the worker doesn't already exist
func (DB *BlanketBoltDB) UpdateWorker(w *worker.WorkerConf) error {
	var err error
	err = DB.db.Update(func(tx *bolt.Tx) error {
		var err error
		bucket := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if bucket == nil {
			return fmt.Errorf("Database format error: Bucket '%s' does not exist.", BOLTDB_WORKER_BUCKET)
		}

		sbts, err := json.Marshal(w)
		if err != nil {
			return err
		}
		sid := fmt.Sprintf("%d", w.Pid)
		err = bucket.Put([]byte(sid), []byte(sbts))
		if err != nil {
			return err
		}
		return nil
	})
	return err
}

func (DB *BlanketBoltDB) DeleteWorker(workerId string) error {
	return DB.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return fmt.Errorf("Database format error: Bucket '%s' does not exist.", BOLTDB_WORKER_BUCKET)
		}
		err := b.Delete([]byte(workerId))
		return err
	})
}

// FIXME: Look for workers that have not heartbeated in a while
// - get pids
// - query OS for process information
// - remove from DB if not running (pid is not found or is to a non-worker process)
// - kill if running and not responsive
func (DB *BlanketBoltDB) CleanupStalledWorkers() error {
	return nil
}

// TASKS

func fetchTaskBucket(tx *bolt.Tx) (b *bolt.Bucket, err error) {
	b = tx.Bucket([]byte(BOLTDB_TASK_BUCKET))
	if b == nil {
		err = fmt.Errorf("Database format error: Bucket '%s' does not exist.", BOLTDB_TASK_BUCKET)
	}
	return
}

func fetchTaskFromBucket(taskId *bson.ObjectId, b *bolt.Bucket) (t tasks.Task, err error) {
	result := b.Get(IdBytes(*taskId))
	err = json.Unmarshal(result, &t)
	return
}

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

// This is always looking in the main tasks bucket
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

func (DB *BlanketBoltDB) GetTask(taskId bson.ObjectId) (tasks.Task, error) {
	var err error
	task := tasks.Task{}
	err = DB.db.View(func(tx *bolt.Tx) error {
		b, err := fetchTaskBucket(tx)
		if err != nil {
			return err
		}
		task, err = fetchTaskFromBucket(&taskId, b)
		return nil
	})
	return task, err
}

// FIXME: Move FindTasksInBoltDB and ModifyTaskInBoltTransaction to their own helper library
// FIXME: Return task objects in a slice instead of a string; may actually want to send on a channel for streaming
func FindTasksInBoltDB(db *bolt.DB, bucketName string, tc *TaskSearchConf) (string, int, error) {
	var err error

	result := "["
	nfound := 0
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			err = fmt.Errorf("Database format error: Bucket '%s' does not exist.", bucketName)
		}

		c := b.Cursor()
		isFirst := true

		// Sort order
		var (
			checkFunction func(bts []byte) bool
			k             []byte
			v             []byte
			iterFunction  func() ([]byte, []byte)
			endBytes      []byte
		)
		if tc.ReverseSort {
			// Have to just jump to the end, since seeking to a far future key goes to the end
			// Seek only goes in 1 order
			// Seek manually to the highest value
			for k, v = c.Last(); k != nil && bytes.Compare(k, IdBytes(tc.LargestId)) >= 0; k, v = c.Prev() {
				continue
			}
			iterFunction = c.Prev
			endBytes = IdBytes(tc.SmallestId)
			checkFunction = func(bts []byte) bool {
				return k != nil && bytes.Compare(k, endBytes) >= 0
			}
		} else {
			// Normal case
			k, v = c.Seek(IdBytes(tc.SmallestId))
			iterFunction = c.Next
			endBytes = IdBytes(tc.LargestId)
			checkFunction = func(bts []byte) bool {
				return k != nil && bytes.Compare(k, endBytes) <= 0
			}
		}

		for ; checkFunction(k); k, v = iterFunction() {
			// e.g. 50-40 == 10
			if nfound-tc.Offset == tc.Limit {
				break
			}

			// Create an object from bytes
			t := tasks.Task{}
			json.Unmarshal(v, &t)

			// Filter results
			if tc.JustUnclaimed && t.WorkerId != "" {
				continue
			}

			if len(tc.AllowedTaskTypes) != 0 && !tc.AllowedTaskTypes[t.TypeId] {
				continue
			}
			if len(tc.AllowedTaskStates) != 0 && !tc.AllowedTaskStates[t.State] {
				continue
			}

			// All tags in tc.requiredTags must be present on every task
			if len(tc.RequiredTags) > 0 {
				hasTags := true
				for _, requestedTag := range tc.RequiredTags {
					found := false
					for _, existingTag := range t.Tags {
						if requestedTag == existingTag {
							found = true
						}
					}
					if !found {
						hasTags = false
						break
					}
				}
				if !hasTags {
					continue
				}
			}

			// All tags on each task must be present in tc.maxTags
			if len(tc.MaxTags) > 0 {
				taskHasExtraTags := false
				for _, existingTag := range t.Tags {
					found := false
					for _, allowedTag := range tc.MaxTags {
						if allowedTag == existingTag {
							found = true
						}
					}
					if !found {
						taskHasExtraTags = true
						break
					}
				}
				if taskHasExtraTags {
					continue
				}
			}

			// Keep track of found items, and build string that will be returned
			nfound += 1
			if nfound > tc.Offset {
				if !tc.JustCounts {
					if !isFirst {
						result += ","
					}
					// FIXME: Return this in chunks
					result += string(v)
				}
				isFirst = false
			}
		}

		return nil
	})

	result += "]"
	return result, nfound, err
}

func saveTaskToBucket(t *tasks.Task, b *bolt.Bucket) (err error) {
	js, err := t.ToJSON()
	if err != nil {
		return err
	}

	err = b.Put(IdBytes(t.Id), []byte(js))
	if err != nil {
		return err
	}
	return nil
}

func ModifyTaskInBoltTransaction(db *bolt.DB, taskId *bson.ObjectId, f func(t *tasks.Task) error) error {
	err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := fetchTaskBucket(tx)
		if err != nil {
			return err
		}
		t, err := fetchTaskFromBucket(taskId, bucket)
		if err != nil {
			return err
		}

		// Main function; accepts a task object and can perform checks and modify it
		err = f(&t)
		if err != nil {
			return err
		} else {
			t.LastUpdatedTs = time.Now().Unix()
		}

		err = saveTaskToBucket(&t, bucket)
		if err != nil {
			return err
		}
		return nil
	})
	return err
}

func (DB *BlanketBoltDB) GetTasks(tc *TaskSearchConf) (string, int, error) {
	return FindTasksInBoltDB(DB.db, BOLTDB_TASK_BUCKET, tc)
}

func (DB *BlanketBoltDB) DeleteTask(taskId bson.ObjectId) error {
	var err error
	return DB.db.Update(func(tx *bolt.Tx) error {
		b, err := fetchTaskBucket(tx)
		if err != nil {
			return err
		}
		return b.Delete(IdBytes(taskId))
	})
	return err
}

// progress is a number [0:100]
// Should also update task.LastUpdatedTs
func (DB *BlanketBoltDB) UpdateTaskProgress(taskId bson.ObjectId, progress int) error {
	return ModifyTaskInBoltTransaction(DB.db, &taskId, func(t *tasks.Task) error {
		t.Progress = progress
		return nil
	})
}

// Things to clean up
// - tasks still in state `START` X min after StartedTs because:
// 	- worker failed to parse worker object
// 	- worker crashed trying to run the task
func (DB *BlanketBoltDB) CleanupStalledTasks() error {
	return nil
}

// This will be called on a task pulled out of the queue
// Any task that, for any reason, happens to exist with the same id should be overwritten
func (DB *BlanketBoltDB) StartTask(t *tasks.Task) error {
	// Just save in database
	err := DB.db.Update(func(tx *bolt.Tx) error {
		bucket, err := fetchTaskBucket(tx)
		if err != nil {
			return err
		}
		err = saveTaskToBucket(t, bucket)
		if err != nil {
			return err
		}
		return nil
	})
	return err
}

type TaskRunConfig struct {
	Timeout       int
	LastUpdatedTs int
	Pid           int
	TypeDigest    string
}

// This should be done as an upsert or within a transaction
func (DB *BlanketBoltDB) RunTask(taskId bson.ObjectId, fields *TaskRunConfig) error {
	// Set lots of fields
	return ModifyTaskInBoltTransaction(DB.db, &taskId, func(t *tasks.Task) error {
		if t.State != "STARTING" {
			return fmt.Errorf("Task found in unexpected state; found '%s', expected 'STARTING'", t.State)
		}
		t.State = "RUNNING"
		t.Progress = 0
		t.Timeout = int64(fields.Timeout)
		t.LastUpdatedTs = int64(fields.LastUpdatedTs)
		t.Pid = fields.Pid
		t.TypeDigest = fields.TypeDigest
		return nil
	})
}

// Set task to a terminal state
// Checks that task is currently in the RUNNING state
// Sets progress to 100 if the state is SUCCESS
func (DB *BlanketBoltDB) FinishTask(taskId bson.ObjectId, newState string) error {
	// Set lots of fields
	return ModifyTaskInBoltTransaction(DB.db, &taskId, func(t *tasks.Task) error {
		if t.State != "RUNNING" {
			return fmt.Errorf("Task found in unexpected state; found '%s', expected 'RUNNING'", t.State)
		}
		t.State = newState
		if t.State == "SUCCESS" {
			t.Progress = 100
		}
		t.LastUpdatedTs = time.Now().Unix()
		return nil
	})
}
