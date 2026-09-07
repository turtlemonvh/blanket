package bolt

/*

Forward-only numbered migrations, and the open-time sequence that applies
them (turtlemonvh/blanket#23 phase 4).

## Forward-only

There are no Down() functions, and there never will be. A rollback path
made of hand-written inverse migrations is code that is written once, run
approximately never, and therefore tested by nobody — the classic shape of
a routine that is broken exactly when you finally need it. Blanket's
rollback story is a *file*: a byte-for-byte backup taken immediately before
the migration, restored with `blanket migrate --restore`. That path is
exercised by every migration, not only by the failing ones.

## Atomicity

Each migration's data change and the schema-version bump that records it
commit in **one** bolt transaction. This is the whole safety argument. A
crash mid-migration therefore leaves either (a) the change applied and the
version bumped, or (b) neither — never "the data is half-new and the
version says old". Combined with the mandatory backup, the worst case is
always recoverable and always recoverable to a *known* state.

## The version mismatch matrix

Let `db` be the version stamped in the file and `target` the highest
version this binary knows.

	db == target                 open normally
	db >  target                 refuse: this binary is older than the
	                             database. Migrations are forward-only, so
	                             there is nothing this binary could do
	                             about it but damage. Name both versions.
	db <  target, no marker      back up, then apply each pending
	                             migration, then clear the marker

and, when a marker is already set:

	marker.To > target           a *newer* binary is mid-migration. Log
	                             "migration vX→vY in progress; this vX
	                             binary is exiting to await the binary
	                             swap" and exit non-zero without touching
	                             anything. Crucially this is not a retry
	                             loop: under `Restart=always` an old
	                             binary that kept retrying would race the
	                             new one for a lock with a few-second
	                             timeout forever, which is a livelock
	                             built into the recovery path.
	marker.To <= target
	  and db >= marker.To        the migration transaction committed and
	                             only the marker-clearing write was lost.
	                             Nothing is wrong. Clear it and continue.
	  and db <  marker.To        a migration died mid-flight. Refuse to
	                             start, and print the exact
	                             `blanket migrate --restore <path> --yes`
	                             command.

That last row is deliberately conservative — refuse and tell the operator,
rather than restoring the backup automatically. Two reasons. An automatic
restore silently discards anything that happened between the backup and the
crash, and there is no way for the code to know whether that matters. And
under a supervisor, an automatic restore that itself fails turns into a
loop that rewrites the database file on every boot; refusing stops the
world exactly once, where a human can see it.

*/

import (
	"fmt"
	"sort"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/database"
	bolt "go.etcd.io/bbolt"
)

// Migration is one numbered, forward-only step.
//
// Up runs inside the same transaction that bumps the schema version to
// Version, so it must not open one of its own and must not assume anything
// it writes is visible outside until it returns nil.
type Migration struct {
	Version int
	Name    string
	Up      func(tx *bolt.Tx) error
}

// Registry is an ordered set of migrations. Registries are values rather
// than a package-level list so a test can build one containing the
// test-only migration (see migrations_test.go) without that migration
// being reachable from a shipped binary.
type Registry struct {
	migrations []Migration
}

// NewRegistry sorts ms by version and rejects duplicates and versions at
// or below the initial one — both of which would silently make a migration
// unreachable, which is the kind of bug that only shows up on somebody
// else's install.
func NewRegistry(ms ...Migration) (*Registry, error) {
	sorted := append([]Migration(nil), ms...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })

	seen := map[int]bool{}
	for _, m := range sorted {
		if m.Version <= database.InitialSchemaVersion {
			return nil, fmt.Errorf("migration %q has version %d; migrations must be numbered above the initial schema version (%d)",
				m.Name, m.Version, database.InitialSchemaVersion)
		}
		if seen[m.Version] {
			return nil, fmt.Errorf("two migrations share version %d", m.Version)
		}
		if m.Up == nil {
			return nil, fmt.Errorf("migration %q (v%d) has no Up function", m.Name, m.Version)
		}
		seen[m.Version] = true
	}
	return &Registry{migrations: sorted}, nil
}

