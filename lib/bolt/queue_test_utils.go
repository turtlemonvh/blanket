package bolt

import (
	"github.com/turtlemonvh/blanket/lib/queue"
)

// NewTestQueue returns a queue backed by a throwaway bolt file. The
// database buckets are created in the same file (see NewTestDBAndQueue in
// database_test_utils.go), so a queue method that needs to read the tasks
// bucket -- CleanupUnclaimedTasks does -- can.
func NewTestQueue() (queue.BlanketQueue, func()) {
	_, Q, closer := NewTestDBAndQueue()
	return Q, closer
}
