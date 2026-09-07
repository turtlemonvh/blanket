// Package proclive answers one question — "is this pid still the process I
// started?" — and, crucially, says whether it is sure.
//
// The reaper (turtlemonvh/blanket#23 phase 3) is the consumer. Its whole
// design fails safe toward "alive": it never deletes a worker or rewrites a
// task's outcome on a guess. So every answer here comes back as a pair,
// (alive, conclusive), and an inconclusive answer means the caller must do
// nothing at all rather than pick the likelier branch.
//
// # Pid reuse
//
// A bare "does pid 4213 exist?" is not enough. Pids are recycled, quickly on
// a busy machine, so a stale pid recorded hours ago may now belong to
// something entirely unrelated — and treating that as "my worker is alive"
// keeps a dead worker's tasks stranded forever, while treating a recycled
// pid as "my worker is alive and here it is" would let the server kill an
// innocent process. Both are avoided by pairing the pid with the process's
// **start time**, which the OS records and which no later process can
// inherit. A pid that exists but started at a different moment is a
// different process, and this package reports that as conclusively dead.
//
// # Same-host only
//
// Pid liveness is meaningful only for processes on this machine. blanket's
// workers are same-host today (every call site hardcodes
// http://localhost:{port}), so this is the strongest liveness signal
// available. It is deliberately *not* the only one: the reaper also has the
// heartbeat, which is portable, and falls back to heartbeat staleness alone
// wherever this package answers "inconclusive". A future off-host worker
// would simply always take that fallback path — see docs/task_flow.md,
// "Same-host assumption".
//
// # Platforms
//
//	linux    /proc/<pid>/stat field 22 (start time in clock ticks since
//	         boot) plus /proc/stat's btime. No cgo, no shelling out.
//	darwin   kern.proc.pid.<pid> via sysctl (KinfoProc.Proc.P_starttime).
//	windows  OpenProcess + GetProcessTimes' creation time, with
//	         GetExitCodeProcess distinguishing "running" from "exited but
//	         the handle is still open".
//	other    inconclusive, always. Nothing is reaped on that platform,
//	         which is the safe direction.
package proclive

import "time"

// StartTimeTolerance is how far a re-read start time may drift from a
// recorded one and still be considered the same process.
//
// It is not zero because the recorded value round-trips through unix
// seconds while the platforms report sub-second precision (linux quantises
// to clock ticks against a boot time that itself only has second
// resolution), so a truncation either side can differ by a second in
// principle. Two seconds is comfortably inside pid-reuse timescales: a pid
// recycled within two seconds of the original starting is not a case worth
// distinguishing, and the consequence of getting it wrong here is only that
// the reaper waits for its longer inconclusive threshold instead.
const StartTimeTolerance = 2 * time.Second

// IsAlive reports whether the process identified by (pid, pidStartTs) is
// still running, and whether that answer is conclusive.
//
// pidStartTs is the process's start time in unix seconds as recorded when it
// was launched — worker.OutcomeJournal.PidStartTs for a task's child,
// worker.WorkerConf.PidStartTs for a worker. Zero means "not recorded", the
// documented encoding for a value written by a build that predates this
// package.
//
// The four outcomes:
//
//	(false, false)  nothing usable: a zero/negative pid, or the platform
//	                could not be asked. Caller must not act.
//	(false, true)   definitely gone: no such process, or the pid exists but
//	                started at a different time (reuse).
//	(true, false)   the pid exists, but reuse can't be ruled out — no start
//	                time was recorded, or it couldn't be read back. Caller
//	                must not act (this is the fail-safe-toward-alive case).
//	(true, true)    the pid exists and its start time matches.
func IsAlive(pid int, pidStartTs int64) (alive bool, conclusive bool) {
	if pid <= 0 {
		return false, false
	}

	exists, err := processExists(pid)
	if err != nil {
		return false, false
	}
	if !exists {
		// Nothing is running under this pid at all, so there is no
		// reuse question to answer.
		return false, true
	}

	if pidStartTs <= 0 {
		// Something is running under this pid, but we have no way to tell
		// whether it is *ours*. Alive, and deliberately not conclusive.
		return true, false
	}

	actual, ok := StartTime(pid)
	if !ok {
		return true, false
	}

	delta := actual - pidStartTs
	if delta < 0 {
		delta = -delta
	}
	if delta <= int64(StartTimeTolerance/time.Second) {
		return true, true
	}

	// The pid is live but belongs to a different process than the one that
	// was recorded: ours is conclusively gone.
	return false, true
}

// StartTime returns the process's start time in unix seconds, and whether
// it could be determined. Used both by IsAlive and by the worker, which
// stamps it into the outcome journal and its own record at launch so a later
// IsAlive call has something to compare against.
func StartTime(pid int) (int64, bool) {
	if pid <= 0 {
		return 0, false
	}
	return startTime(pid)
}
