package server

/*

The restart state machine, server side (turtlemonvh/blanket#23 phase 5).

Phase 2 gave the server an ordered shutdown and a SIGUSR2 that re-execs it
in place. That is enough to restart a server; it is not enough to *upgrade*
one, because an upgrade is a sequence with a driver outside the process:
stage a binary, back the database up, stop spawning workers, swap the file,
drain, restart, check it came back. Any step can be the last one, because
the machine can lose power between any two of them.

So the sequence is written down, one state at a time, and every step is an
HTTP call plus a durable record. Two properties follow, and they are the
whole design:

  - **Every transition is one transaction.** Two facts that must agree are
    never written in two of them — see lib/bolt/restart.go for the drain,
    which is the case that matters.

  - **Every state has a documented recovery.** A process that boots and
    finds a record knows what it is the continuation of, and what it owes
    the workers that record mentions. scripts/restart_machine.sh kills the
    server at each state in turn and asserts the recovery actually happens.

## What is in memory, and why any of it is

The record in the `meta` bucket is the truth. Three things are also kept in
memory, all derived from it, all set by applyRestartRecord:

  - `restartPendingFlag` — the reaper's third grace layer (phase 3). During
    a restart every worker is about to look stale at once, through no fault
    of its own.
  - `spawnPaused` — checked on the worker-spawn path, which is a request
    handler and should not take a database read to answer a question whose
    answer changes about twice a year.
  - `restartDeadlineTs` — the watchdog's, so the loop's common case (no
    restart in flight) costs nothing.

## The pause, and the window it closes

worker.Run resolves os.Executable() at spawn time. Between the moment the
binary on disk is swapped and the moment the server is replaced, the *old*
server would therefore fork workers from the *new* binary — a pairing
nobody has tested, arrived at by accident, for as long as the swap takes.
PAUSED refuses those spawns with 409. It is not a lock; it is a statement
that this server is on its way out and should not be starting anything.

## The watchdog

The driver is a separate process and can be killed. A restart that is
paused and abandoned would leave a server that cannot start a worker and
gives no reason, and the only way out would be to restart it — which is
exactly what the abandoned driver failed to do. So the record carries a
deadline, every transition refreshes it (this is a watchdog, not a budget:
a driver making progress is never timed out), and a loop aborts the restart
when it lapses. Aborting is the same code path as POST /ops/restart/abort,
including its respawn of anything the drain stopped.

*/

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/proclive"
	"github.com/turtlemonvh/blanket/lib/timing"
	"github.com/turtlemonvh/blanket/worker"
)

// Unscaled defaults for the restart machine's durations. Every one of them
// goes through lib/timing at the point of use, so a compressed test run
// moves them together and the crash-injection suite does not have to wait
// out production-sized timeouts.
const (
	// DefaultRestartDeadline is how long a restart may sit in one state
	// before the watchdog decides its driver is gone. Generous, because
	// the states it has to cover include "a human is swapping a binary by
	// hand" and "a backup of a large database is being written", and the
	// cost of being wrong is aborting a restart that was going fine.
	DefaultRestartDeadline = 5 * time.Minute

	// DefaultDrainTimeout bounds how long POST /ops/restart/drain waits
	// for the workers it stopped to actually exit. A worker finishes its
	// current task first, so this is a bound on the *wait*, never on the
	// task: hitting it returns 200 with `drained: false` and the list of
	// stragglers, and the caller decides whether to go ahead.
	DefaultDrainTimeout = 60 * time.Second

	// RestartWatchdogInterval is how often the deadline is checked.
	RestartWatchdogInterval = 5 * time.Second

	// MinRespawnInterval is the floor between two respawns of the same
	// worker. The storm this prevents is a server that dies during its own
	// boot: a supervisor restarts it every few seconds, and without this
	// each of those boots would fork the whole worker fleet again.
	MinRespawnInterval = 30 * time.Second

	// MaxRespawnAttempts is the generation cap: how many times the server
	// will try to bring one worker back before recording why it stopped
	// trying and leaving it down. A worker that cannot start is a
	// permanent condition, and retrying a permanent condition forever is
	// how a recovery mechanism becomes an outage.
	MaxRespawnAttempts = 3
)

