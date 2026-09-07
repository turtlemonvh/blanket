package bolt

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func TestRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blanket.db")

	var backupPath string

	db := openRaw(t, path)
	require.NoError(t, PrepareDatabase(db, nil))
	DB := &BlanketBoltDB{db}
	id := seedTask(t, DB)

	var err error
	backupPath, err = DB.Backup("")
	require.NoError(t, err)

	// Change the database after the backup, so a successful restore is
	// observable as the *absence* of the later change rather than only as
	// the presence of the earlier one.
	require.NoError(t, DB.DeleteTask(id))
	_, err = DB.GetTask(id)
	require.Error(t, err)

	// Release the lock: restoring under a live handle is refused, by design.
	require.NoError(t, db.Close())

	res, err := RestoreBackup(backupPath, path)
	require.NoError(t, err)
	assert.Equal(t, 1, res.SchemaVersion)
	require.NotEmpty(t, res.DisplacedPath, "the replaced database must be kept, not deleted")
	assert.FileExists(t, res.DisplacedPath)

	restored := openRaw(t, path)
	got, err := (&BlanketBoltDB{restored}).GetTask(id)
	require.NoError(t, err)
	assert.Equal(t, "echo_task", got.TypeId)
}

// Restoring over a database somebody has open would not restore anything:
// bolt has the old pages mapped and would flush them back over the new
// contents. Taking the lock ourselves is the only real check.
func TestRestoreRefusesWhileTheDatabaseIsInUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blanket.db")

	db := openRaw(t, path)
	require.NoError(t, PrepareDatabase(db, nil))
	backupPath, err := (&BlanketBoltDB{db}).Backup("")
	require.NoError(t, err)

	// db is still open, so the lock is held.
	_, err = RestoreBackup(backupPath, path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in use")
}

// The backup is verified before the live database is touched. Discovering
// a truncated "backup" half-way through a restore would mean the only
// other copy had already been destroyed.
func TestRestoreVerifiesTheBackupBeforeTouchingTheDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blanket.db")
	func() {
		db := openRaw(t, path)
		require.NoError(t, PrepareDatabase(db, nil))
		require.NoError(t, db.Close())
	}()
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	junk := filepath.Join(dir, "not-a-backup.db")
	require.NoError(t, os.WriteFile(junk, []byte("hello"), 0600))

	_, err = RestoreBackup(junk, path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to restore")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the live database must be untouched")
}

// A bolt database that isn't a blanket one is rejected too — the shape
// check is on buckets, not just on the file magic.
func TestRestoreRejectsAForeignBoltDatabase(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "someone-elses.db")
	fdb, err := bolt.Open(foreign, 0666, &bolt.Options{Timeout: time.Second})
	require.NoError(t, err)
	require.NoError(t, fdb.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("wat"))
		return err
	}))
	require.NoError(t, fdb.Close())

	path := filepath.Join(dir, "blanket.db")
	_, err = RestoreBackup(foreign, path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a blanket one")
}

// Restoring onto a path with no database there yet is legitimate — it is
// what a bare-metal recovery looks like.
func TestRestoreOntoAMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.db")
	func() {
		db := openRaw(t, source)
		require.NoError(t, PrepareDatabase(db, nil))
		require.NoError(t, db.Close())
	}()

	target := filepath.Join(t.TempDir(), "fresh.db")
	res, err := RestoreBackup(source, target)
	require.NoError(t, err)
	assert.Empty(t, res.DisplacedPath)
	assert.FileExists(t, target)
}

// The lock-holder sidecar describes the file that was replaced; leaving it
// would make the next lock conflict name a process that has nothing to do
// with the restored database.
func TestRestoreRemovesTheStaleLockSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blanket.db")

	db := openRaw(t, path)
	require.NoError(t, PrepareDatabase(db, nil))
	backupPath, err := (&BlanketBoltDB{db}).Backup("")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	assert.FileExists(t, path+LockSidecarSuffix)

	_, err = RestoreBackup(backupPath, path)
	require.NoError(t, err)
	assert.NoFileExists(t, path+LockSidecarSuffix)
}
