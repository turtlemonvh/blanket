package bolt

/*

The migration harness's own tests, including the test-only migration the
design calls for: blanket ships zero real migrations, so without one of
these the entire mechanism would first be exercised by whichever change
finally needs it — i.e. in production, on somebody's data.

It is registered only from here. A shipped binary's DefaultRegistry stays
empty and its target version stays 1.

*/

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/database"
	bolt "go.etcd.io/bbolt"
)

const (
	testMigrationBucket = "meta"
	testMigrationKey    = "phase4TestMigrationRan"
)

// testMigration is the proof-of-harness migration. It writes one key, so
// "did the data change?" and "did the version bump?" are two independently
// observable facts that the atomicity test can check disagree with each
// other never.
func testMigration(version int) Migration {
	return Migration{
		Version: version,
		Name:    fmt.Sprintf("test-only marker v%d", version),
		Up: func(tx *bolt.Tx) error {
			b, err := fetchMetaBucket(tx)
			if err != nil {
				return err
			}
			return b.Put([]byte(testMigrationKey), []byte(fmt.Sprintf("v%d", version)))
		},
	}
}

// failingMigration writes the same marker and then fails, so the rollback
// of the whole transaction — data *and* version — is observable.
func failingMigration(version int) Migration {
	inner := testMigration(version)
	return Migration{
		Version: version,
		Name:    "test-only migration that dies half-way",
		Up: func(tx *bolt.Tx) error {
			if err := inner.Up(tx); err != nil {
				return err
			}
			return errors.New("boom, half-way through")
		},
	}
}

func readTestMarker(t *testing.T, db *bolt.DB) string {
	t.Helper()
	var got string
	require.NoError(t, db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(testMigrationBucket))
		if b == nil {
			return nil
		}
		got = string(b.Get([]byte(testMigrationKey)))
		return nil
	}))
	return got
}

