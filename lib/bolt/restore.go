package bolt

/*

Restoring a backup (turtlemonvh/blanket#23 phase 4).

This is the other half of the forward-only migration bargain: there are no
Down() functions because going back means putting a file back, and this is
the code that puts the file back. It is reached from
`blanket migrate --restore <path> --yes` and, in phase 6, from
`blanket rollback --restore-db`.

Three properties it has to have, in order of how badly they hurt when
missing:

 1. It must refuse while the database is in use. Overwriting the file
    underneath a running server's open handle does not "restore" anything —
    bolt has the old file's pages mapped, and the first write flushes them
    back over the new contents. The check is to *take the lock ourselves*
    for a moment. Nothing else is a real check: a pidfile can be stale, a
    port probe can race, but a successful flock is proof.

 2. It must verify the backup before touching the live database. A restore
    that discovers halfway through that the "backup" is a truncated file
    has destroyed the only other copy. So the backup is opened, its buckets
    are checked, and its schema version is read, all before the live file
    moves.

 3. It must keep what it replaced. The displaced database is renamed to
    `<database>.pre-restore-<timestamp>`, never deleted. Restoring the
    wrong backup is a very easy mistake to make at 2am, and it should be
    an undoable one.

*/

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	log "github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
)

// RestoreResult describes what a restore did, so the CLI can print it and
// a test can assert on it.
type RestoreResult struct {
	// BackupPath is the file that was restored from.
	BackupPath string
	// DatabasePath is the file that was restored to.
	DatabasePath string
	// DisplacedPath is where the previous database was moved to, or ""
	// when there was no previous database.
	DisplacedPath string
	// SchemaVersion is the version stamped in the restored file.
	SchemaVersion int
}

// RestoreBackup replaces the database at dbPath with the backup at
// backupPath. See the file header for the three checks it makes.
func RestoreBackup(backupPath, dbPath string) (*RestoreResult, error) {
	if _, err := os.Stat(backupPath); err != nil {
		return nil, fmt.Errorf("backup %q: %w", backupPath, err)
	}

	// (2) Verify the backup first, while the live database is still the
	// only thing anyone is relying on.
	version, err := inspectBackup(backupPath)
	if err != nil {
		return nil, fmt.Errorf("refusing to restore: %w", err)
	}

	// (1) Prove nothing has the live database open, by holding its lock
	// for a moment ourselves. A short timeout on purpose: this is a
	// yes/no question, not something to wait out.
	if _, err := os.Stat(dbPath); err == nil {
		probe, err := bolt.Open(dbPath, 0666, &bolt.Options{Timeout: time.Second})
		if err != nil {
			if who := DescribeLockHolder(dbPath); who != "" {
				return nil, fmt.Errorf("refusing to restore while the database is in use: %s. Stop blanket and try again", who)
			}
			return nil, fmt.Errorf("refusing to restore while the database is in use (could not take its lock: %v). Stop blanket and try again", err)
		}
		if err := probe.Close(); err != nil {
			return nil, err
		}
	}

	result := &RestoreResult{
		BackupPath:    backupPath,
		DatabasePath:  dbPath,
		SchemaVersion: version,
	}

	// (3) Move the live database aside rather than overwriting it.
	if _, err := os.Stat(dbPath); err == nil {
		displaced := fmt.Sprintf("%s.pre-restore-%s", dbPath, time.Now().UTC().Format(backupTimeLayout))
		if err := os.Rename(dbPath, displaced); err != nil {
			return nil, fmt.Errorf("could not move the existing database aside: %w", err)
		}
		result.DisplacedPath = displaced
	}

	if err := copyFile(backupPath, dbPath); err != nil {
		// Put the original back; a failed restore must not leave the
		// install with no database at all.
		if result.DisplacedPath != "" {
			if rerr := os.Rename(result.DisplacedPath, dbPath); rerr != nil {
				return nil, fmt.Errorf("restore failed (%v) AND the previous database could not be put back from %s (%v) — move it back by hand", err, result.DisplacedPath, rerr)
			}
		}
		return nil, fmt.Errorf("copying the backup into place: %w", err)
	}

	// The lock-holder sidecar describes the process that owned the file we
	// just replaced. Leaving it would make the next lock conflict lie.
	os.Remove(dbPath + LockSidecarSuffix)

	log.WithFields(log.Fields{
		"from":          backupPath,
		"to":            dbPath,
		"schemaVersion": version,
		"displaced":     result.DisplacedPath,
	}).Warn("restored database from backup")

	return result, nil
}

// inspectBackup opens the candidate read-only and checks it is a blanket
// database, returning its schema version.
func inspectBackup(path string) (int, error) {
	db, err := bolt.Open(path, 0444, &bolt.Options{Timeout: time.Second, ReadOnly: true})
	if err != nil {
		return 0, fmt.Errorf("%q is not a readable bolt database: %w", path, err)
	}
	defer db.Close()

	var version int
	err = db.View(func(tx *bolt.Tx) error {
		for _, name := range []string{BOLTDB_WORKER_BUCKET, BOLTDB_TASK_BUCKET} {
			if tx.Bucket([]byte(name)) == nil {
				return fmt.Errorf("%q is a bolt database but not a blanket one: no %q bucket", path, name)
			}
		}
		// A backup taken by a pre-phase-4 blanket has no meta bucket, and
		// that is a perfectly good backup to restore — it is exactly the
		// "upgrade went wrong, go back to what I had" case. Read the
		// version only if the bucket is there.
		if tx.Bucket([]byte(BOLTDB_META_BUCKET)) == nil {
			version = 1
			return nil
		}
		var err error
		version, err = readSchemaVersion(tx)
		return err
	})
	return version, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if dir := filepath.Dir(dst); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
