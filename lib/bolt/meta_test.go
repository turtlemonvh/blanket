package bolt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/database"
	bolt "go.etcd.io/bbolt"
)

// newTempDBPath returns a path in a t.TempDir where a bolt file can be
// created. The file is deliberately *not* created: several tests here care
// about the difference between "no database" and "an empty one".
func newTempDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "blanket.db")
}

func openRaw(t *testing.T, path string) *bolt.DB {
	t.Helper()
	db, err := bolt.Open(path, 0666, nil)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestNewBlanketBoltDBCreatesMetaBucket(t *testing.T) {
	db := openRaw(t, newTempDBPath(t))
	NewBlanketBoltDB(db)

	require.NoError(t, db.View(func(tx *bolt.Tx) error {
		for _, name := range []string{BOLTDB_WORKER_BUCKET, BOLTDB_TASK_BUCKET, BOLTDB_META_BUCKET} {
			assert.NotNil(t, tx.Bucket([]byte(name)), "bucket %q missing", name)
		}
		return nil
	}))
}

// An unstamped database — anything written before phase 4 — reads as
// version 1 and is stamped by the open sequence, so the next open sees a
// version rather than inferring one.
func TestUnstampedDatabaseReadsAsV1AndIsStamped(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)
	require.NoError(t, ensureBuckets(db))

	// Nothing stamped it yet.
	require.NoError(t, db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_META_BUCKET))
		assert.Nil(t, b.Get([]byte(MetaKeySchemaVersion)), "expected no schemaVersion key yet")
		return nil
	}))

	v, err := SchemaVersionOf(db)
	require.NoError(t, err)
	assert.Equal(t, database.InitialSchemaVersion, v, "an unstamped database reads as version 1")

	require.NoError(t, PrepareDatabase(db, nil))

	require.NoError(t, db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_META_BUCKET))
		raw := b.Get([]byte(MetaKeySchemaVersion))
		require.NotNil(t, raw, "PrepareDatabase should have stamped the version")
		var got int
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, database.InitialSchemaVersion, got)
		return nil
	}))
}

func TestMetaAccessorsRoundTrip(t *testing.T) {
	DB, closer := NewTestDB()
	defer closer()

	si := database.ServerInstance{InstanceId: "abc123", StartedTs: 1700000000}
	require.NoError(t, DB.SetServerInstance(si))
	got, err := DB.ServerInstance()
	require.NoError(t, err)
	assert.Equal(t, si, got)

	require.NoError(t, DB.SetRestartRecord(database.RestartRecord{}))
	_, err = DB.RestartRecord()
	require.NoError(t, err)

	// No marker on a healthy database.
	m, err := DB.MigrationMarker()
	require.NoError(t, err)
	assert.Nil(t, m)
}

// A database nobody has opened yet answers with zero values rather than
// errors: every meta key is optional by construction.
func TestMetaAccessorsOnAnEmptyBucket(t *testing.T) {
	DB, closer := NewTestDB()
	defer closer()

	si, err := DB.ServerInstance()
	require.NoError(t, err)
	assert.Equal(t, database.ServerInstance{}, si)

	lh, err := DB.LockHolder()
	require.NoError(t, err)
	assert.Equal(t, database.LockHolder{}, lh)
}

func TestLockHolderStampedAndClearedWithSidecar(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)
	require.NoError(t, PrepareDatabase(db, nil))

	DB := &BlanketBoltDB{db}
	lh, err := DB.LockHolder()
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), lh.Pid, "the opening process should be recorded as the holder")
	assert.NotZero(t, lh.AcquiredTs)

	// The sidecar is the copy a locked-out process can actually read; see
	// the header of meta.go.
	sidecar, err := ReadLockHolderSidecar(path)
	require.NoError(t, err)
	require.NotNil(t, sidecar)
	assert.Equal(t, os.Getpid(), sidecar.Pid)

	// And it names us in the message a lock conflict would print.
	assert.Contains(t, DescribeLockHolder(path), "has it open")

	require.NoError(t, DB.ClearLockHolder())

	cleared, err := DB.LockHolder()
	require.NoError(t, err)
	assert.Zero(t, cleared.Pid, "a clean close clears the record")

	sidecar, err = ReadLockHolderSidecar(path)
	require.NoError(t, err)
	assert.Nil(t, sidecar, "the sidecar goes too")
	assert.Equal(t, "", DescribeLockHolder(path))
}

// A dead pid in the sidecar must be reported as a stale lock, not as a
// live holder — otherwise the message sends an operator off to kill a pid
// that now belongs to something else entirely.
func TestDescribeLockHolderReportsAStaleRecord(t *testing.T) {
	path := newTempDBPath(t)
	require.NoError(t, os.WriteFile(path, []byte("not a database, just a name to hang the sidecar on"), 0600))

	// Pid 0 is never a real process; the accessor treats it as "nothing
	// recorded", so use a pid that is syntactically plausible and
	// certainly not running.
	require.NoError(t, writeLockSidecar(path, database.LockHolder{
		Pid:        4000000,
		StartedTs:  1,
		Hostname:   "somebox",
		AcquiredTs: 1700000000,
	}))

	desc := DescribeLockHolder(path)
	require.NotEmpty(t, desc)
	// Either conclusively gone, or "cannot confirm" on a platform with no
	// proclive implementation. Never "has it open".
	assert.NotContains(t, desc, "has it open")
}

func TestReadLockHolderSidecarMissingIsNotAnError(t *testing.T) {
	lh, err := ReadLockHolderSidecar(filepath.Join(t.TempDir(), "nope.db"))
	require.NoError(t, err)
	assert.Nil(t, lh)
}
