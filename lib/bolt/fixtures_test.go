package bolt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

// The fixtures must look like production: one file, one handle, all three
// buckets. Anything cross-bucket -- the reaper's CleanupUnclaimedTasks
// looking a queued task up in the tasks bucket
// (turtlemonvh/blanket#23 phase 3) -- is untestable otherwise, and would
// silently pass against fixtures that kept the two apart.

func TestFixturesShareOneDatabaseHandle(t *testing.T) {
	DB, Q, closer := NewTestDBAndQueue()
	defer closer()

	boltDB, ok := DB.(*BlanketBoltDB)
	require.True(t, ok, "expected a *BlanketBoltDB")
	boltQ, ok := Q.(*BlanketBoltQueue)
	require.True(t, ok, "expected a *BlanketBoltQueue")

	assert.Same(t, boltDB.db, boltQ.db, "database and queue must share one bolt handle, as in production")
}

func TestFixturesCreateEveryBucket(t *testing.T) {
	for name, open := range map[string]func() (*bolt.DB, func()){
		"NewTestDB": func() (*bolt.DB, func()) {
			DB, closer := NewTestDB()
			return DB.(*BlanketBoltDB).db, closer
		},
		"NewTestQueue": func() (*bolt.DB, func()) {
			Q, closer := NewTestQueue()
			return Q.(*BlanketBoltQueue).db, closer
		},
	} {
		t.Run(name, func(t *testing.T) {
			db, closer := open()
			defer closer()

			require.NoError(t, db.View(func(tx *bolt.Tx) error {
				for _, bucket := range []string{
					BOLTDB_WORKER_BUCKET,
					BOLTDB_TASK_BUCKET,
					BOLTDB_TASK_QUEUE_BUCKET,
				} {
					assert.NotNil(t, tx.Bucket([]byte(bucket)), "bucket %q missing", bucket)
				}
				return nil
			}))
		})
	}
}
