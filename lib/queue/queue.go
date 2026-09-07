package queue

import (
	"errors"

	"github.com/turtlemonvh/blanket/lib"
	"github.com/turtlemonvh/blanket/lib/database"
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
	// CleanupUnclaimedTasks reconciles queue entries whose claim was never
	// acked. It takes the same injected clock/liveness/journal options as
	// the database's cleanup routines (turtlemonvh/blanket#23 phase 3) --
	// see database.ReapOptions for why. That is also why this package now
	// imports lib/database: one options type for all three routines beats
	// three near-identical ones.
	CleanupUnclaimedTasks(opts *database.ReapOptions) (database.ReapReport, error)
}

var (
	IdBytes = lib.IdBytes
)
