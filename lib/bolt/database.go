package bolt

import (
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/boltdb/bolt"
	"github.com/turtlemonvh/blanket/lib"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
	"gopkg.in/mgo.v2/bson"
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

func (DB *BlanketBoltDB) GetWorker(workerId bson.ObjectId) (worker.WorkerConf, error) {
	w := worker.WorkerConf{}
	err := DB.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_WORKER_BUCKET))
		if b == nil {
			return MakeBucketDNEError(BOLTDB_WORKER_BUCKET)
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
			return MakeBucketDNEError(BOLTDB_WORKER_BUCKET)
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
			return MakeBucketDNEError(BOLTDB_WORKER_BUCKET)
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

// Tasks

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
