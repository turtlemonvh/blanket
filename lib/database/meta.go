package database

/*

The `meta` bucket's value types (turtlemonvh/blanket#23 phase 4).

They live here rather than in lib/bolt because the BlanketDB interface
names them, and because nothing about them is bolt-specific: a second
backend would store the same five facts.

What the bucket is *for* is worth stating once. Everything else in the
database describes work — tasks, workers, the queue. `meta` describes the
**installation**: which schema the file on disk is written in, which
process currently owns it, and what multi-step operation (a migration, and
in phase 5 a restart) was in flight the last time anyone touched it. Those
are precisely the facts a process needs *before* it can safely read
anything else, and the facts a human needs when a process died in the
middle of something.

Each fact is a separate key holding a JSON document, rather than one
"meta" document holding all five. Separate keys mean an old binary
tolerates a key it has never heard of, a partial write can only ever lose
one fact, and two writers touching different facts never collide.

*/

// SchemaVersion values.
//
// InitialSchemaVersion is what an *unstamped* database — every database
// written before phase 4 existed — is read as. It is not a guess: the
// pre-phase-4 layout is version 1 by definition, since nothing has ever
// changed it. Such a database is stamped on its first successful open and
// no data is touched (see docs/upgrade.md).
const InitialSchemaVersion = 1

// LockHolder identifies the process that currently holds the database's
// exclusive bolt lock.
//
// bolt takes an exclusive flock for the life of an open handle, so exactly
// one blanket process can have the database open at a time. When a second
// one tries, all it gets from bbolt is a bare "timeout" — which tells an
// operator nothing about *who* to go and look at. This record turns that
// into a name.
//
// StartedTs pairs with Pid for the same reason worker.WorkerConf.PidStartTs
// does: pids are recycled, and reporting "held by pid 4213" when 4213 is
// now an unrelated editor would send the operator to kill the wrong thing.
// With both, lib/proclive can say whether the recorded process is still
// the one that was recorded.
type LockHolder struct {
	Pid       int    `json:"pid"`
	StartedTs int64  `json:"startedTs"`
	Hostname  string `json:"hostname"`
	// AcquiredTs is when the lock was taken, in unix seconds.
	AcquiredTs int64 `json:"acquiredTs"`
}

// ServerInstance is the identity of one server *process*: the instance id
// a worker's heartbeat response carries, and when that process started.
//
// Both were generated in memory in phase 3 (server.ServerConfig.InstanceId)
// and lived only for the life of the process. Persisting them means the
// pair a worker last saw is still readable after the process is gone —
// which is what lets a restart, a crash, and a still-running server be
// told apart from the outside.
type ServerInstance struct {
	InstanceId string `json:"instanceId"`
	StartedTs  int64  `json:"startedTs"`
}

// MigrationMarker records that a forward migration is in flight.
//
// It is written *before* the first migration transaction and cleared after
// the last one, so finding one set on boot means a process died somewhere
// in between. Three cases follow from comparing it against what the
// booting binary knows (see docs/upgrade.md for the full table):
//
//   - To is higher than this binary's target version: a *newer* binary is
//     mid-migration and this old one must not touch the database at all.
//     It logs and exits non-zero rather than fighting for the bolt lock —
//     under `Restart=always` two binaries racing for a one-second lock
//     timeout is a livelock, not a recovery.
//   - To is at or below this binary's target, and the stored schema
//     version already reached To: the migration transaction committed and
//     only the marker-clearing write was lost. Harmless; the marker is
//     cleared and boot continues.
//   - To is at or below this binary's target and the schema version has
//     not reached it: a migration died mid-flight. Boot is refused, with
//     BackupPath naming the file to restore from.
//
// BackupPath is the whole point of writing this down: without it, "restore
// the backup" means "go and guess which of the files in backups/ was the
// one taken immediately before the migration that failed".
type MigrationMarker struct {
	From       int    `json:"from"`
	To         int    `json:"to"`
	StartedTs  int64  `json:"startedTs"`
	BackupPath string `json:"backupPath"`
}

// RestartRecord is the server-owned half of phase 5's restart state
// machine, defined here now so phase 4 owns the whole `meta` bucket layout
// and phase 5 adds fields rather than a bucket.
//
// It is deliberately empty. The state machine's states, deadlines, and
// worker respawn intents are phase 5's design to make; what phase 4 fixes
// is only *where* they will live, so that a phase-4 binary reading a
// phase-5 database finds a key it can decode and ignore rather than one it
// has never heard of.
type RestartRecord struct {
}
