package queue

import (
	"errors"

	"github.com/turtlemonvh/blanket/lib"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

// ErrQueueEmpty signals that no task in the queue matches the requesting
// worker's capabilities. It is a normal steady state (workers poll an idle
// queue) and should not surface as an error to callers — the server maps
// it to 204 No Content and the worker client returns a zero Task.
var ErrQueueEmpty = errors.New("queue: no eligible tasks")

/*

CAVEATS:
- queues can define their own serialization for task objects; we use json strings stored as byte slices in boltdb

FIXME:
- list and claim in 1 step for worker instead of claim by id

*/

type BlanketQueue interface {
	AddTask(task *tasks.Task) error
	ClaimTask(worker *worker.WorkerConf) (tasks.Task, func() error, func() error, error)
	CleanupUnclaimedTasks() error
}

var (
	IdBytes = lib.IdBytes
)
