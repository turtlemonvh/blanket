package bolt

import (
	"fmt"
	"os"
	"time"

	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/queue"
	bolt "go.etcd.io/bbolt"
)

// Test fixtures for the bolt-backed database and queue.
//
// These deliberately mirror **production's** layout: one bolt file, one
// *bolt.DB handle, every bucket (workers, tasks, task-queue) inside it --
// which is exactly what command/serve.go builds (NewBlanketBoltDB(db) and
// NewBlanketBoltQueue(db) over the same handle).
//
// They used to open two *separate* temp files, one per fixture, so the
// queue's buckets lived in a different database from the tasks bucket.
// That made any cross-bucket read untestable, and the reaper needs
// exactly that: CleanupUnclaimedTasks has to look a queued task up in the
// tasks bucket to tell "the claim landed and only the ack was lost" from
// "the claim never happened" (turtlemonvh/blanket#23 phase 3). bbolt also
// takes an exclusive flock per file, so the two handles could never have
// been pointed at one path without one of them blocking.

// newTestBoltFile opens a fresh bolt database on a throwaway path, and
// returns it with a closer.
func newTestBoltFile() (*bolt.DB, func()) {
	// Retrieve a temporary path.
	f, err := os.CreateTemp("", "blanket-test-*.db")
	if err != nil {
		panic(fmt.Sprintf("temp file: %s", err))
	}
	path := f.Name()
	f.Close()
	os.Remove(path)

	// Open the database.
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		panic(fmt.Sprintf("open: %s", err))
	}

	return db, func() {
		db.Close()
		os.Remove(path)
	}
}

// NewTestDBAndQueue returns a database and a queue sharing one bolt file
// and one handle, plus a single closer -- the same arrangement production
// uses. Prefer this over the two single-purpose helpers below whenever a
// test touches both.
func NewTestDBAndQueue() (database.BlanketDB, queue.BlanketQueue, func()) {
	db, closer := newTestBoltFile()
	return NewBlanketBoltDB(db), NewBlanketBoltQueue(db), closer
}

// NewTestDB returns a database backed by a throwaway bolt file. The queue
// buckets are created in the same file too, so a cross-bucket read from a
// database method behaves as it does in production.
func NewTestDB() (database.BlanketDB, func()) {
	DB, _, closer := NewTestDBAndQueue()
	return DB, closer
}
