package bolt

import (
	"fmt"
	"os"
	"time"

	"github.com/turtlemonvh/blanket/lib/objectid"

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

// ---------------------------------------------------------------------------
// Pre-0.3.0 compatibility fixture (turtlemonvh/blanket#87, #23 phase 4)
// ---------------------------------------------------------------------------

// LegacyFixture names what WriteLegacyFixtureDatabase put in the file, so
// a test can assert the records come back unchanged.
type LegacyFixture struct {
	Path     string
	TaskId   objectid.ObjectId
	WorkerId objectid.ObjectId
}

// WriteLegacyFixtureDatabase creates a bolt file shaped the way a
// pre-0.3.0 blanket wrote one: the `workers`, `tasks`, and `task-queue`
// buckets and nothing else — in particular **no `meta` bucket and no
// schema version** — holding records whose JSON is missing every field
// added since (runId, exitCode, requeueCount, pidStartTs, lastHeardTs,
// lost, stoppedReason).
//
// The fixture is generated rather than checked in as a binary. A .db file
// in testdata/ would be an opaque blob that nobody can review, that no
// future contributor can regenerate, and whose relevance quietly expires;
// the twelve lines of JSON below say exactly what "an old database" means
// and fail loudly if that stops being true.
//
// It lives in this non-_test.go file for the same reason NewTestDB does:
// the server package's own tests need it, and a helper in a _test.go file
// is not importable across packages.
func WriteLegacyFixtureDatabase(path string) (LegacyFixture, error) {
	f := LegacyFixture{
		Path:     path,
		TaskId:   objectid.NewObjectId(),
		WorkerId: objectid.NewObjectId(),
	}

	db, err := bolt.Open(path, 0666, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return f, err
	}
	defer db.Close()

	// Exactly the JSON an old blanket wrote. Deliberately hand-written
	// rather than produced by marshalling today's structs: marshalling
	// today's structs would produce today's fields, which is the one
	// thing this fixture must not have.
	legacyTask := fmt.Sprintf(`{"id":%s,"pid":0,"createdTs":1600000000,"startedTs":0,`+
		`"lastUpdatedTs":1600000000,"type":"echo_task","resultDir":"/tmp/legacy",`+
		`"typeDigest":"deadbeef","timeout":60,"state":"WAITING","workerId":%s,`+
		`"progress":0,"defaultEnv":{"MESSAGE":"hello from 0.2"},"tags":["legacy"]}`,
		string(IdBytes(f.TaskId)), string(IdBytes(objectid.ObjectId{})))

	legacyWorker := fmt.Sprintf(`{"id":%s,"tags":["legacy"],"logfile":"worker.legacy.log",`+
		`"daemon":false,"pid":4321,"stopped":false,"checkInterval":2,"startedTs":1600000000}`,
		string(IdBytes(f.WorkerId)))

	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{BOLTDB_WORKER_BUCKET, BOLTDB_TASK_BUCKET, BOLTDB_TASK_QUEUE_BUCKET} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		if err := tx.Bucket([]byte(BOLTDB_TASK_BUCKET)).Put(IdBytes(f.TaskId), []byte(legacyTask)); err != nil {
			return err
		}
		if err := tx.Bucket([]byte(BOLTDB_TASK_QUEUE_BUCKET)).Put(IdBytes(f.TaskId), []byte(legacyTask)); err != nil {
			return err
		}
		return tx.Bucket([]byte(BOLTDB_WORKER_BUCKET)).Put(IdBytes(f.WorkerId), []byte(legacyWorker))
	})
	return f, err
}
