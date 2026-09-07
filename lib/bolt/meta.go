package bolt

/*

The `meta` bucket, bolt-side (turtlemonvh/blanket#23 phase 4).

Every value is stored as a JSON document under its own key — see
lib/database/meta.go for what the five facts are and why they are separate
keys. The accessors below are deliberately thin: read the key, unmarshal,
or marshal and write it. Anything that needs two facts to agree does so
inside one transaction, in migrations.go.

## The lock-holder sidecar

One of the five facts cannot be read the way the other four are. bolt takes
an *exclusive* flock for the life of an open handle, and a read-only open
takes a shared one, so a process that has been locked out cannot read the
database at all — not even to find out who locked it out. The record inside
`meta` is therefore only ever readable by its own writer.

That would make `meta.lockHolderPid` useless for the thing it exists for:
turning bbolt's bare "timeout" into "pid 4213 (blanket, started 10:02) has
this database open". So the same record is also written to a sidecar file,
`<database>.lock.json`, at the same moment. The bucket record is the
durable, transactional copy; the sidecar is the copy an outsider can read.

The sidecar can go stale in ways the bucket record cannot (a `kill -9`
leaves both behind, but only the sidecar can survive the database file
being moved). It is treated as a *hint* accordingly: the reader pairs it
with lib/proclive, and a recorded pid that is conclusively dead is reported
as a stale lock rather than as a live holder.

*/

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/proclive"
	bolt "go.etcd.io/bbolt"
)

const (
	// BOLTDB_META_BUCKET holds the installation-level facts described in
	// lib/database/meta.go. Created alongside `workers` and `tasks`
	// whenever it is missing, which is what makes a pre-phase-4 database
	// openable by a phase-4 binary with no migration.
	BOLTDB_META_BUCKET = "meta"

	// Keys within the meta bucket.
	MetaKeySchemaVersion   = "schemaVersion"
	MetaKeyServerInstance  = "serverInstance"
	MetaKeyLockHolder      = "lockHolderPid"
	MetaKeyRestartRecord   = "restartRecord"
	MetaKeyMigrationMarker = "migrationMarker"

	// LockSidecarSuffix is appended to the database path to form the
	// sidecar's path; see the file header.
	LockSidecarSuffix = ".lock.json"
)

// ---------------------------------------------------------------------------
// Low-level helpers, all operating on an open transaction
// ---------------------------------------------------------------------------

func fetchMetaBucket(tx *bolt.Tx) (*bolt.Bucket, error) {
	b := tx.Bucket([]byte(BOLTDB_META_BUCKET))
	if b == nil {
		return nil, MakeBucketDNEError(BOLTDB_META_BUCKET)
	}
	return b, nil
}

// getMetaJSON unmarshals the value at key into out. Reports whether the key
// was present; a missing key is not an error, since every meta key is
// optional by construction (an older binary simply never wrote it).
func getMetaJSON(tx *bolt.Tx, key string, out interface{}) (bool, error) {
	b, err := fetchMetaBucket(tx)
	if err != nil {
		return false, err
	}
	v := b.Get([]byte(key))
	if v == nil {
		return false, nil
	}
	if err := json.Unmarshal(v, out); err != nil {
		return true, fmt.Errorf("meta key %q holds undecodable JSON: %w", key, err)
	}
	return true, nil
}

func putMetaJSON(tx *bolt.Tx, key string, in interface{}) error {
	b, err := fetchMetaBucket(tx)
	if err != nil {
		return err
	}
	out, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return b.Put([]byte(key), out)
}

func deleteMeta(tx *bolt.Tx, key string) error {
	b, err := fetchMetaBucket(tx)
	if err != nil {
		return err
	}
	return b.Delete([]byte(key))
}

// readSchemaVersion is the in-transaction form of SchemaVersion. An
// unstamped database reads as database.InitialSchemaVersion — see the
// constant's comment for why that is a definition rather than a guess.
func readSchemaVersion(tx *bolt.Tx) (int, error) {
	var v int
	found, err := getMetaJSON(tx, MetaKeySchemaVersion, &v)
	if err != nil {
		return 0, err
	}
	if !found {
		return database.InitialSchemaVersion, nil
	}
	if v < 1 {
		return 0, fmt.Errorf("meta.%s holds a nonsensical schema version %d", MetaKeySchemaVersion, v)
	}
	return v, nil
}

func readMigrationMarker(tx *bolt.Tx) (*database.MigrationMarker, error) {
	var m database.MigrationMarker
	found, err := getMetaJSON(tx, MetaKeyMigrationMarker, &m)
	if err != nil || !found {
		return nil, err
	}
	return &m, nil
}

// ---------------------------------------------------------------------------
// BlanketDB meta accessors
// ---------------------------------------------------------------------------

func (DB *BlanketBoltDB) SchemaVersion() (int, error) {
	return SchemaVersionOf(DB.db)
}

// SchemaVersionOf is SchemaVersion for a bare handle — the shape
// `blanket migrate` holds, since it never builds a BlanketDB.
func SchemaVersionOf(db *bolt.DB) (int, error) {
	var v int
	err := db.View(func(tx *bolt.Tx) error {
		var err error
		v, err = readSchemaVersion(tx)
		return err
	})
	return v, err
}

func (DB *BlanketBoltDB) SetServerInstance(si database.ServerInstance) error {
	return DB.db.Update(func(tx *bolt.Tx) error {
		return putMetaJSON(tx, MetaKeyServerInstance, si)
	})
}

