package bolt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
	"gopkg.in/mgo.v2/bson"
	"log"
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

func MustOpenBoltDatabase() *bolt.DB {
	db, err := bolt.Open(viper.GetString("database"), 0666, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	return db
}

func NewBlanketBoltDB(db *bolt.DB) database.BlanketDB {
	// Ensure required buckets exist
	db.Update(func(tx *bolt.Tx) error {
		var err error

		requiredBuckets := []string{
			BOLTDB_WORKER_BUCKET,
			BOLTDB_TASK_BUCKET,
		}

		for _, bucketName := range requiredBuckets {
			b := tx.Bucket([]byte(bucketName))
			if b == nil {
				b, err = tx.CreateBucket([]byte(bucketName))
				if err != nil {
					log.Fatal(err)
				}
			}
		}

		return nil
	})

	return &BlanketBoltDB{db}
}

// WORKERS

func (DB *BlanketBoltDB) GetWorkers() ([]worker.WorkerConf, error) {
	var err error
	ws := []worker.WorkerConf{}

	err = DB.db.View(func(tx *bolt.Tx) error {
		var err error

		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return fmt.Errorf("Database format error: Bucket '%s' does not exist.", BOLTDB_WORKER_BUCKET)
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

func (DB *BlanketBoltDB) GetWorker(workerId bson.ObjectId) (worker.WorkerConf, error) {
	w := worker.WorkerConf{}
	err := DB.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return fmt.Errorf("Database format error: Bucket '%s' does not exist.", BOLTDB_WORKER_BUCKET)
		}
		return json.Unmarshal(b.Get(IdBytes(workerId)), &w)
	})
	return w, err
}

// FIXME: More granularity, because may be killed by setting field in database
// Should only every be called by one process, so just overwrites all values
// Should handle creation if the worker doesn't already exist
func (DB *BlanketBoltDB) UpdateWorker(w *worker.WorkerConf) error {
	return DB.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return fmt.Errorf("Database format error: Bucket '%s' does not exist.", BOLTDB_WORKER_BUCKET)
		}
		bts, err := json.Marshal(w)
		if err != nil {
			return err
		}
		return b.Put(IdBytes(w.Id), bts)
	})
}

func (DB *BlanketBoltDB) DeleteWorker(workerId bson.ObjectId) error {
	return DB.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return fmt.Errorf("Database format error: Bucket '%s' does not exist.", BOLTDB_WORKER_BUCKET)
		}
		return b.Delete(IdBytes(workerId))
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
	if result == nil {
		err = database.NotFoundError(fmt.Sprintf("No item for id %v", taskId))
		return
	}
	err = json.Unmarshal(result, &t)
	return
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
		return err
	})
	return task, err
}

// Returns a list of tasks, the number found, and any error
// FIXME: Move FindTasksInBoltDB and ModifyTaskInBoltTransaction to their own helper library
// FIXME: Return task objects in a slice instead of a string; may actually want to send on a channel for streaming
func FindTasksInBoltDB(db *bolt.DB, bucketName string, tc *database.TaskSearchConf) ([]tasks.Task, int, error) {
	var err error

	result := []tasks.Task{}
	nfound := 0
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			err = fmt.Errorf("Database format error: Bucket '%s' does not exist.", bucketName)
		}

		c := b.Cursor()

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
			if tc.JustUnclaimed && t.WorkerId.Valid() {
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
					result = append(result, t)
				}
			}
		}

		return nil
	})

	return result, nfound, err
}

func saveTaskToBucket(t *tasks.Task, b *bolt.Bucket) (err error) {
	bts, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return b.Put(IdBytes(t.Id), bts)
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

		return saveTaskToBucket(&t, bucket)
	})
	return err
}

func (DB *BlanketBoltDB) GetTasks(tc *database.TaskSearchConf) ([]tasks.Task, int, error) {
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
// - tasks still in state `CLAIMED` X min after StartedTs because:
//  - worker failed to parse worker object
//  - worker crashed trying to run the task
func (DB *BlanketBoltDB) CleanupStalledTasks() error {
	// FIXME: Implement me
	return nil
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

// This should be done as an upsert or within a transaction
func (DB *BlanketBoltDB) RunTask(taskId bson.ObjectId, fields *database.TaskRunConfig) error {
	// Set lots of fields
	return ModifyTaskInBoltTransaction(DB.db, &taskId, func(t *tasks.Task) error {
		if t.State != "CLAIMED" {
			return fmt.Errorf("Task found in unexpected state; found '%s', expected 'CLAIMED'", t.State)
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
		if t.State != "RUNNING" && t.State != "WAITING" {
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
