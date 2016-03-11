package queue

import (
	"github.com/turtlemonvh/blanket/lib"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

/*

CAVEATS:
- queues can define their own serialization for task objects; we use json strings stored as byte slices in boltdb

FIXME:
- list tasks
- list and claim in 1 step for worker instead of claim by id

*/

type BlanketQueue interface {
	AddTask(task *tasks.Task) error
	ListTasks(tags []string, limit int) ([]tasks.Task, int, error)
	ClaimTask(worker *worker.WorkerConf) (tasks.Task, func() error, func() error, error)
	CleanupUnclaimedTasks() error
}

var (
	IdBytes = lib.IdBytes
)
