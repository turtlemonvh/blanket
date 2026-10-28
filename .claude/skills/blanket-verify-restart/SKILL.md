---
name: blanket-verify-restart
description: Use when a change touches blanket's restart state machine, worker respawn, the drain, the reaper's handling of stopped workers, or worker stop/restart — server/restart.go, server/serve_restart.go, lib/bolt/restart.go, the worker parts of lib/bolt/database.go, server/serve_workers.go, worker/worker.go's claim loop. Model-checks the change against five rules (no worker silently lost, no respawn debt left unpaid, one process per worker id, an operator stop wins, no old-binary worker after an upgrade). Triggers on "check the restart model", "does this break respawn", or any edit to the files above.
---

# blanket-verify-restart

`formal/restart/` holds a model of the server side of the restart state
machine: the `/ops/restart/*` transitions, the drain, abort and the
watchdog, boot, the respawn pass, worker processes, operator stop/restart
and the reaper, with the server able to crash between any two steps. A
model checker walks **every** ordering of those steps up to small bounds
and checks five rules in every state. A test checks the orderings someone
thought of; this checks all of them.

You do not need to know TLA+ to use this. You do need to keep the model
true to the code, which is most of this file.

## 1. Does your change touch the model?

The model covers these functions (at the sync commit below). If your diff
touches one, or adds code that writes the same worker or restart-record
fields, the model needs a look.

| Model action (in `BlanketRestart.tla` and `check.py`) | Code |
| --- | --- |
| `Begin`, `Advance`, `ExecCommit` | `server/restart.go` `beginRestart`, `advanceRestart`, `stampTransition`, `noteBackupForRestart`; `server/serve_restart.go` `opsRestartExec` |
| `Drain` | `server/restart.go` `drainRestart`; `lib/bolt/restart.go` `StopWorkersForRestart` |
| `Abort` (also the watchdog) | `server/restart.go` `abortRestart`, `restartWatchdogLoop` |
| `Teardown`, `Crash`, `Boot` | `server/lifecycle.go` `Serve`, `serveListener`, `handleRestart`; `server/restart.go` `adoptRestartRecordOnBoot` |
| `PassPick`/`PassGuard`/`PassClaim`/`PassSpawn`/`PassWait` | `server/restart.go` `respawnWorkers`, `respawnWorker` (and `ServerConfig.respawnMu`); `lib/bolt/restart.go` `ClaimWorkerRespawn`, `RecordWorkerSpawn`, `SettleWorkerRespawn`, `UndrainWorker`, `ClearWorkerRespawn`; `server/serve_workers.go` `launchWorkerAndWait`; `worker/worker.go` `Running`, `Registering`, `StillDrained`, `Run` (sets `SpawnPid`) |
| `PeriodicPass` | `server/restart.go` `respawnRetryLoop`; `server/scheduler.go` `startBackgroundLoops`; `restart.respawnRetryInterval` |
| `Register`, `RegisterGiveUp`, `Poll`, `TaskDone` | `worker/worker.go` `MustRegister`, `ProcessTasks`; `lib/bolt/database.go` `UpdateWorker` (the field merge) |
| `OpStop` | `server/serve_workers.go` `stopWorker`; `lib/bolt/database.go` `StopWorker` |
| `OpRestart` | `server/serve_workers.go` `restartWorker` (the 409 and `respawnMu`); `lib/bolt/database.go` `StartWorker` |
| `Reap` | `lib/bolt/reap.go` `CleanupStalledWorkers`; `server/reaper.go` (the restart-pending skip) |
| `Tick` | `MinRespawnInterval` in `server/restart.go` |

**Sync commit: `14c9767`.** The model was last confirmed to describe the
code at that commit. To see what has changed since:

```
git diff 14c9767 -- server/restart.go server/serve_restart.go server/lifecycle.go \
  server/serve_workers.go server/reaper.go server/scheduler.go lib/bolt/restart.go \
  lib/bolt/database.go lib/bolt/reap.go worker/worker.go
```

## 2. Run it

