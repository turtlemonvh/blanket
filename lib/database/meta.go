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

// ---------------------------------------------------------------------------
// The restart state machine (turtlemonvh/blanket#23 phase 5)
// ---------------------------------------------------------------------------

// The states one restart attempt moves through. Forward-only: a transition
// may skip states (a restart that changes no worker code never drains; a
// restart that installs no new binary never backs up or swaps), but it may
// never go backwards. `abort` is the one escape, and it goes straight to
// IDLE from anywhere.
//
// Who moves each one is the point of the split described on RestartRecord:
//
//	IDLE       no restart in flight. The absence of a record, so an old
//	           database and a settled one read identically.
//	STAGED     a restart has been announced. Nothing has changed yet; the
//	           reaper is suppressed and the deadline watchdog is armed.
//	BACKED_UP  a database backup exists, and the record names it. Reached
//	           by POST /ops/backup while a record sits at STAGED, so the
//	           record states a fact the server itself observed rather than
//	           one the caller asserted.
//	PAUSED     worker spawn is refused with 409. This closes the window in
//	           which the *old* server could still fork a worker from the
//	           *new* binary on disk: worker.Run resolves os.Executable at
//	           spawn time, so a spawn racing the swap gets a mismatched
//	           pair.
//	SWAPPED    the caller reports the binary on disk is now the new one.
//	           The only state the server cannot observe for itself, and so
//	           the only one that is purely the caller's word.
//	DRAINING   every live worker has been stopped with respawn intent
//	           recorded, in one transaction with this state change.
//	EXECING    the shutdown has begun. Whether the process re-execs in
//	           place or exits for a supervisor is ExecMode's business.
//	VERIFIED   the next process found the record, respawned what was
//	           drained, and cleared it. Transient: it is logged on the way
//	           back to IDLE, never left in the file.
const (
	RestartStateIdle     = "IDLE"
	RestartStateStaged   = "STAGED"
	RestartStateBackedUp = "BACKED_UP"
	RestartStatePaused   = "PAUSED"
	RestartStateSwapped  = "SWAPPED"
	RestartStateDraining = "DRAINING"
	RestartStateExecing  = "EXECING"
	RestartStateVerified = "VERIFIED"
)

// restartStateOrder is the forward-only ordering above. VERIFIED is not in
// it: it is reached only from the next process's boot, never by a
// transition within one process's life.
var restartStateOrder = []string{
	RestartStateIdle,
	RestartStateStaged,
	RestartStateBackedUp,
	RestartStatePaused,
	RestartStateSwapped,
	RestartStateDraining,
	RestartStateExecing,
}

// RestartStateRank reports a state's position in the forward-only order,
// or -1 for a state that has none (an unknown string, or VERIFIED). The
// empty string ranks as IDLE, which is what an absent record decodes to.
func RestartStateRank(state string) int {
	if state == "" {
		state = RestartStateIdle
	}
	for i, s := range restartStateOrder {
		if s == state {
			return i
		}
	}
	return -1
}

// Exec modes: how the EXECING step actually replaces the process
// (brief decision row 2).
//
//	auto  exit for the supervisor when there is one, re-exec in place when
//	      there is not. The default, and a heuristic — see
//	      server/restart.go's Supervised().
//	exec  always re-exec in place (phase 2's SIGUSR2 path). Refused on
//	      windows, which never self-restarts (decision row 9).
//	exit  always drain and exit, leaving the restart to whatever started
//	      this process.
const (
	ExecModeAuto = "auto"
	ExecModeExec = "exec"
	ExecModeExit = "exit"
)

// Drain modes: whether a restart stops its workers (brief decision row 3).
//
//	auto    drain only when the caller asks for it, by calling
//	        POST /ops/restart/drain. Routine restarts don't; an upgrade
//	        that changes worker code does. The default.
//	always  every restart drains, whether or not the caller asked.
//	never   drain is refused. The opt-out.
const (
	DrainModeAuto   = "auto"
	DrainModeAlways = "always"
	DrainModeNever  = "never"
)