// Exit codes the server uses to tell its supervisor what it wants.
const (
	// RestartExitCode is what --exec-mode=exit exits with.
	//
	// Not 0, and that is not a detail: the systemd unit blanket installs
	// says `Restart=on-failure` (lib/service/service.go), so a clean exit
	// is precisely the one thing a supervised server must not do when it
	// wants to come back. 75 is EX_TEMPFAIL — "the operation failed, try
	// again" — which is exactly the request being made, and which launchd's
	// KeepAlive and any `Restart=on-failure`/`always` policy honour.
	RestartExitCode = 75

	// CrashInjectExitCode is what the BLANKET_TEST_CRASH_AT hook exits
	// with. Distinct from every other code the process can produce, so a
	// test can tell an injected crash from a real failure.
	CrashInjectExitCode = 97
)

// CrashAtEnv names the state at which the server should die immediately
// after committing the transition into it. The crash-injection suite
// (scripts/restart_machine.sh) parametrizes over every state with it.
//
// This is a test hook compiled into the production binary, which is a real
// (if small) cost, taken deliberately. The property being tested — "a
// process that dies exactly here recovers like this" — is not observable
// any other way: it needs a real process, killed at a point that is inside
// a transaction boundary rather than at any wall-clock moment a test could
// aim `kill -9` at. The mitigations are that it does nothing unless the
// variable names a state, and that a server started with it set says so at
// warn level on every boot.
const CrashAtEnv = "BLANKET_TEST_CRASH_AT"

// RestartStateError is returned when a transition is refused because the
// record is not where the caller thought it was. Surfaces as 409.
type RestartStateError struct {
	Current   string
	Requested string
	Detail    string
}

func (e *RestartStateError) Error() string {
	cur := e.Current
	if cur == "" {
		cur = database.RestartStateIdle
	}
	if e.Detail != "" {
		return fmt.Sprintf("cannot move a restart from %s to %s: %s", cur, e.Requested, e.Detail)
	}
	return fmt.Sprintf("cannot move a restart from %s to %s; transitions are forward-only", cur, e.Requested)
}

// ---------------------------------------------------------------------------
// The in-memory projection of the record
// ---------------------------------------------------------------------------

// applyRestartRecord updates everything that is derived from the record:
// the reaper's suppression flag, the spawn pause, and the watchdog's
// deadline. Called from every transition and from boot, so the three can
// never drift from the record or from each other.
func (s *ServerConfig) applyRestartRecord(rr database.RestartRecord) {
	s.restartMu.Lock()
	s.restartPendingFlag = rr.Active()
	s.spawnPaused = rr.Active() && rr.PausesSpawn()
	s.restartDeadlineTs = 0
	if rr.Active() {
		s.restartDeadlineTs = rr.DeadlineTs
	}
	s.restartMu.Unlock()
}

// spawnIsPaused reports whether worker spawn is currently refused.
func (s *ServerConfig) spawnIsPaused() bool {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	return s.spawnPaused
}

// restartDeadlinePassed reports whether a restart is in flight and its
// deadline has lapsed. The watchdog's fast path: no lock contention, no
// database read, in the overwhelmingly common case of no restart at all.
func (s *ServerConfig) restartDeadlinePassed(now time.Time) bool {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	return s.restartDeadlineTs > 0 && now.Unix() > s.restartDeadlineTs
}

// ---------------------------------------------------------------------------
// Resolving the configured modes
// ---------------------------------------------------------------------------