func prepareOptsWithRegistry(t *testing.T, reg *Registry) *PrepareOptions {
	t.Helper()
	return &PrepareOptions{
		Registry:  reg,
		BackupDir: filepath.Join(t.TempDir(), "backups"),
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func TestDefaultRegistryShipsNoMigrations(t *testing.T) {
	assert.Equal(t, database.InitialSchemaVersion, DefaultRegistry.TargetVersion(),
		"blanket ships zero real migrations; phase 4 adds the framework, not a schema change")
	assert.Empty(t, DefaultRegistry.Pending(database.InitialSchemaVersion))
}

func TestNewRegistryRejectsBadMigrations(t *testing.T) {
	_, err := NewRegistry(testMigration(1))
	assert.Error(t, err, "a migration numbered at the initial version would never run")

	_, err = NewRegistry(testMigration(2), testMigration(2))
	assert.Error(t, err, "duplicate versions")

	_, err = NewRegistry(Migration{Version: 2, Name: "no Up"})
	assert.Error(t, err)
}

func TestRegistryOrdersAndReportsPending(t *testing.T) {
	reg, err := NewRegistry(testMigration(4), testMigration(2), testMigration(3))
	require.NoError(t, err)
	assert.Equal(t, 4, reg.TargetVersion())

	pending := reg.Pending(2)
	require.Len(t, pending, 2)
	assert.Equal(t, 3, pending[0].Version)
	assert.Equal(t, 4, pending[1].Version)
	assert.Empty(t, reg.Pending(4))
}

// ---------------------------------------------------------------------------
// Applying migrations
// ---------------------------------------------------------------------------

func TestForwardMigrationAppliesAfterABackup(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)

	backupDir := filepath.Join(t.TempDir(), "backups")
	reg := MustNewRegistry(testMigration(2))

	require.NoError(t, PrepareDatabase(db, &PrepareOptions{Registry: reg, BackupDir: backupDir}))

	v, err := SchemaVersionOf(db)
	require.NoError(t, err)
	assert.Equal(t, 2, v, "the version should have been bumped")
	assert.Equal(t, "v2", readTestMarker(t, db), "the migration's data change should be visible")

	backups, err := ListBackups(backupDir)
	require.NoError(t, err)
	require.Len(t, backups, 1, "the pre-migration backup is mandatory")
	// Named for the version it was taken *at*, not the one being migrated to.
	assert.Contains(t, filepath.Base(backups[0]), "blanket-1-")

	// The marker is cleared once everything landed.
	marker, err := (&BlanketBoltDB{db}).MigrationMarker()
	require.NoError(t, err)
	assert.Nil(t, marker)
}

// The single most important property in phase 4: the data change and the
// version bump commit together or not at all. A migration that dies
// half-way must leave the version unchanged, the data untouched, and the
// backup intact.
func TestFailedMigrationRollsBackDataAndVersionTogether(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)

	backupDir := filepath.Join(t.TempDir(), "backups")
	reg := MustNewRegistry(failingMigration(2))

	err := PrepareDatabase(db, &PrepareOptions{Registry: reg, BackupDir: backupDir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still at version 1")

	v, verr := SchemaVersionOf(db)
	require.NoError(t, verr)
	assert.Equal(t, 1, v, "a failed migration must not bump the version")
	assert.Equal(t, "", readTestMarker(t, db), "…and must not leave its data change behind either")

	backups, berr := ListBackups(backupDir)
	require.NoError(t, berr)
	require.Len(t, backups, 1, "the backup is what makes the failure recoverable; it must still be there")

	// The marker is left set, which is exactly what makes the next boot
	// refuse rather than blunder on.
	marker, merr := (&BlanketBoltDB{db}).MigrationMarker()
	require.NoError(t, merr)
	require.NotNil(t, marker)
	assert.Equal(t, 2, marker.To)
	assert.Equal(t, backups[0], marker.BackupPath)
}

func TestBackwardMismatchRefusesToOpen(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)

	// Stamp the database as though a newer blanket wrote it.
	require.NoError(t, ensureBuckets(db))
	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		return putMetaJSON(tx, MetaKeySchemaVersion, 9)
	}))

	err := PrepareDatabase(db, prepareOptsWithRegistry(t, MustNewRegistry(testMigration(2))))
	require.Error(t, err)

	var tooNew *ErrSchemaTooNew
	require.True(t, errors.As(err, &tooNew), "want *ErrSchemaTooNew, got %T", err)
	assert.Equal(t, 9, tooNew.DBVersion)
	assert.Equal(t, 2, tooNew.BinaryVersion)
	// Both versions must appear, or the operator can't tell what to install.
	assert.Contains(t, err.Error(), "9")
	assert.Contains(t, err.Error(), "2")
}

// ---------------------------------------------------------------------------
// The migration marker, boot by boot
// ---------------------------------------------------------------------------

func setMarker(t *testing.T, db *bolt.DB, m database.MigrationMarker) {
	t.Helper()
	require.NoError(t, ensureBuckets(db))
	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		return putMetaJSON(tx, MetaKeyMigrationMarker, m)
	}))
}

// An OLD binary booting into a migration a NEWER one is running must log
// and exit without touching anything — never retry, or the two race for
// the bolt lock forever under Restart=always.
func TestOldBinaryWithAnInProgressMarkerRefusesAndExits(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)
	setMarker(t, db, database.MigrationMarker{From: 1, To: 5, StartedTs: time.Now().Unix(), BackupPath: "/tmp/b.db"})

	// This "binary" only knows up to v2.
	err := PrepareDatabase(db, prepareOptsWithRegistry(t, MustNewRegistry(testMigration(2))))
	require.Error(t, err)

	var inProgress *ErrMigrationInProgress
	require.True(t, errors.As(err, &inProgress), "want *ErrMigrationInProgress, got %T", err)
	assert.Contains(t, err.Error(), "migration v1→v5 in progress")
	assert.Contains(t, err.Error(), "exiting to await the binary swap")

	// Nothing was written: no version stamp, no lock holder.
	require.NoError(t, db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(BOLTDB_META_BUCKET))
		assert.Nil(t, b.Get([]byte(MetaKeySchemaVersion)))
		assert.Nil(t, b.Get([]byte(MetaKeyLockHolder)))
		return nil
	}))
}