// MustNewRegistry is NewRegistry for a list known at compile time.
func MustNewRegistry(ms ...Migration) *Registry {
	r, err := NewRegistry(ms...)
	if err != nil {
		panic(err)
	}
	return r
}

// TargetVersion is the schema version a database is brought to. With no
// migrations registered it is the initial version, which is why a blanket
// that has never had a migration still stamps and checks a version: the
// machinery has to be load-bearing *before* the first real migration, or
// the first real migration is also the first test of it.
func (r *Registry) TargetVersion() int {
	v := database.InitialSchemaVersion
	for _, m := range r.migrations {
		if m.Version > v {
			v = m.Version
		}
	}
	return v
}

// Pending returns the migrations that still need to run to bring a
// database at version `from` up to TargetVersion.
func (r *Registry) Pending(from int) []Migration {
	out := []Migration{}
	for _, m := range r.migrations {
		if m.Version > from {
			out = append(out, m)
		}
	}
	return out
}

// DefaultRegistry is what a shipped binary uses. It is empty: phase 4 adds
// the framework, not a schema change. See the package docs above for why
// an empty registry is still worth having.
var DefaultRegistry = MustNewRegistry()

// ---------------------------------------------------------------------------
// Errors callers distinguish
// ---------------------------------------------------------------------------

// ErrSchemaTooNew means the database was written by a newer blanket. The
// message names both versions; there is no automatic recovery.
type ErrSchemaTooNew struct {
	DBVersion     int
	BinaryVersion int
}

func (e *ErrSchemaTooNew) Error() string {
	return fmt.Sprintf("database schema version %d is newer than this binary understands (%d): "+
		"migrations are forward-only, so this binary cannot open it. Install blanket %d or newer, "+
		"or restore a backup taken before the upgrade (see docs/upgrade.md)",
		e.DBVersion, e.BinaryVersion, e.DBVersion)
}

// ErrMigrationInProgress means a *newer* binary is part-way through a
// migration. The correct response is to exit and let the binary swap
// finish — never to retry, never to touch the database.
type ErrMigrationInProgress struct {
	Marker        database.MigrationMarker
	BinaryVersion int
}

func (e *ErrMigrationInProgress) Error() string {
	return fmt.Sprintf("migration v%d→v%d in progress; this v%d binary is exiting to await the binary swap",
		e.Marker.From, e.Marker.To, e.BinaryVersion)
}

// ErrMigrationIncomplete means a migration died mid-flight and the
// database is at a version below where that migration was heading.
type ErrMigrationIncomplete struct {
	Marker    database.MigrationMarker
	DBVersion int
}

func (e *ErrMigrationIncomplete) Error() string {
	restore := e.Marker.BackupPath
	if restore == "" {
		restore = "<a backup from " + backupDirHint + ">"
	}
	return fmt.Sprintf("a migration v%d→v%d started at %s never finished; the database is still at version %d. "+
		"Restore the pre-migration backup before starting blanket again:\n\n    blanket migrate --restore %s --yes\n",
		e.Marker.From, e.Marker.To,
		time.Unix(e.Marker.StartedTs, 0).Format(time.RFC3339),
		e.DBVersion, restore)
}

// backupDirHint is only ever interpolated into the message above, for the
// (unreachable in practice) case of a marker with no backup path.
const backupDirHint = "the backups/ directory beside your database"

// ---------------------------------------------------------------------------
// The open-time sequence
// ---------------------------------------------------------------------------

// PrepareOptions configures PrepareDatabase. Every field has a usable
// zero value; the struct exists so tests can inject a registry, a clock,
// and a backup directory without a five-argument function.
type PrepareOptions struct {
	// Registry defaults to DefaultRegistry.
	Registry *Registry
	// BackupDir is where the mandatory pre-migration backup is written.
	// Empty means DefaultBackupDir(<database path>).
	BackupDir string
	// Retention is how many backups to keep; zero means
	// DefaultBackupRetention.
	Retention int
	// Now defaults to time.Now.
	Now func() time.Time
	// FreeSpace defaults to diskfree.Available. Injected by the
	// free-space precheck tests.
	FreeSpace func(dir string) (uint64, error)
}