// Supervised reports whether this process looks like it is running under
// something that will start it again if it exits.
//
// It is a heuristic and is documented as one. systemd is the case that
// matters and is the case it is certain about: INVOCATION_ID has been set
// in every service's environment since systemd 232, and JOURNAL_STREAM
// since 231. NOTIFY_SOCKET and LISTEN_PID cover the socket-activated and
// notify-type units. macOS launchd sets XPC_SERVICE_NAME to the job label
// (a plain login shell gets the literal "0").
//
// BLANKET_SUPERVISED overrides the lot, for the deployments this cannot
// know about — a container with an entrypoint that loops, a process
// manager nobody here has heard of. So does --exec-mode, which is the
// answer when a heuristic is not wanted at all.
func Supervised() bool {
	if v := os.Getenv("BLANKET_SUPERVISED"); v != "" {
		return v != "0" && !strings.EqualFold(v, "false")
	}
	for _, k := range []string{"INVOCATION_ID", "JOURNAL_STREAM", "NOTIFY_SOCKET", "LISTEN_PID"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	if runtime.GOOS == "darwin" {
		if v := os.Getenv("XPC_SERVICE_NAME"); v != "" && v != "0" {
			return true
		}
	}
	return false
}

// resolveExecMode turns a requested mode into one of exec/exit.
//
// `auto` is decision row 2 of the design brief: under a supervisor, exit
// and let it do the restarting, because a supervised process that re-execs
// itself is invisible to the thing that is supposed to be managing it and
// escapes whatever the supervisor would have done about a bad new binary.
// Unsupervised, re-exec in place, because nothing else would bring it back.
//
// Windows never self-restarts whatever it is asked for (brief decision row
// 9): a detached replacement escapes a service's job object and holds the
// bolt lock invisibly, which is the worst failure this system has. There,
// `exec` degrades to `exit` and the upgrade CLI is the one that starts the
// server again.
func resolveExecMode(requested string) string {
	mode := strings.ToLower(strings.TrimSpace(requested))
	switch mode {
	case database.ExecModeExec, database.ExecModeExit:
	case "", database.ExecModeAuto:
		if Supervised() {
			mode = database.ExecModeExit
		} else {
			mode = database.ExecModeExec
		}
	default:
		log.WithField("execMode", requested).Warn("unknown exec mode; treating it as auto")
		return resolveExecMode(database.ExecModeAuto)
	}

	if mode == database.ExecModeExec && runtime.GOOS == "windows" {
		log.Warn("windows never self-restarts; exiting for the upgrade CLI to start the server again")
		mode = database.ExecModeExit
	}
	return mode
}

// execMode is the configured default, before a request overrides it.
func (s *ServerConfig) execMode() string {
	if s.ExecMode == "" {
		return database.ExecModeAuto
	}
	return s.ExecMode
}

// drainMode is the configured drain policy (brief decision row 3).
func (s *ServerConfig) drainMode() string {
	if s.DrainMode == "" {
		return database.DrainModeAuto
	}
	return strings.ToLower(s.DrainMode)
}

func (s *ServerConfig) drainTimeout() time.Duration {
	return timing.Scale(orDefault(s.DrainTimeout, DefaultDrainTimeout))
}

func (s *ServerConfig) restartDeadline() time.Duration {
	return timing.Scale(orDefault(s.RestartDeadline, DefaultRestartDeadline))
}

// ---------------------------------------------------------------------------
// Transitions
// ---------------------------------------------------------------------------

// stampTransition applies the bookkeeping every transition shares: the
// forward-only check, the new state, and the refreshed deadline.
//
// Refreshing on every transition is what makes the deadline a watchdog
// rather than a budget for the whole restart. A driver that is still
// calling in is, by definition, not the driver this exists to notice.
func (s *ServerConfig) stampTransition(rr *database.RestartRecord, to string) error {
	if database.RestartStateRank(to) <= database.RestartStateRank(rr.State) {
		return &RestartStateError{Current: rr.State, Requested: to}
	}
	rr.State = to
	now := time.Now()
	rr.UpdatedTs = now.Unix()
	if rr.DeadlineSeconds <= 0 {
		rr.DeadlineSeconds = int64(s.restartDeadline() / time.Second)
	}
	rr.DeadlineTs = now.Unix() + rr.DeadlineSeconds
	return nil
}

// BeginRestartOptions is what POST /ops/restart/begin accepts.
type BeginRestartOptions struct {
	Reason string `json:"reason"`
	// ExecMode overrides the server's configured --exec-mode for this
	// restart only. Resolved and pinned here rather than read at exec
	// time, so one attempt cannot answer the question two different ways.
	ExecMode string `json:"execMode"`
	// DeadlineSeconds overrides the watchdog's deadline for this restart.
	DeadlineSeconds int64 `json:"deadlineSeconds"`
}

// beginRestart opens a restart record at STAGED.
func (s *ServerConfig) beginRestart(opts BeginRestartOptions) (database.RestartRecord, error) {
	mode := opts.ExecMode
	if mode == "" {
		mode = s.execMode()
	}
	resolved := resolveExecMode(mode)

	rr, err := s.DB.UpdateRestartRecord(func(rr *database.RestartRecord) error {
		if rr.Active() {
			return &RestartStateError{
				Current:   rr.State,
				Requested: database.RestartStateStaged,
				Detail:    "a restart is already in flight; abort it first",
			}
		}
		*rr = database.RestartRecord{
			Id:              objectid.NewObjectId().Hex(),
			Reason:          opts.Reason,
			StartedTs:       time.Now().Unix(),
			DeadlineSeconds: opts.DeadlineSeconds,
			ExecMode:        resolved,
			FromInstanceId:  s.InstanceId(),
			FromVersion:     s.Version,
			FromPid:         os.Getpid(),
		}
		return s.stampTransition(rr, database.RestartStateStaged)
	})
	if err != nil {
		return rr, err
	}
	s.applyRestartRecord(rr)
	log.WithFields(log.Fields{
		"restartId": rr.Id,
		"reason":    rr.Reason,
		"execMode":  rr.ExecMode,
		"deadline":  time.Unix(rr.DeadlineTs, 0).Format(time.RFC3339),
	}).Warn("restart: staged")
	maybeCrashAt(rr.State)
	return rr, nil
}

// advanceRestart moves the record to `to` without any side effect beyond
// the record itself. BACKED_UP, PAUSED and SWAPPED all use it; what
// distinguishes them is what the *server* then refuses to do (spawn), which
// applyRestartRecord derives from the state rather than each caller
// remembering to set a flag.
func (s *ServerConfig) advanceRestart(to string, mutate func(*database.RestartRecord)) (database.RestartRecord, error) {
	rr, err := s.DB.UpdateRestartRecord(func(rr *database.RestartRecord) error {
		if !rr.Active() {
			return &RestartStateError{
				Current:   database.RestartStateIdle,
				Requested: to,
				Detail:    "no restart is in flight; POST /ops/restart/begin first",
			}
		}
		if err := s.stampTransition(rr, to); err != nil {
			return err
		}
		if mutate != nil {
			mutate(rr)
		}
		return nil
	})
	if err != nil {
		return rr, err
	}
	s.applyRestartRecord(rr)
	log.WithFields(log.Fields{"restartId": rr.Id, "state": rr.State}).Warn("restart: state changed")
	maybeCrashAt(rr.State)
	return rr, nil
}

// noteBackupForRestart records a backup against an in-flight restart, if
// there is one. Called by POST /ops/backup.
//
// This is why BACKED_UP is not an endpoint of its own: the server took the
// backup and knows the path, so the record can state a fact rather than
// repeat a claim. A backup taken with no restart in flight is an ordinary
// backup and changes nothing.
func (s *ServerConfig) noteBackupForRestart(path string) {
	rr, err := s.DB.RestartRecord()
	if err != nil || !rr.Active() {
		return
	}
	if database.RestartStateRank(rr.State) >= database.RestartStateRank(database.RestartStateBackedUp) {
		// Past this point in the sequence; record the path, leave the
		// state alone.
		updated, err := s.DB.UpdateRestartRecord(func(rr *database.RestartRecord) error {
			rr.BackupPath = path
			return nil
		})
		if err == nil {
			s.applyRestartRecord(updated)
		}
		return
	}
	if _, err := s.advanceRestart(database.RestartStateBackedUp, func(rr *database.RestartRecord) {
		rr.BackupPath = path
	}); err != nil {
		log.WithField("err", err).Warn("restart: could not record the backup against the in-flight restart")
	}
}

// DrainResult is what POST /ops/restart/drain answers with.
type DrainResult struct {
	Record       database.RestartRecord `json:"restart"`
	Stopped      []string               `json:"stopped"`
	StillRunning []string               `json:"stillRunning"`
	Drained      bool                   `json:"drained"`
	WaitedMs     int64                  `json:"waitedMs"`
}

// drainRestart stops every running worker with a respawn intent and,
// optionally, waits for those processes to actually go away.
//
// The stop and the state change are one transaction (see
// lib/bolt/restart.go). Everything after it — signalling, waiting — is
// best-effort on top of a record that is already correct.
func (s *ServerConfig) drainRestart(ctx context.Context, wait bool) (DrainResult, error) {
	if s.drainMode() == database.DrainModeNever {
		return DrainResult{}, &RestartStateError{
			Requested: database.RestartStateDraining,
			Detail:    "draining is disabled by drain-mode=never",
		}
	}

	rr, stopped, err := s.DB.StopWorkersForRestart(worker.StopReasonRestart, func(rr *database.RestartRecord) error {
		if !rr.Active() {
			return &RestartStateError{
				Current:   database.RestartStateIdle,
				Requested: database.RestartStateDraining,
				Detail:    "no restart is in flight; POST /ops/restart/begin first",
			}
		}
		return s.stampTransition(rr, database.RestartStateDraining)
	})
	if err != nil {
		return DrainResult{}, err
	}
	s.applyRestartRecord(rr)
	s.WorkerEvents.Notify()

	res := DrainResult{Record: rr, Drained: true}
	for _, w := range stopped {
		res.Stopped = append(res.Stopped, w.Id.Hex())
	}
	log.WithFields(log.Fields{
		"restartId": rr.Id,
		"workers":   len(stopped),
		"wait":      wait,
	}).Warn("restart: draining workers")
	maybeCrashAt(rr.State)

	if wait && len(stopped) > 0 {
		started := time.Now()
		res.StillRunning = s.waitForDrain(ctx, stopped)
		res.Drained = len(res.StillRunning) == 0
		res.WaitedMs = time.Since(started).Milliseconds()
	}
	return res, nil
}

// waitForDrain polls until every worker in ws has demonstrably exited, or
// the drain timeout runs out. Returns the ids still running.
//
// "Demonstrably exited" is two independent signals, either of which is
// enough, because neither is available everywhere:
//
//   - the worker told us so. Its shutdown handler calls
//     PUT /worker/:id/stop?reason=worker-shutdown, so that reason on the
//     record is the worker's own last word. This is the only signal
//     available on a platform where pid liveness cannot answer.
//   - pid liveness says the process is conclusively gone
//     (lib/proclive, pid paired with its start time so a recycled pid
//     cannot masquerade as the old one).
func (s *ServerConfig) waitForDrain(ctx context.Context, ws []worker.WorkerConf) []string {
	deadline := time.Now().Add(s.drainTimeout())
	poll := timing.Scale(250 * time.Millisecond)

	for {
		var remaining []string
		for _, w := range ws {
			if s.workerHasExited(w) {
				continue
			}
			remaining = append(remaining, w.Id.Hex())
		}
		if len(remaining) == 0 || time.Now().After(deadline) {
			return remaining
		}
		select {
		case <-ctx.Done():
			return remaining
		case <-time.After(poll):
		}
	}
}

func (s *ServerConfig) workerHasExited(w worker.WorkerConf) bool {
	if cur, err := s.DB.GetWorker(w.Id); err == nil {
		if cur.StoppedReason == worker.StopReasonSelf {
			return true
		}
		w = cur
	}
	alive, conclusive := proclive.IsAlive(w.Pid, w.PidStartTs)
	return !alive && conclusive
}

// abortRestart tears a restart down and puts the box back where it was.
//
// Used by POST /ops/restart/abort and by the watchdog, which is the point:
// there is one recovery path, so the one a human triggers is the one that
// has been exercised by every watchdog timeout.
func (s *ServerConfig) abortRestart(reason string) (database.RestartRecord, error) {
	var was database.RestartRecord
	rr, err := s.DB.UpdateRestartRecord(func(rr *database.RestartRecord) error {
		was = *rr
		if !rr.Active() {
			return &RestartStateError{
				Current:   database.RestartStateIdle,
				Requested: database.RestartStateIdle,
				Detail:    "no restart is in flight",
			}
		}
		rr.State = database.RestartStateIdle
		return nil
	})
	if err != nil {
		return rr, err
	}
	s.applyRestartRecord(rr)

	log.WithFields(log.Fields{
		"restartId": was.Id,
		"wasState":  was.State,
		"reason":    reason,
	}).Warn("restart: aborted")

	// A drain that has happened has to be undone, or aborting would be a
	// worse outcome than the restart it is rescuing us from: workers
	// stopped, and now nothing left that intends to bring them back.
	if database.RestartStateRank(was.State) >= database.RestartStateRank(database.RestartStateDraining) {
		s.respawnWorkers("abort")
	}
	return was, nil
}

// ---------------------------------------------------------------------------
// Boot: adopting a record the previous process left behind
// ---------------------------------------------------------------------------

// adoptRestartRecordOnBoot reconciles whatever the last process left in the
// `meta` bucket. Called from Serve, before anything can observe the server
// as up.
//
// It always clears the record, whatever state it was in, and that is the
// single most important line in this file. A boot is the strongest evidence
// obtainable that the process which wrote the record is gone: it held the
// bolt lock, and we have it. Keeping the record — staying paused because
// somebody paused us before the power went out — would mean a crashed
// driver could leave an install unable to start a worker, permanently,
// with the only remedy being the restart that failed in the first place.
//
// Clearing it is not "forgetting". The part that must survive is the
// obligation to specific workers, and that lives on the worker records as
// respawn intent, which this deliberately does not touch. The record is the
// plan; the intents are the debt.
func (s *ServerConfig) adoptRestartRecordOnBoot() {
	if s.DB == nil {
		return
	}
	rr, err := s.DB.RestartRecord()
	if err != nil {
		log.WithField("err", err).Warn("restart: could not read the restart record at boot")
		return
	}
	if !rr.Active() {
		s.applyRestartRecord(rr)
		return
	}

	sameProcess := rr.FromInstanceId == s.InstanceId()
	log.WithFields(log.Fields{
		"restartId":      rr.Id,
		"wasState":       rr.State,
		"reason":         rr.Reason,
		"fromVersion":    rr.FromVersion,
		"fromPid":        rr.FromPid,
		"backupPath":     rr.BackupPath,
		"drainedWorkers": rr.DrainedWorkers,
		"state":          database.RestartStateVerified,
	}).Warn("restart: found a restart record from a previous process; completing it")

	if sameProcess {
		// Cannot happen through the normal paths (the instance id is
		// generated per process), and would mean the record survived into
		// a process that believes it wrote it. Worth a line rather than a
		// silent clear.
		log.Warn("restart: the record names this very process; clearing it anyway")
	}

	cleared, err := s.DB.UpdateRestartRecord(func(rr *database.RestartRecord) error {
		rr.State = database.RestartStateIdle
		return nil
	})
	if err != nil {
		log.WithField("err", err).Warn("restart: could not clear the restart record at boot")
		return
	}
	s.applyRestartRecord(cleared)
	maybeCrashAt(database.RestartStateVerified)
}

// ---------------------------------------------------------------------------
// Respawn
// ---------------------------------------------------------------------------

// respawnWorkers brings back every worker carrying a respawn intent.
//
// Called once after the listener is up (a worker registers itself over
// HTTP, so there is nothing for it to register with before then) and again
// by abortRestart. `trigger` is only for the log.
func (s *ServerConfig) respawnWorkers(trigger string) {
	if s.DB == nil {
		return
	}
	ws, err := s.DB.GetWorkers()
	if err != nil {
		log.WithField("err", err).Warn("restart: could not list workers to respawn")
		return
	}
	for _, w := range ws {
		if !w.RespawnIntent {
			continue
		}
		s.respawnWorker(w, trigger)
	}
}

// respawnWorker runs the three storm guards over one worker and, if they
// all pass, brings it back.
//
// The guards are ordered cheapest-and-most-conclusive first:
//
//  1. **pid liveness.** The worker is already running — which is the
//     normal outcome of the at-least-once design, when the server died
//     after spawning but before clearing the intent. Clear and move on;
//     spawning a second process for one worker id is the one outcome
//     worse than not spawning at all.
//  2. **the generation cap.** This worker has been tried enough times.
//     Record why and leave it down, so the operator finds a reason on the
//     record rather than a worker that is mysteriously absent.
//  3. **the minimum interval.** It was tried very recently, so this is a
//     server restarting in a tight loop rather than a restart completing.
//     Leave the intent alone and let a later boot try.
func (s *ServerConfig) respawnWorker(w worker.WorkerConf, trigger string) {
	fields := log.Fields{"workerId": w.Id.Hex(), "trigger": trigger, "attempts": w.RespawnAttempts}

	if alive, _ := proclive.IsAlive(w.Pid, w.PidStartTs); alive {
		log.WithFields(fields).Info("restart: worker is already running; clearing its respawn intent")
		if _, err := s.DB.ClearWorkerRespawn(w.Id, ""); err != nil {
			log.WithFields(fields).WithField("err", err).Warn("restart: could not clear a respawn intent")
		}
		return
	}

	if w.RespawnAttempts >= MaxRespawnAttempts {
		reason := fmt.Sprintf("restart: gave up respawning after %d attempts", w.RespawnAttempts)
		log.WithFields(fields).Error("restart: respawn attempt cap reached; leaving this worker stopped")
		if _, err := s.DB.ClearWorkerRespawn(w.Id, reason); err != nil {
			log.WithFields(fields).WithField("err", err).Warn("restart: could not record the respawn give-up")
		}
		s.WorkerEvents.Notify()
		return
	}

	if w.LastRespawnTs > 0 {
		since := time.Since(time.Unix(w.LastRespawnTs, 0))
		if since < timing.Scale(MinRespawnInterval) {
			log.WithFields(fields).WithField("since", since.String()).
				Warn("restart: respawned too recently; leaving the intent for a later pass")
			return
		}
	}

	claimed, err := s.DB.ClaimWorkerRespawn(w.Id)
	if err != nil {
		log.WithFields(fields).WithField("err", err).Warn("restart: could not claim a respawn")
		return
	}
	if !claimed.RespawnIntent {
		// Cleared underneath us between the listing and the claim: an
		// operator stopped this worker explicitly. They win.
		log.WithFields(fields).Info("restart: respawn intent was cleared before the spawn; leaving the worker stopped")
		return
	}

	if _, err := s.spawnRespawnedWorker(&claimed); err != nil {
		// The attempt is already counted (ClaimWorkerRespawn counts before
		// the fork), so the cap still converges even if this failure is
		// the kind that takes the server with it. The intent stays set on
		// purpose: a later boot tries again, up to the cap.
		log.WithFields(fields).WithField("err", err).Error("restart: respawn failed; the intent stays set for a later attempt")
		return
	}

	// Only now. See worker.WorkerConf.RespawnIntent for why this is the
	// direction to be wrong in.
	if _, err := s.DB.ClearWorkerRespawn(claimed.Id, ""); err != nil {
		log.WithFields(fields).WithField("err", err).Warn("restart: worker respawned but its intent could not be cleared")
	}
	log.WithFields(fields).Warn("restart: worker respawned")
	s.WorkerEvents.Notify()
}

// spawnRespawnedWorker forks the worker daemon, or calls the test hook.
//
// The hook exists because the real path resolves os.Executable() and forks
// it, which under `go test` is the test binary. Everything worth testing
// about respawn is the decision — which workers, how often, and when to
// stop trying — and that decision is what the hook leaves in place.
func (s *ServerConfig) spawnRespawnedWorker(w *worker.WorkerConf) (worker.WorkerConf, error) {
	if s.spawnWorkerFn != nil {
		return s.spawnWorkerFn(w)
	}
	return s.launchWorkerAndWait(context.Background(), w)
}

// ---------------------------------------------------------------------------
// The watchdog
// ---------------------------------------------------------------------------

// restartWatchdogLoop aborts a restart whose driver has stopped calling in.
// Started by startBackgroundLoops and cancelled at shutdown step (e), like
// the scheduler and the reaper.
func (s *ServerConfig) restartWatchdogLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if !s.restartDeadlinePassed(now) {
				continue
			}
			if _, err := s.abortRestart("the restart deadline passed with no further progress; its driver is presumed gone"); err != nil {
				var stateErr *RestartStateError
				if !errors.As(err, &stateErr) {
					log.WithField("err", err).Warn("restart: watchdog could not abort the lapsed restart")
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Crash injection
// ---------------------------------------------------------------------------

// maybeCrashAt exits immediately if BLANKET_TEST_CRASH_AT names `state`.
// Call it *after* the transition's transaction has committed, so what the
// next process finds is the state that was reached rather than the one
// before it.
func maybeCrashAt(state string) {
	want := os.Getenv(CrashAtEnv)
	if want == "" || !strings.EqualFold(strings.TrimSpace(want), state) {
		return
	}
	log.WithFields(log.Fields{
		"state": state,
		"env":   CrashAtEnv,
	}).Error("restart: crash injection point reached; exiting hard")
	os.Exit(CrashInjectExitCode)
}

// warnIfCrashInjectionArmed says so, loudly, once per boot. A production
// server with this set would die at its next restart for no visible
// reason; one line in the log is the difference between that and a
// five-minute mystery.
func warnIfCrashInjectionArmed() {
	if v := os.Getenv(CrashAtEnv); v != "" {
		log.WithFields(log.Fields{
			"state": v,
			"env":   CrashAtEnv,
		}).Warn("restart: crash injection is armed; this server will exit on reaching that restart state")
	}
}

// restartStatusCode maps a state-machine error onto an HTTP status. A
// refused transition is a conflict, not a server fault.
func restartStatusCode(err error) int {
	var stateErr *RestartStateError
	if errors.As(err, &stateErr) {
		return http.StatusConflict
	}
	return statusForDBError(err, http.StatusInternalServerError)
}

// processPid is os.Getpid, named so the handlers read as descriptions of
// what they report rather than as syscalls.
func processPid() int { return os.Getpid() }

// execModeExitCode is the exit code a resolved exec mode will produce.
// Zero for `exec`, which never exits: syscall.Exec replaces the process
// image, so the pid the caller is watching stays put and simply starts
// answering again.
func execModeExitCode(mode string) int {
	if mode == database.ExecModeExit {
		return RestartExitCode
	}
	return 0
}