// RestartRecord is the server-owned half of the restart state machine
// (turtlemonvh/blanket#23 phase 5). Phase 4 reserved the key and left the
// struct empty; this is what fills it.
//
// # Why the state is split in two
//
// A restart is driven from outside the server — by an operator with curl,
// or by phase 6's `blanket upgrade` — and the driver cannot open the
// database: bolt's exclusive lock belongs to the server for as long as the
// server is up, and the interesting part of a restart is precisely the
// window where the server is going away and coming back. So the state
// lives in two places, and they are read in different circumstances:
//
//   - **this record**, in the `meta` bucket, is what the *next server
//     process* reads on boot to find out what it is the continuation of;
//   - **a journal file** owned by the driver is what a *human* reads when
//     the server is down and did not come back.
//
// The journal's format is phase 6's to define, because nothing here reads
// it: the server learns everything it needs from this record plus the
// worker records, and a format defined a phase early would be a format
// defined without its only writer.
//
// # The transactional invariant
//
// Every transition is a single bolt transaction, and two facts that must
// agree are never written in two of them. The load-bearing case is the
// drain: moving to DRAINING and flipping every worker's stopped-plus-
// respawn-intent happen in one Update (BlanketDB.StopWorkersForRestart),
// because a crash between them would either stop workers nothing will ever
// bring back, or record an intent to respawn workers that were never
// stopped.
type RestartRecord struct {
	// State is one of the RestartState* constants. Empty means IDLE — an
	// absent key and a settled one decode identically.
	State string `json:"state,omitempty"`

	// Id names one restart attempt, so a log line, a status response and
	// a journal entry can be tied to the same attempt across the process
	// boundary the restart puts in the middle of them.
	Id string `json:"id,omitempty"`

	// Reason is free text from whoever began the restart ("upgrade to
	// 0.4.0", "config change"). Echoed back by GET /ops/restart/status and
	// logged by the process that finds the record on boot, which is the
	// one moment somebody is asking "what was this?".
	Reason string `json:"reason,omitempty"`

	StartedTs int64 `json:"startedTs,omitempty"`
	UpdatedTs int64 `json:"updatedTs,omitempty"`

	// DeadlineTs is when the watchdog gives up on the driver and aborts
	// the restart, in unix seconds. Refreshed by every transition, because
	// this is a watchdog and not a budget: a driver that is making
	// progress should never be timed out, and one that has been `kill -9`ed
	// must not leave the server paused forever.
	DeadlineTs int64 `json:"deadlineTs,omitempty"`

	// DeadlineSeconds is what DeadlineTs is recomputed from on each
	// transition. Carried in the record so the deadline survives into the
	// next process, which did not see the request that set it.
	DeadlineSeconds int64 `json:"deadlineSeconds,omitempty"`

	// ExecMode is the ExecMode* value this restart resolved at `begin`,
	// pinned there rather than read at exec time so the whole attempt uses
	// one answer.
	ExecMode string `json:"execMode,omitempty"`

	// BackupPath is the backup POST /ops/backup wrote during this restart,
	// if any. The same field MigrationMarker carries, for the same reason:
	// "restore the backup" is useless advice if it does not name a file.
	BackupPath string `json:"backupPath,omitempty"`

	// FromInstanceId, FromVersion and FromPid identify the process that
	// began the restart. The boot-time check that the record belongs to a
	// *previous* process is FromInstanceId != this process's instance id;
	// the other two are for the human reading the log.
	FromInstanceId string `json:"fromInstanceId,omitempty"`
	FromVersion    string `json:"fromVersion,omitempty"`
	FromPid        int    `json:"fromPid,omitempty"`

	// DrainedWorkers is how many workers the drain stopped, recorded in
	// the same transaction that stopped them.
	DrainedWorkers int `json:"drainedWorkers,omitempty"`
}

// Active reports whether a restart is in flight.
func (rr RestartRecord) Active() bool {
	return rr.State != "" && rr.State != RestartStateIdle
}

// PausesSpawn reports whether worker spawn should be refused in this
// state. Everything from PAUSED onward: once the binary on disk may have
// been swapped, a spawn resolves os.Executable to a file that no longer
// matches the server that is forking it.
func (rr RestartRecord) PausesSpawn() bool {
	return RestartStateRank(rr.State) >= RestartStateRank(RestartStatePaused)
}
