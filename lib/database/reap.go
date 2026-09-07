package database

import (
	"errors"
	"time"

	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

// ErrNoJournal is what a ReadJournalFunc returns when a task has no
// outcome journal. It is a completely ordinary condition — every task that
// finishes cleanly deletes its journal — so it is a sentinel to compare
// against rather than something to log.
var ErrNoJournal = errors.New("no outcome journal for this task")

// Reaper inputs (turtlemonvh/blanket#23 phase 3).
//
// The three cleanup routines used to take no arguments, which is why they
// stayed `return nil` stubs for years: a function that reads the wall
// clock, asks the OS about pids, and touches the filesystem cannot be
// tested at all, let alone tested cheaply. Everything they depend on is
// therefore injected here, so every row of the decision table in
// docs/task_flow.md is a deterministic table test with a fake clock, a fake
// liveness oracle, and a fake journal reader — no sleeping, no spawning,
// no real processes.
//
// The server builds one of these per pass (see server/reaper.go).

// IsAliveFunc reports whether a pid is running, and whether that answer can
// be trusted. Production wires proclive.IsAlive; tests wire a map.
//
// The second return value is the whole point: the reaper's contract is that
// it never acts on an inconclusive answer, so a platform that cannot check
// pids, a record with no start time, or a permissions failure all degrade
// to "leave it alone" instead of a guess.
type IsAliveFunc func(pid int, pidStartTs int64) (alive bool, conclusive bool)

// ReadJournalFunc loads a task's outcome journal from its result
// directory. Production wires worker.ReadOutcomeJournal. An error (in
// particular a wrapped os.ErrNotExist, the normal case for a task that
// finished cleanly) means "no journal", not "reap it".
type ReadJournalFunc func(resultDir string) (*worker.OutcomeJournal, error)

// RequeueFunc puts a task back on the claimable queue. Supplied by the
// caller because the queue is a separate abstraction from the database,
// even though bolt happens to back both with one file.
type RequeueFunc func(t *tasks.Task) error

// ReapOptions carries everything a cleanup pass needs to decide.
type ReapOptions struct {
	// Now is the clock. Never call time.Now() inside a cleanup routine.
	Now func() time.Time

	// IsAlive answers pid liveness. Required; a nil IsAlive makes every
	// liveness question inconclusive, which is the safe default.
	IsAlive IsAliveFunc

	// ReadJournal loads a task's outcome journal. Required by
	// CleanupStalledTasks: reading the real outcome is what keeps a
	// finished three-hour render from being recorded as ERROR.
	ReadJournal ReadJournalFunc

	// Requeue returns a task to the queue. Used only for CLAIMED tasks
	// that provably never started.
	Requeue RequeueFunc

	// BaselineTs is the floor every staleness measurement is taken from:
	// max(LastHeardTs, BaselineTs). The server sets it to
	// max(serverStartedTs, lastResumeTs), so that after a restart — or
	// after the machine wakes from sleep — every worker gets a full
	// staleness window to check in again, instead of all of them looking
	// stale at once because nobody could have reported during the gap.
	BaselineTs int64

	// WorkerStaleAfter is how long a worker's heartbeat may be silent
	// before it is considered stale. A stale worker with a live pid is
	// marked Lost for the UI and otherwise left completely alone.
	WorkerStaleAfter time.Duration

	// WorkerDeadAfter is the longer threshold used when pid liveness is
	// inconclusive — an unsupported platform, a record with no
	// PidStartTs, a future off-host worker. Heartbeat staleness alone is
	// weaker evidence than a conclusively dead pid, so it has to be much
	// staler before the reaper acts on it.
	WorkerDeadAfter time.Duration

	// TaskStaleAfter is how long a CLAIMED or RUNNING task may go without
	// an update before the reaper looks at it, and how long a queue entry
	// may sit claimed-but-unacked before CleanupUnclaimedTasks does.
	TaskStaleAfter time.Duration

	// MaxRequeues caps how many times one task may be requeued after its
	// worker died before it starting. Without a cap, a task that kills
	// whichever worker claims it (an OOM, a kernel panic on some device)
	// is requeued forever, taking down every worker in turn.
	MaxRequeues int
}

// ReapReport counts what a pass did. Returned rather than only logged so
// tests can assert on the decision table directly, and so the loop can log
// one line per pass instead of one per record.
type ReapReport struct {
	WorkersMarkedLost   int
	WorkersStopped      int
	TasksRecovered      int
	TasksRequeued       int
	TasksFailed         int
	QueueEntriesFreed   int
	QueueEntriesDropped int
	// LeftAlone counts records the reaper looked at and deliberately did
	// not touch because the evidence was not conclusive. A healthy install
	// has this at zero; a nonzero value in the logs is the first thing to
	// look at when a task appears stuck.
	LeftAlone int
}

// Empty reports whether a pass changed nothing at all, so the loop can skip
// logging on the overwhelmingly common quiet pass.
func (r ReapReport) Empty() bool {
	return r == ReapReport{}
}

// Add merges another report into this one.
func (r *ReapReport) Add(other ReapReport) {
	r.WorkersMarkedLost += other.WorkersMarkedLost
	r.WorkersStopped += other.WorkersStopped
	r.TasksRecovered += other.TasksRecovered
	r.TasksRequeued += other.TasksRequeued
	r.TasksFailed += other.TasksFailed
	r.QueueEntriesFreed += other.QueueEntriesFreed
	r.QueueEntriesDropped += other.QueueEntriesDropped
	r.LeftAlone += other.LeftAlone
}

// WithDefaults fills in the pieces a caller left out, always in the
// conservative direction: a missing clock is the real one, missing
// liveness is inconclusive, a missing journal reader finds no journal, and
// a zero threshold is treated as "never stale" rather than "always stale".
func (o *ReapOptions) WithDefaults() *ReapOptions {
	out := *o
	if out.Now == nil {
		out.Now = time.Now
	}
	if out.IsAlive == nil {
		out.IsAlive = func(pid int, pidStartTs int64) (bool, bool) { return false, false }
	}
	if out.ReadJournal == nil {
		out.ReadJournal = func(resultDir string) (*worker.OutcomeJournal, error) { return nil, ErrNoJournal }
	}
	return &out
}

// NowTs is the pass's clock as unix seconds, which is the unit every
// timestamp in the database is stored in.
func (o *ReapOptions) NowTs() int64 { return o.Now().Unix() }

// StaleFor returns how long ago ts was, measured from the later of ts and
// BaselineTs — the grace layer that keeps a server restart from making
// every worker look stale at once.
func (o *ReapOptions) StaleFor(ts int64) time.Duration {
	if ts < o.BaselineTs {
		ts = o.BaselineTs
	}
	d := time.Duration(o.NowTs()-ts) * time.Second
	if d < 0 {
		return 0
	}
	return d
}

// Exceeds reports whether a staleness exceeds a threshold, treating a
// non-positive threshold as "never" rather than "always". A misconfigured
// (or zero-valued) option must not turn the reaper loose on everything.
func Exceeds(staleness, threshold time.Duration) bool {
	return threshold > 0 && staleness > threshold
}
