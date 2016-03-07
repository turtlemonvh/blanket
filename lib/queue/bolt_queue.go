package queue

import (
	"encoding/json"
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
	"gopkg.in/mgo.v2/bson"
	"time"
)

// Concrete functions
type BlanketBoltQueue struct {
	db *bolt.DB
}

func NewBlanketBoltQueue(db *bolt.DB) BlanketQueue {
	return &BlanketBoltQueue{db}
}

const (
	BOLTDB_TASK_QUEUE_BUCKET = "task-queue"
)

func fetchTaskBucket(tx *bolt.Tx) (b *bolt.Bucket, err error) {
	b = tx.Bucket([]byte(BOLTDB_TASK_QUEUE_BUCKET))
	if b == nil {
		err = fmt.Errorf("Database format error: Bucket '%s' does not exist.", BOLTDB_TASK_QUEUE_BUCKET)
	}
	return
}

// Should add to the relevant queue(s) based on tags
// Searching for a string of tags may be more complex on some platforms (e.g. rabbitmq; may require scanning)
func (Q *BlanketBoltQueue) AddTask(t *tasks.Task) error {
	return Q.db.Update(func(tx *bolt.Tx) error {
		b, err := fetchTaskBucket(tx)
		if b == nil {
			return err
		}
		bts, err := json.Marshal(t)
		if err != nil {
			return err
		}
		return b.Put(IdBytes(t.Id), bts)
	})
}

func (Q *BlanketBoltQueue) ListTasks(tags []string, limit int) (string, int, error) {
	tc := &database.TaskSearchConf{
		Limit:        limit,
		ReverseSort:  true,
		RequiredTags: tags,
		//JustUnclaimed: true,
		SmallestId: bson.NewObjectIdWithTime(time.Unix(0, 0)),
		LargestId:  bson.NewObjectIdWithTime(time.Unix(database.FAR_FUTURE_SECONDS, 0)),
	}
	return database.FindTasksInBoltDB(Q.db, BOLTDB_TASK_QUEUE_BUCKET, tc)
}

// Optional function that is called by a background daemon to move tasks that were supposed to be handled by a worker
// but also are still in the queue (i.e. ack or nack function never got called)
// - In rabbitmq and other queues this is handled for you with a configurable ttl on ack requests
// - In mongo, postgres, bolt, claims are made by setting the workerId field
// When cleaning up unacked, check if the task is in the database >state CLAIMED; if so, maybe just ack failed and we don't want to re-run and duplicate
func (Q *BlanketBoltQueue) CleanupUnclaimedTasks() error {
	// FIXME: Implement me
	// Find all tasks in queue with a worker id that have a lastModifiedTs older than the TTL
	// Set the WorkerId of those tasks back to "" to allow them to get processed
	return nil
}

// Claim a task in the queue; return functions to confirm or deny claim
// Implementers can choose to make the ack and nack functions no-ops, with the side effect of less safety
// Implementers can make the calculation of weights for tasks more complex
// FIXME: Store with est. :: (completion time) = (time added) + (max time) / (task weight)
// - this will make queuing of tasks more fair
// See: https://www.rabbitmq.com/confirms.html
func (Q *BlanketBoltQueue) ClaimTask(worker *worker.WorkerConf) (tasks.Task, func() error, func() error, error) {
	var task tasks.Task
	var ackCallback func() error
	var nackCallback func() error
	var err error

	// Find the first task this worker can work with
	tc := &database.TaskSearchConf{
		Limit:         1,
		ReverseSort:   true,
		MaxTags:       worker.ParsedTags,
		JustUnclaimed: true,
		SmallestId:    bson.NewObjectIdWithTime(time.Unix(0, 0)),
		LargestId:     bson.NewObjectIdWithTime(time.Unix(database.FAR_FUTURE_SECONDS, 0)),
	}
	tasksStr, _, err := database.FindTasksInBoltDB(Q.db, BOLTDB_TASK_QUEUE_BUCKET, tc)
	if err != nil {
		return task, ackCallback, nackCallback, err
	}

	var ts []tasks.Task
	err = json.Unmarshal([]byte(tasksStr), &ts)
	if err != nil {
		return task, ackCallback, nackCallback, err
	}

	// Check that exactly 1 item was returned
	// Return not found error if not here
	if len(ts) != 1 {
		// FIXME: Return typed error
		return tasks.Task{}, ackCallback, nackCallback, fmt.Errorf("No eligible tasks were found")
	}
	task = ts[0]

	// Mark task as claimed by this worker, and mark with last modified time
	// Cleanup task will handle these markers hanging around in the database
	err = Q.db.Update(func(tx *bolt.Tx) error {
		b, err := fetchTaskBucket(tx)
		if b == nil {
			return err
		}

		// Fetch task in queue; unmarshall over whatever already exists in this object
		err = json.Unmarshal(b.Get(IdBytes(task.Id)), &task)

		// Modify
		task.LastUpdatedTs = time.Now().Unix()
		task.WorkerId = worker.Id.Hex()

		// Serilalize and save
		js, err := task.ToJSON()
		if err != nil {
			return err
		}
		return b.Put(IdBytes(task.Id), []byte(js))
	})

	ackCallback = func() error {
		// A function they can call after successfully claiming a task; ack
		// Removes item this bolt bucket
		return Q.db.Update(func(tx *bolt.Tx) error {
			b, err := fetchTaskBucket(tx)
			if b == nil {
				return err
			}
			return b.Delete(IdBytes(task.Id))
		})
	}
	nackCallback = func() error {
		// A function to free the item back for processing in the queue; unack
		// Resets the worker field of this task to nothing
		return Q.db.Update(func(tx *bolt.Tx) error {
			b, err := fetchTaskBucket(tx)
			if b == nil {
				return err
			}

			// Fetch task in queue; unmarshall over whatever already exists in this object
			err = json.Unmarshal(b.Get(IdBytes(task.Id)), &task)

			// Modify
			task.LastUpdatedTs = time.Now().Unix()
			task.WorkerId = ""

			// Serilalize and save
			js, err := task.ToJSON()
			if err != nil {
				return err
			}
			return b.Put(IdBytes(task.Id), []byte(js))
		})
	}

	return task, ackCallback, nackCallback, err
}