```
python3 formal/restart/check.py              # the code as it is (about 2s)
python3 formal/restart/check.py --pre-278    # the code before #278, for contrast (about 15s)
```

Python 3 standard library only. It prints each rule as `[holds]` or
`[BROKEN]` with the shortest sequence of steps that breaks it, in the
code's words, and exits 1 if anything is broken.

At the sync commit the expected result is:

- the code as it is: all five hold; 207,403 distinct states.
- `--pre-278`: `NoLostWorker`, `DebtPaid`, `NoDuplicateWorker` and
  `NoOldWorkerAfterUpgrade` BROKEN, `OperatorStopWins` holds; 966,990
  distinct states. This is the code before turtlemonvh/blanket#278 (see
  "History"); it stays in the model as proof that the rules can fail.

Wider bounds, for a change you are less sure of (minutes, and memory):

```
python3 formal/restart/check.py --max-procs 4 --max-crashes 2 --max-ops 2 --spawn-can-fail
python3 formal/restart/check.py --nw 2 --max-restarts 1
python3 formal/restart/check.py --max-attempts 3 --spawn-can-fail
```

### With TLC (optional, the reference checker)

`BlanketRestart.tla` is the spec; `check.py` is a transliteration of it.
With Java and `tla2tools.jar` (github.com/tlaplus/tlaplus releases, v1.8.0
used here; keep the jar out of the repo):

```
cd formal/restart
java -XX:+UseParallelGC -cp /path/to/tla2tools.jar tlc2.TLC -workers auto -config current.cfg BlanketRestart.tla
```

The distinct-state count must equal `check.py`'s on the same constants
(207,403; and 966,990 for `pre278.cfg`). If they differ, the two files have
drifted: fix that before trusting either. TLC counts the initial state as
step 1, so its traces are one step longer than `check.py`'s.

For `pre278.cfg`, or any run that breaks a rule, keep one rule in
`INVARIANTS` at a time. `-continue` prints every violating trace, and on
`pre278.cfg` that is gigabytes. Both cfgs set `CHECK_DEADLOCK FALSE`:
with bounded counters a run that stops is normal (every counter spent, the
only worker stopped by an operator), not a bug. For a TLC counterexample, the pstack
`/statechart` skill turns it into a chart and a numbered story.

## 3. A rule broke: bug or modeling error?

Replay the counterexample against the real code, one model step per code
step, before calling it either. The patterns already used for this:

- `server/restart_model_test.go`-style Go tests drive the HTTP routes and the
  boot sequence (`adoptRestartRecordOnBoot` then `respawnWorkers`) and
  assert only what `GET /worker/:id` returns. A worker that is "still
  running" is the test process's own pid and start time
  (`selfPidAndStart`).
- A live run with the real binary, a real worker and a `sleep` task, on
  the `scripts/test/lib/harness.sh` harness.

Ready-made breaks: `--mutate stop-keeps-intent` is `StopWorker` no longer
clearing the intent (breaks `OperatorStopWins`); `--mutate drain-two-tx`
splits the drain into two transactions. The second breaks `DebtPaid` only
with `--pre-278`: on the current code the periodic pass repays a debt the
split leaves behind, so none of the five rules can see it. If your change is
one of these, run the flag instead of editing both files.

If a step cannot happen in the code, the model is wrong: fix the model and
say what was wrong. If every step can happen, it is a bug: report the steps
in plain words and the harm in one sentence. **Never weaken a rule or the
model to make a check pass.** The rules are below; each is one sentence a
person agreed to.

| Rule | Plain words |
| --- | --- |
| `NoLostWorker` | Once things settle, every worker nobody chose to stop is running, still owed a respawn, or stopped with a reason saying it is not coming back. |
| `DebtPaid` | A respawn the restart owes is paid while the server is up, not at some future restart. |
| `NoDuplicateWorker` | One worker id is never run by two processes at once. |
| `OperatorStopWins` | A worker an operator stopped stays stopped. |
| `NoOldWorkerAfterUpgrade` | After an upgrade that drained settles, no worker still runs the old binary. |

If it is a bug, revert your model edits (the model describes the code you
want, not the bug), report the counterexample, and leave the sync commit
alone.

