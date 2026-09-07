package bolt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	bolt "go.etcd.io/bbolt"
)

// seedTask puts one task in the database so a backup has something in it
// whose survival can be asserted on.
func seedTask(t *testing.T, DB interface {
	SaveTask(*tasks.Task) error
}) objectid.ObjectId {
	t.Helper()
	task := tasks.Task{
		Id:        objectid.NewObjectId(),
		TypeId:    "echo_task",
		State:     "WAITING",
		CreatedTs: time.Now().Unix(),
	}
	require.NoError(t, DB.SaveTask(&task))
	return task.Id
}

func TestBackupProducesAnOpenableCopy(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)
	require.NoError(t, PrepareDatabase(db, nil))
	DB := &BlanketBoltDB{db}

	taskId := seedTask(t, DB)

	dir := filepath.Join(t.TempDir(), "backups")
	backupPath, err := DB.Backup(dir)
	require.NoError(t, err)
	assert.FileExists(t, backupPath)

	// The real assertion: reopen the copy and find the data. A file of the
	// right size is not a backup.
	copyDb, err := bolt.Open(backupPath, 0666, &bolt.Options{Timeout: time.Second})
	require.NoError(t, err)
	defer copyDb.Close()

	copyDB := &BlanketBoltDB{copyDb}
	got, err := copyDB.GetTask(taskId)
	require.NoError(t, err)
	assert.Equal(t, "echo_task", got.TypeId)

	v, err := copyDB.SchemaVersion()
	require.NoError(t, err)
	assert.Equal(t, 1, v)
}

func TestBackupFilenameCarriesVersionAndAWindowsSafeTimestamp(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)
	require.NoError(t, PrepareDatabase(db, nil))

	dir := filepath.Join(t.TempDir(), "backups")
	backupPath, err := (&BlanketBoltDB{db}).Backup(dir)
	require.NoError(t, err)

	name := filepath.Base(backupPath)
	assert.True(t, strings.HasPrefix(name, "blanket-1-"), "got %q", name)
	assert.True(t, strings.HasSuffix(name, ".db"), "got %q", name)
	// Windows filenames cannot contain a colon, so the RFC3339 timestamp
	// has its colons swapped out. A backup format that works on two of
	// three supported platforms is not a backup format.
	assert.NotContains(t, name, ":")
}

func TestBackupDefaultsToTheDatabasesOwnBackupsDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blanket.db")
	db := openRaw(t, path)
	require.NoError(t, PrepareDatabase(db, nil))

	backupPath, err := (&BlanketBoltDB{db}).Backup("")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, BackupDirName), filepath.Dir(backupPath))
}

func TestBackupRetentionKeepsTheThreeNewest(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)
	require.NoError(t, PrepareDatabase(db, nil))

	dir := filepath.Join(t.TempDir(), "backups")

	// A fake clock, so the ordering under test is the filenames' and not
	// the machine's scheduling.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var written []string
	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		p, err := backupTo(db, dir, 1, &backupOptions{Now: func() time.Time { return at }})
		require.NoError(t, err)
		written = append(written, p)
	}

	kept, err := ListBackups(dir)
	require.NoError(t, err)
	require.Len(t, kept, DefaultBackupRetention, "retention keeps 3 (brief decision row 9)")

	// Newest first, and it is the three newest that survived.
	assert.Equal(t, written[4], kept[0])
	assert.Equal(t, written[3], kept[1])
	assert.Equal(t, written[2], kept[2])
	assert.NoFileExists(t, written[0])
	assert.NoFileExists(t, written[1])
}

// The pruner must only ever delete files it recognises as its own. The
// backups directory belongs to the operator too.
func TestPruneLeavesForeignFilesAlone(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "notes.txt")
	require.NoError(t, os.WriteFile(foreign, []byte("mine"), 0600))
	for i := 0; i < 4; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("blanket-1-2026-01-0%dT00-00-00.000Z.db", i+1)), []byte("x"), 0600))
	}

	require.NoError(t, pruneBackups(dir, 3))

	assert.FileExists(t, foreign)
	kept, err := ListBackups(dir)
	require.NoError(t, err)
	assert.Len(t, kept, 3)
}

func TestListBackupsOnAMissingDirIsEmpty(t *testing.T) {
	got, err := ListBackups(filepath.Join(t.TempDir(), "never-created"))
	require.NoError(t, err)
	assert.Empty(t, got)
}

// ---------------------------------------------------------------------------
// Free-space precheck, with an injected statfs
// ---------------------------------------------------------------------------

func TestFreeSpacePrecheckRefusesBelowTheDatabaseSize(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)
	require.NoError(t, PrepareDatabase(db, nil))

	size, err := databaseSize(db)
	require.NoError(t, err)

	dir := filepath.Join(t.TempDir(), "backups")
	_, err = backupTo(db, dir, 1, &backupOptions{
		FreeSpace: func(string) (uint64, error) { return uint64(size) - 1, nil },
	})
	require.Error(t, err)

	var noSpace *ErrInsufficientSpace
	require.True(t, errors.As(err, &noSpace), "want *ErrInsufficientSpace, got %T", err)
	assert.Equal(t, size, noSpace.Need)

	// And nothing was written, not even a partial file.
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries, "a refused backup must leave no file behind")
}

func TestFreeSpacePrecheckWarnsButProceedsWhenTight(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)
	require.NoError(t, PrepareDatabase(db, nil))

	size, err := databaseSize(db)
	require.NoError(t, err)

	dir := filepath.Join(t.TempDir(), "backups")
	// Enough to write, not enough to sustain retention: warn, don't refuse.
	got, err := backupTo(db, dir, 1, &backupOptions{
		FreeSpace: func(string) (uint64, error) { return uint64(size) * 2, nil },
	})
	require.NoError(t, err)
	assert.FileExists(t, got)
}

// "Don't know" is not "no space" — see lib/diskfree. A platform with no
// statfs must still be able to take the mandatory pre-migration backup.
func TestFreeSpaceUnknownStillBacksUp(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)
	require.NoError(t, PrepareDatabase(db, nil))

	dir := filepath.Join(t.TempDir(), "backups")
	got, err := backupTo(db, dir, 1, &backupOptions{
		FreeSpace: func(string) (uint64, error) { return 0, errors.New("not implemented on this platform") },
	})
	require.NoError(t, err)
	assert.FileExists(t, got)
}

// A migration must not proceed when its mandatory backup could not be
// taken. This is the one place the precheck is load-bearing rather than
// advisory.
func TestMigrationRefusesWhenTheBackupCannotBeTaken(t *testing.T) {
	path := newTempDBPath(t)
	db := openRaw(t, path)

	err := PrepareDatabase(db, &PrepareOptions{
		Registry:  MustNewRegistry(testMigration(2)),
		BackupDir: filepath.Join(t.TempDir(), "backups"),
		FreeSpace: func(string) (uint64, error) { return 1, nil },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mandatory pre-migration backup failed")

	v, verr := SchemaVersionOf(db)
	require.NoError(t, verr)
	assert.Equal(t, 1, v, "no backup means no migration means no version bump")
	assert.Equal(t, "", readTestMarker(t, db))
}

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "512 B", humanBytes(512))
	assert.Equal(t, "1.0 KiB", humanBytes(1024))
	assert.Equal(t, "1.5 MiB", humanBytes(1024*1024*3/2))
}