func (o *PrepareOptions) registry() *Registry {
	if o != nil && o.Registry != nil {
		return o.Registry
	}
	return DefaultRegistry
}

func (o *PrepareOptions) now() time.Time {
	if o != nil && o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// PrepareDatabase runs the open-time sequence on an already-open handle:
// ensure the buckets exist, resolve the version mismatch matrix above,
// apply any pending migrations after a mandatory backup, stamp the version,
// and record this process as the lock holder.
//
// This is the *only* implementation of that sequence. `blanket migrate`
// calls it too, rather than reimplementing it, so the CLI and the server
// can never drift into disagreeing about what a database needs.
func PrepareDatabase(db *bolt.DB, opts *PrepareOptions) error {
	if err := ensureBuckets(db); err != nil {
		return err
	}

	reg := opts.registry()
	target := reg.TargetVersion()

	var (
		current int
		marker  *database.MigrationMarker
	)
	if err := db.View(func(tx *bolt.Tx) error {
		var err error
		if current, err = readSchemaVersion(tx); err != nil {
			return err
		}
		marker, err = readMigrationMarker(tx)
		return err
	}); err != nil {
		return err
	}

	// A marker is read before the version comparison: an in-flight
	// migration is a stronger fact than a version mismatch, and the two
	// answers differ (exit-and-wait vs. refuse-and-restore).
	if marker != nil {
		switch {
		case marker.To > target:
			return &ErrMigrationInProgress{Marker: *marker, BinaryVersion: target}
		case current >= marker.To:
			// The migration transaction committed; only the write that
			// clears the marker was lost. Tidy up and carry on.
			log.WithFields(log.Fields{
				"from": marker.From,
				"to":   marker.To,
			}).Warn("clearing a migration marker left behind by a completed migration")
			if err := db.Update(func(tx *bolt.Tx) error {
				return deleteMeta(tx, MetaKeyMigrationMarker)
			}); err != nil {
				return err
			}
		default:
			return &ErrMigrationIncomplete{Marker: *marker, DBVersion: current}
		}
	}

	switch {
	case current > target:
		return &ErrSchemaTooNew{DBVersion: current, BinaryVersion: target}
	case current < target:
		if err := migrateForward(db, reg, current, target, opts); err != nil {
			return err
		}
	default:
		// Already at the target. Stamp it anyway if it was never
		// written: an unstamped database is *read* as version 1, and
		// writing that down is what makes the next open see a version
		// rather than infer one. No data is touched.
		if err := stampVersionIfUnset(db, target); err != nil {
			return err
		}
	}

	// Whoever we are, we hold the lock now. Failing to record it is not
	// worth refusing to start over — it costs a nicer error message on
	// somebody else's next open, nothing more.
	if err := StampLockHolder(db); err != nil {
		log.WithField("err", err).Warn("could not record the lock holder; a locked-out process will get a generic message")
	}
	return nil
}

// Inspection is what `blanket migrate --check` reports: everything about
// a database's schema state, established without writing to it.
type Inspection struct {
	Path string
	// Unstamped is true for a database with no schemaVersion key — every
	// database written before phase 4. CurrentVersion is then the
	// initial version, by definition rather than by guess.
	Unstamped      bool
	CurrentVersion int
	TargetVersion  int
	Pending        []Migration
	Marker         *database.MigrationMarker
}

// UpToDate reports whether opening this database would apply anything.
func (i *Inspection) UpToDate() bool {
	return len(i.Pending) == 0 && i.Marker == nil && i.CurrentVersion <= i.TargetVersion
}

// InspectDatabase reports a database's schema state without modifying it —
// not even to create the `meta` bucket, which is why it does not go
// through ensureBuckets. `--check` has to be safe to run against anything,
// including a database an operator is in the middle of worrying about.
//
// The read-only open takes a *shared* flock, so a running server's
// exclusive one blocks it. That is the correct behaviour rather than a
// limitation: the answer to "what version is the database?" while another
// process owns it should come from that process (GET /config/), not from a
// second reader.
func InspectDatabase(path string, reg *Registry) (*Inspection, error) {
	if reg == nil {
		reg = DefaultRegistry
	}
	db, err := bolt.Open(path, 0444, &bolt.Options{Timeout: OpenTimeout(), ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer db.Close()

	out := &Inspection{Path: path, TargetVersion: reg.TargetVersion()}
	err = db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte(BOLTDB_META_BUCKET)) == nil {
			out.Unstamped = true
			out.CurrentVersion = database.InitialSchemaVersion
			return nil
		}
		if tx.Bucket([]byte(BOLTDB_META_BUCKET)).Get([]byte(MetaKeySchemaVersion)) == nil {
			out.Unstamped = true
		}
		var err error
		if out.CurrentVersion, err = readSchemaVersion(tx); err != nil {
			return err
		}
		out.Marker, err = readMigrationMarker(tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	out.Pending = reg.Pending(out.CurrentVersion)
	return out, nil
}

// ensureBuckets creates the buckets a blanket database must have. `meta`
// joins `workers` and `tasks` here, which is what lets a phase-4 binary
// open a pre-phase-4 database with no migration and no ceremony.
func ensureBuckets(db *bolt.DB) error {
	return db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{
			BOLTDB_WORKER_BUCKET,
			BOLTDB_TASK_BUCKET,
			BOLTDB_META_BUCKET,
		} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		return nil
	})
}

func stampVersionIfUnset(db *bolt.DB, version int) error {
	return db.Update(func(tx *bolt.Tx) error {
		b, err := fetchMetaBucket(tx)
		if err != nil {
			return err
		}
		if b.Get([]byte(MetaKeySchemaVersion)) != nil {
			return nil
		}
		log.WithField("version", version).Info("stamping schema version on a previously unstamped database")
		return putMetaJSON(tx, MetaKeySchemaVersion, version)
	})
}

// migrateForward takes the mandatory backup, sets the marker, applies each
// pending migration in its own single transaction, and clears the marker.
func migrateForward(db *bolt.DB, reg *Registry, from, to int, opts *PrepareOptions) error {
	pending := reg.Pending(from)

	dir := ""
	retention := 0
	var free func(string) (uint64, error)
	if opts != nil {
		dir, retention, free = opts.BackupDir, opts.Retention, opts.FreeSpace
	}
	if dir == "" {
		dir = DefaultBackupDir(db.Path())
	}

	log.WithFields(log.Fields{
		"from":       from,
		"to":         to,
		"migrations": len(pending),
		"backupDir":  dir,
	}).Warn("database schema is out of date; backing up before migrating")

	backupPath, err := backupTo(db, dir, from, &backupOptions{
		Retention: retention,
		Now:       opts.now,
		FreeSpace: free,
	})
	if err != nil {
		return fmt.Errorf("refusing to migrate: the mandatory pre-migration backup failed: %w", err)
	}

	marker := database.MigrationMarker{
		From:       from,
		To:         to,
		StartedTs:  opts.now().Unix(),
		BackupPath: backupPath,
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		return putMetaJSON(tx, MetaKeyMigrationMarker, marker)
	}); err != nil {
		return err
	}

	for _, m := range pending {
		log.WithFields(log.Fields{
			"version": m.Version,
			"name":    m.Name,
		}).Warn("applying migration")

		// The data change and the version bump, in one transaction. See
		// the atomicity note at the top of this file.
		if err := db.Update(func(tx *bolt.Tx) error {
			if err := m.Up(tx); err != nil {
				return err
			}
			return putMetaJSON(tx, MetaKeySchemaVersion, m.Version)
		}); err != nil {
			return fmt.Errorf("migration %q (v%d) failed and was rolled back; the database is still at version %d "+
				"and the pre-migration backup is at %s: %w", m.Name, m.Version, from, backupPath, err)
		}
		from = m.Version
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		return deleteMeta(tx, MetaKeyMigrationMarker)
	}); err != nil {
		return err
	}

	log.WithField("version", to).Warn("migration complete")
	return nil
}