func (DB *BlanketBoltDB) ServerInstance() (database.ServerInstance, error) {
	var si database.ServerInstance
	err := DB.db.View(func(tx *bolt.Tx) error {
		_, err := getMetaJSON(tx, MetaKeyServerInstance, &si)
		return err
	})
	return si, err
}

func (DB *BlanketBoltDB) LockHolder() (database.LockHolder, error) {
	var lh database.LockHolder
	err := DB.db.View(func(tx *bolt.Tx) error {
		_, err := getMetaJSON(tx, MetaKeyLockHolder, &lh)
		return err
	})
	return lh, err
}

// ClearLockHolder removes both copies of the record: the transactional one
// in the bucket and the sidecar. Called on a clean close, so that a record
// found on the next open means the previous process did not get to shut
// down — the one signal that distinguishes a crash from a stop.
func (DB *BlanketBoltDB) ClearLockHolder() error {
	err := DB.db.Update(func(tx *bolt.Tx) error {
		return deleteMeta(tx, MetaKeyLockHolder)
	})
	// Best-effort on the sidecar: a missing one is the desired end state.
	if path := DB.db.Path(); path != "" {
		os.Remove(path + LockSidecarSuffix)
	}
	return err
}

func (DB *BlanketBoltDB) MigrationMarker() (*database.MigrationMarker, error) {
	var m *database.MigrationMarker
	err := DB.db.View(func(tx *bolt.Tx) error {
		var err error
		m, err = readMigrationMarker(tx)
		return err
	})
	return m, err
}

func (DB *BlanketBoltDB) RestartRecord() (database.RestartRecord, error) {
	var rr database.RestartRecord
	err := DB.db.View(func(tx *bolt.Tx) error {
		_, err := getMetaJSON(tx, MetaKeyRestartRecord, &rr)
		return err
	})
	return rr, err
}

func (DB *BlanketBoltDB) SetRestartRecord(rr database.RestartRecord) error {
	return DB.db.Update(func(tx *bolt.Tx) error {
		return putMetaJSON(tx, MetaKeyRestartRecord, rr)
	})
}

// ---------------------------------------------------------------------------
// Lock holder: writing both copies, and reading the sidecar from outside
// ---------------------------------------------------------------------------

// StampLockHolder records this process as the database's owner, in the
// bucket and in the sidecar. Called once, at open.
func StampLockHolder(db *bolt.DB) error {
	pid := os.Getpid()
	startedTs, _ := proclive.StartTime(pid)
	hostname, _ := os.Hostname()

	lh := database.LockHolder{
		Pid:        pid,
		StartedTs:  startedTs,
		Hostname:   hostname,
		AcquiredTs: time.Now().Unix(),
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		return putMetaJSON(tx, MetaKeyLockHolder, lh)
	}); err != nil {
		return err
	}
	return writeLockSidecar(db.Path(), lh)
}

// writeLockSidecar writes the sidecar temp-and-rename, so a reader never
// sees a half-written file. A failure here is returned but is not fatal to
// the caller: the sidecar is a diagnostic, and a database on a read-only
// directory should still open.
func writeLockSidecar(dbPath string, lh database.LockHolder) error {
	if dbPath == "" {
		return nil
	}
	out, err := json.Marshal(lh)
	if err != nil {
		return err
	}
	final := dbPath + LockSidecarSuffix
	tmp, err := os.CreateTemp(filepath.Dir(final), filepath.Base(final)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, final)
}

// ReadLockHolderSidecar reads the sidecar for the database at dbPath. This
// is the path a process that has been *locked out* must take; see the file
// header. Returns (nil, nil) when there is no sidecar.
func ReadLockHolderSidecar(dbPath string) (*database.LockHolder, error) {
	bts, err := os.ReadFile(dbPath + LockSidecarSuffix)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var lh database.LockHolder
	if err := json.Unmarshal(bts, &lh); err != nil {
		return nil, err
	}
	return &lh, nil
}

// DescribeLockHolder turns the sidecar into the sentence a locked-out
// process should print. It pairs the record with lib/proclive so a stale
// record — the pid is gone, or has been recycled by something else — is
// reported as stale rather than as a live holder pointing the operator at
// an innocent process.
//
// Returns "" when there is nothing useful to say, so the caller can fall
// back to its generic message.
func DescribeLockHolder(dbPath string) string {
	lh, err := ReadLockHolderSidecar(dbPath)
	if err != nil || lh == nil || lh.Pid == 0 {
		return ""
	}

	alive, conclusive := proclive.IsAlive(lh.Pid, lh.StartedTs)
	switch {
	case alive:
		return fmt.Sprintf("pid %d on %s has it open (since %s)",
			lh.Pid, lh.Hostname, time.Unix(lh.AcquiredTs, 0).Format(time.RFC3339))
	case conclusive:
		return fmt.Sprintf("the last recorded holder (pid %d on %s) is gone, so this is a stale lock file; "+
			"if no blanket process is running, the lock will clear on its own once the old process's file handle is released",
			lh.Pid, lh.Hostname)
	default:
		return fmt.Sprintf("pid %d on %s was the last recorded holder (since %s), but this platform cannot confirm whether it is still running",
			lh.Pid, lh.Hostname, time.Unix(lh.AcquiredTs, 0).Format(time.RFC3339))
	}
}