The Go tests for the counterexamples this model has already found are in
`server/restart_model_test.go`. A new finding that turns out to be a bug
gets a test there, written the same way.

## 4. Your change was intended: update the model

1. Change the matching action so it does what the code now does. One
   transaction (`db.Update`) or one uninterruptible process step is one
   action; do not merge two transactions into one action unless the
   comment says why that cannot hide a bug you care about.
2. Make the same change in **both** `BlanketRestart.tla` and `check.py`.
   They must stay action-for-action identical.
3. Keep the code reference comment (`file.go:line`) on each condition.
4. Rerun. If a rule now passes that used to fail, break your change on
   purpose (remove the new check, reorder the new step), confirm the rule
   catches it, then put it back. A check never seen failing proves nothing.
   `--mutate` has two ready-made breaks for this. Do the same for the Go
   tests, and make sure the break compiles: a test run that fails to build
   reports no `--- FAIL` lines.
5. Change a rule only when the promise changed on purpose, with evidence
   (docs/upgrade.md, a test, the commit message). Write the old and new
   wording in the PR.
6. Update the table in section 1 and the expected results in section 2,
   and move the sync commit forward **only** after you have confirmed the
   model describes the code at the new commit.

## What the model leaves out

Say these when you report a pass. A pass covers the model at its bounds,
not blanket.

- Bounds: one worker (two with `--nw 2`), up to 3 or 4 worker processes,
  2 restarts, 1 or 2 server crashes, 1 or 2 operator actions.
  `MaxAttempts` is 2 by default (the code's cap is 3; `--max-attempts 3`).
- Time is abstract. Timing assumptions are written into `ReapEnabled`
  (workerDeadAfter 10m outlasts RegisterRetryDeadline 30s and the 5s
  spawn wait) and `Tick` (30s MinRespawnInterval). Change a timeout and
  these may stop holding.
- Pid liveness is assumed conclusive (`lib/proclive` with a start time).
- Not modelled: tasks and the queue, the scheduler, the upgrade CLI and
  its journal, binary swap mechanics and rollback slots, migrations,
  `--exec-mode`/`--drain-mode` other than auto, Windows.
- Two respawn passes may overlap only as one boot-or-periodic pass plus one
  abort pass. The code can run boot, periodic and abort passes at once, but
  every per-worker decision is under `respawnMu` and re-reads the record,
  so a third pass only adds more interleavings of the same whole steps.
- A worker that sees `Stopped` exits in the same step (`Poll`). In the code
  there is a moment between the two (a log line and `os.Exit`). That is why
  abort's un-drain keeps the intent: a worker that exits anyway is
  respawned by the next pass.
- Two merges, each commented in the spec: the fork and `RecordWorkerSpawn`
  (`PassSpawn`), and `StartWorker` and the fork (`OpRestart`). A crash
  between the two halves is not modelled.
- `OpStop` is only enabled on a running worker. The code also lets an
  operator stop an already-stopped (for example just drained) worker,
  which is the natural way to say "don't bring this one back"; the model
  cannot reach that path.

## History

The model was written against `216b256`, where four rules broke
(turtlemonvh/blanket#278). `--pre-278` still models that code:

1. **Lost worker** (`NoLostWorker`). A drained worker still finishing its
   task was "already running" to the respawn pass, which cleared its intent
   but left it stopped; the worker then exited and nothing brought it
   back. Reached by `blanket upgrade` whenever a task outlasted
   `restart.drainTimeout`, and by abort or the watchdog after a drain.
2. **Unpaid debt** (`DebtPaid`). A respawn skipped by the 30s interval or
   a failed spawn was retried only at the next boot or abort.
3. **Duplicate worker** (`NoDuplicateWorker`). `PUT /worker/:id/restart`
   on a stopped worker whose process was still mid-task forked a second
   process with the same id; both kept running.
4. **Old worker after upgrade** (`NoOldWorkerAfterUpgrade`), via 3.

Every `FIX:` comment in the spec is one part of the fix #278 made. Each part
names the code that implements it.