// The NEW binary finding a stale marker for a migration that never
// finished refuses to start and names the backup to restore.
func TestNewBinaryWithAStaleMarkerRefusesAndNamesTheBackup(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)
	setMarker(t, db, database.MigrationMarker{From: 1, To: 2, StartedTs: 1700000000, BackupPath: "/var/lib/blanket/backups/blanket-1-x.db"})

	err := PrepareDatabase(db, prepareOptsWithRegistry(t, MustNewRegistry(testMigration(2))))
	require.Error(t, err)

	var incomplete *ErrMigrationIncomplete
	require.True(t, errors.As(err, &incomplete), "want *ErrMigrationIncomplete, got %T", err)
	assert.Contains(t, err.Error(), "never finished")
	assert.Contains(t, err.Error(), "blanket migrate --restore /var/lib/blanket/backups/blanket-1-x.db --yes")
}

// A marker left behind by a migration whose transaction *did* commit is
// harmless: only the marker-clearing write was lost. Clear it and boot.
func TestCompletedMigrationWithALostMarkerClearIsRecovered(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)
	require.NoError(t, ensureBuckets(db))
	require.NoError(t, db.Update(func(tx *bolt.Tx) error {
		return putMetaJSON(tx, MetaKeySchemaVersion, 2)
	}))
	setMarker(t, db, database.MigrationMarker{From: 1, To: 2, StartedTs: 1700000000, BackupPath: "/tmp/b.db"})

	require.NoError(t, PrepareDatabase(db, prepareOptsWithRegistry(t, MustNewRegistry(testMigration(2)))))

	marker, err := (&BlanketBoltDB{db}).MigrationMarker()
	require.NoError(t, err)
	assert.Nil(t, marker, "a marker whose migration provably committed should be cleared, not fatal")
}

// ---------------------------------------------------------------------------
// Inspection (what `blanket migrate --check` reports)
// ---------------------------------------------------------------------------

func TestInspectDatabaseReportsPendingWithoutWriting(t *testing.T) {
	path := newTempDBPath(t)
	func() {
		db := openRaw(t, path)
		require.NoError(t, ensureBuckets(db))
		require.NoError(t, db.Close())
	}()

	reg := MustNewRegistry(testMigration(2), testMigration(3))
	insp, err := InspectDatabase(path, reg)
	require.NoError(t, err)

	assert.True(t, insp.Unstamped)
	assert.Equal(t, 1, insp.CurrentVersion)
	assert.Equal(t, 3, insp.TargetVersion)
	assert.Len(t, insp.Pending, 2)
	assert.False(t, insp.UpToDate())

	// And it really did not write: still unstamped.
	insp2, err := InspectDatabase(path, reg)
	require.NoError(t, err)
	assert.True(t, insp2.Unstamped)
}

func TestInspectDatabaseOnAFreshDatabaseIsUpToDate(t *testing.T) {
	path := newTempDBPath(t)
	func() {
		db := openRaw(t, path)
		require.NoError(t, PrepareDatabase(db, nil))
		require.NoError(t, db.Close())
	}()

	insp, err := InspectDatabase(path, nil)
	require.NoError(t, err)
	assert.False(t, insp.Unstamped)
	assert.True(t, insp.UpToDate())
	assert.Empty(t, insp.Pending)
}

func TestInspectDatabaseOnAMissingFile(t *testing.T) {
	_, err := InspectDatabase(filepath.Join(t.TempDir(), "nope.db"), nil)
	assert.Error(t, err, "a read-only open of a nonexistent database should fail rather than create one")
	_, statErr := os.Stat(filepath.Join(t.TempDir(), "nope.db"))
	assert.True(t, os.IsNotExist(statErr))
}
