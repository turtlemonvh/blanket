package bolt

/*

Database backup (turtlemonvh/blanket#23 phase 4).

There was no backup path in blanket at all before this. That is fine while
nothing ever rewrites the database; it stops being fine the moment
migrations exist, which is why this ships in the same phase as they do.

## How the copy is taken

`tx.WriteTo` inside a read transaction. bolt's MVCC means that transaction
sees a consistent point-in-time image of the whole file, so the copy is
valid even though the server is serving requests throughout — no lock to
take, no downtime, no "stop the server first". `cp` would give none of
those guarantees.

## Where it goes

`<directory holding the database>/backups/`, overridable with the
`database.backupDir` config key. Beside the database rather than in a
separate configured tree, because the one property that matters when
somebody is restoring at 2am is that the backup is where they will look for
it, and everything else about a blanket install (results, logs, the db)
already lives in the data directory.

Filenames are `blanket-<schemaVersion>-<timestamp>.db`. The schema version
is in the name because it is the fact you need when choosing which backup
to restore, and reading it out of the file requires opening the file. The
timestamp is RFC3339 with `:` replaced by `-`: Windows filenames cannot
contain a colon, and a backup format that works on two of three supported
platforms is not a backup format.

## The precheck

WriteTo momentarily doubles the database's footprint. The check is:

	free < size          refuse. Writing would (at best) fail part-way and
	                     leave a truncated file that looks like a backup.
	free < size * 4      warn. Retention keeps 3 backups plus the live
	                     database, so below 4× the current size the
	                     retention policy cannot be honoured for long.
	free unknown         proceed, and say so. See lib/diskfree.

## Retention

Keep the 3 newest, prune the rest (decision row 9 of the brief: a count,
not a size cap, with a warning rather than a hard budget). Three is the
number of rollback slots phase 6's `blanket rollback` keeps, and the
database half of a slot is exactly one of these files.

Pruning happens *after* the new backup is written, never before: a prune
that ran first would, in the disk-full case this code exists to survive,
delete a good backup and then fail to write its replacement.

*/

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/diskfree"
	bolt "go.etcd.io/bbolt"
)

const (
	// DefaultBackupRetention is how many backup files are kept.
	DefaultBackupRetention = 3

	// BackupWarnFactor is the free-space multiple below which a backup
	// still proceeds but warns; see the file header.
	BackupWarnFactor = 4

	// backupPrefix / backupSuffix bracket the generated filenames, and
	// are also what the pruner uses to recognise its own files. A file
	// that does not match both is never deleted — the backups directory
	// belongs to the operator too.
	backupPrefix = "blanket-"
	backupSuffix = ".db"

	// backupTimeLayout is RFC3339 with the colons swapped out (see the
	// file header) and milliseconds kept.
	//
	// The milliseconds are not decoration. Two backups taken in the same
	// second would otherwise generate the same filename, and the second
	// would silently replace the first — which is very easy to do:
	// `blanket backup && blanket backup`, or an upgrade script that backs
	// up before and after a step. Losing a backup to a naming collision is
	// exactly the kind of quiet failure this whole phase exists to
	// prevent.
	backupTimeLayout = "2006-01-02T15-04-05.000Z0700"

	// BackupDirName is the subdirectory of the database's directory that
	// backups are written to.
	BackupDirName = "backups"
)

// DefaultBackupDir returns the backups directory for a database at
// dbPath. A relative database path (blanket's default is a bare
// "blanket.db") yields a relative backups directory, resolved against the
// working directory the same way the database itself is.
func DefaultBackupDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), BackupDirName)
}

// backupOptions is the internal knob set; the exported entry points take
// simpler arguments and fill this in.
type backupOptions struct {
	Retention int
	Now       func() time.Time
	FreeSpace func(dir string) (uint64, error)
}

func (o *backupOptions) retention() int {
	if o != nil && o.Retention > 0 {
		return o.Retention
	}
	return DefaultBackupRetention
}

func (o *backupOptions) now() time.Time {
	if o != nil && o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *backupOptions) freeSpace(dir string) (uint64, error) {
	if o != nil && o.FreeSpace != nil {
		return o.FreeSpace(dir)
	}
	return diskfree.Available(dir)
}

// Backup writes a consistent copy of the database into dir and returns the
// path written. dir is created if missing; an empty dir means
// DefaultBackupDir of this database's own path.
func (DB *BlanketBoltDB) Backup(dir string) (string, error) {
	if dir == "" {
		dir = DefaultBackupDir(DB.db.Path())
	}
	version, err := DB.SchemaVersion()
	if err != nil {
		return "", err
	}
	return backupTo(DB.db, dir, version, nil)
}

// backupTo is the implementation shared by the BlanketDB method and the
// pre-migration backup in migrations.go. version is only used to name the
// file, and is passed in rather than read here because the migration path
// has already read it and must name the *pre*-migration version.
func backupTo(db *bolt.DB, dir string, version int, opts *backupOptions) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("could not create backup directory %q: %w", dir, err)
	}

	size, err := databaseSize(db)
	if err != nil {
		return "", err
	}
	if err := CheckBackupSpace(dir, size, opts); err != nil {
		return "", err
	}

	name := fmt.Sprintf("%s%d-%s%s", backupPrefix, version, opts.now().UTC().Format(backupTimeLayout), backupSuffix)
	final := filepath.Join(dir, name)

	// Write to a temp file in the same directory and rename. A reader (a
	// human, `blanket migrate --restore`, phase 6's rollback) must never
	// find a partially written file under a name that says "backup".
	tmp, err := os.CreateTemp(dir, name+".partial*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()

	err = db.View(func(tx *bolt.Tx) error {
		_, werr := tx.WriteTo(tmp)
		return werr
	})
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("writing backup: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return "", err
	}

	log.WithFields(log.Fields{
		"path":          final,
		"bytes":         size,
		"schemaVersion": version,
	}).Info("wrote database backup")

	// Pruning is best-effort: a backup that was written successfully must
	// not be reported as a failure because an old one could not be
	// removed.
	if err := pruneBackups(dir, opts.retention()); err != nil {
		log.WithField("err", err).Warn("could not prune old backups")
	}

	return final, nil
}

// databaseSize reports the database's on-disk size, read from the
// transaction rather than from os.Stat so it is the size of the image the
// copy will actually contain.
func databaseSize(db *bolt.DB) (int64, error) {
	var size int64
	err := db.View(func(tx *bolt.Tx) error {
		size = tx.Size()
		return nil
	})
	return size, err
}

// ErrInsufficientSpace is returned by CheckBackupSpace when there is
// provably not enough room. Its own type so the CLI and the ops endpoint
// can report it as a refusal rather than as an I/O failure.
type ErrInsufficientSpace struct {
	Dir       string
	Need      int64
	Available uint64
}

func (e *ErrInsufficientSpace) Error() string {
	return fmt.Sprintf("not enough free space in %s to back up the database: need at least %s, %s available",
		e.Dir, humanBytes(uint64(e.Need)), humanBytes(e.Available))
}

// CheckBackupSpace implements the precheck described in the file header.
// Exported so `blanket migrate --check` can report the same verdict
// without writing anything.
func CheckBackupSpace(dir string, size int64, opts *backupOptions) error {
	free, err := opts.freeSpace(dir)
	if err != nil {
		// No answer is not "no space" — see lib/diskfree. Proceed, but
		// leave a trail, because a disk-full failure a moment later
		// should not look mysterious.
		log.WithFields(log.Fields{
			"dir": dir,
			"err": err,
		}).Warn("could not determine free space; taking the backup without a precheck")
		return nil
	}
	if free < uint64(size) {
		return &ErrInsufficientSpace{Dir: dir, Need: size, Available: free}
	}
	if free < uint64(size)*BackupWarnFactor {
		log.WithFields(log.Fields{
			"dir":       dir,
			"available": humanBytes(free),
			"database":  humanBytes(uint64(size)),
			"retention": DefaultBackupRetention,
		}).Warn("free space is low relative to the database size; the backup retention policy may not be sustainable")
	}
	return nil
}

// pruneBackups keeps the `keep` newest backup files in dir and removes the
// rest. Newest is decided by the timestamp in the *filename*, not by mtime:
// mtimes get rewritten by rsync, by a restore, and by an operator poking
// around, and the name is the fact the file was created with.
func pruneBackups(dir string, keep int) error {
	files, err := ListBackups(dir)
	if err != nil || len(files) <= keep {
		return err
	}
	var firstErr error
	for _, f := range files[keep:] {
		if err := os.Remove(f); err != nil && firstErr == nil {
			firstErr = err
		} else if err == nil {
			log.WithField("path", f).Info("pruned an old database backup")
		}
	}
	return firstErr
}

// ListBackups returns the backup files in dir, newest first. A missing
// directory is an empty list, not an error.
func ListBackups(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, backupPrefix) && strings.HasSuffix(n, backupSuffix) {
			names = append(names, n)
		}
	}
	// The name is `blanket-<version>-<timestamp>.db`, and the timestamp
	// layout is lexicographically ordered, so a plain reverse string sort
	// puts the newest first within a version — and versions ascend, so it
	// is right across them too.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, filepath.Join(dir, n))
	}
	return out, nil
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
