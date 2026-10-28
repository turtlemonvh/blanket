---------------------------- MODULE BlanketRestart ----------------------------
(***************************************************************************)
(* The server side of blanket's restart state machine, the worker          *)
(* processes it stops and brings back, and everything that can interleave  *)
(* with them: a driver calling the /ops/restart/* routes in any order the  *)
(* server accepts, the watchdog, an operator stopping or restarting a      *)
(* worker, the reaper, and the server crashing at any point.               *)
(*                                                                         *)
(* Every action is one indivisible step in the code: one bolt transaction  *)
(* (db.Update), or one step a process takes that nothing else can split.   *)
(* Where two code steps are merged into one action, the comment says so;   *)
(* a merge can hide an interleaving but never adds a behaviour, so any     *)
(* counterexample found here is still a real one.                          *)
(*                                                                         *)
(* Code references are to commit 216b256.                                  *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, Sequences

CONSTANTS
    NW,           \* number of workers registered before anything starts
    MaxProcs,     \* bound on worker processes ever started (pids are never reused)
    MaxAttempts,  \* server/restart.go:114 MaxRespawnAttempts (3 in the code)
    MaxRestarts,  \* bound on POST /ops/restart/begin calls
    MaxCrashes,   \* bound on server crashes
    MaxOps,       \* bound on operator/environment actions
    SpawnCanFail, \* model cmd.Start / registration failures of a respawn
    AllowAbort,   \* FALSE isolates the boot path from the abort path
    FIXED         \* TRUE: the code since turtlemonvh/blanket#278 (every "FIX:"
                  \* comment). FALSE: the code before it, which breaks four rules.

Workers == 1..NW

\* lib/database/meta.go:135 restartStateOrder
Order == <<"IDLE", "STAGED", "BACKED_UP", "PAUSED", "SWAPPED", "DRAINING", "EXECING">>
Rank(s) == CHOOSE i \in 1..Len(Order) : Order[i] = s
Active(s) == s # "IDLE"                         \* meta.go RestartRecord.Active
PausesSpawn(s) == Rank(s) >= Rank("PAUSED")     \* meta.go RestartRecord.PausesSpawn

VARIABLES
    rec,          \* restartRecord.State in the meta bucket
    srv,          \* "up" | "down": is a server process holding the bolt lock
    srvVer,       \* binary the running server was started from
    disk,         \* binary at os.Executable(): "old" | "new"
    execPending,  \* EXECING committed; the teardown has not run yet
    wk,           \* worker records in the workers bucket
    procs,        \* worker processes; index = pid (pid+startTs never reused)
    passes,       \* respawnWorkers goroutines in flight (one per slot)
    wanted,       \* ghost: should this worker be running, by anyone's say-so
    upgDrained,   \* ghost: this attempt drained after the binary was swapped
    nRestarts, nCrashes, nOps

vars == <<rec, srv, srvVer, disk, execPending, wk, procs, passes, wanted,
          nRestarts, nCrashes, nOps, upgDrained>>

Reasons == {"", "restart-drain", "worker-shutdown", "reaper", "gaveup", "operator"}
PCs == {"reg", "loop", "task", "exit"}
Phases == {"pick", "guard", "claim", "spawn", "wait"}
NoPass == [on |-> FALSE, kind |-> "none", todo |-> {}, cur |-> 0, ph |-> "pick",
           snap |-> [w \in Workers |-> [pid |-> 0, attempts |-> 0, recent |-> FALSE]]]
Slots == {"boot", "abort"}

\* lib/proclive IsAlive(pid, pidStartTs), conclusive on linux/darwin/windows.
Alive(pid) == pid \in 1..Len(procs) /\ procs[pid].pc # "exit"
Live(w) == \E i \in 1..Len(procs) : procs[i].w = w /\ procs[i].pc # "exit"
LiveCount(w) == Cardinality({i \in 1..Len(procs) : procs[i].w = w /\ procs[i].pc # "exit"})

TypeOK ==
    /\ rec \in {Order[i] : i \in 1..Len(Order)}
    /\ srv \in {"up", "down"} /\ srvVer \in {"old", "new"} /\ disk \in {"old", "new"}
    /\ execPending \in BOOLEAN
    /\ \A w \in Workers :
         /\ wk[w].stopped \in BOOLEAN /\ wk[w].reason \in Reasons
         /\ wk[w].intent \in BOOLEAN /\ wk[w].attempts \in Nat   \* a stale listing can claim past the cap
         /\ wk[w].recent \in BOOLEAN /\ wk[w].pid \in 0..MaxProcs
    /\ \A i \in 1..Len(procs) : procs[i].w \in Workers /\ procs[i].pc \in PCs
    /\ Len(procs) <= MaxProcs

Init ==
    /\ rec = "IDLE" /\ srv = "up" /\ srvVer = "old" /\ disk = "old"
    /\ execPending = FALSE
    /\ wk = [w \in Workers |-> [stopped |-> FALSE, reason |-> "", intent |-> FALSE,
                                attempts |-> 0, recent |-> FALSE, pid |-> w]]
    /\ procs = [i \in 1..NW |-> [w |-> i, ver |-> "old", pc |-> "loop"]]
    /\ passes = [s \in Slots |-> NoPass]
    /\ wanted = [w \in Workers |-> TRUE]
    /\ nRestarts = 0 /\ nCrashes = 0 /\ nOps = 0 /\ upgDrained = FALSE

\* respawnWorkers (server/restart.go:656): GetWorkers once, then each
\* worker carrying an intent, in turn, on one goroutine.
StartPass(kind) ==
    [on |-> TRUE, kind |-> kind, todo |-> {w \in Workers : wk[w].intent}, cur |-> 0,
     ph |-> "pick",
     snap |-> [w \in Workers |-> [pid |-> wk[w].pid, attempts |-> wk[w].attempts,
                                  recent |-> wk[w].recent]]]

-----------------------------------------------------------------------------
(* The driver: any /ops/restart/* call the server accepts, in any order.   *)

\* POST /ops/restart/begin -- beginRestart, server/restart.go:333
Begin ==
    /\ srv = "up" /\ rec = "IDLE" /\ nRestarts < MaxRestarts
    /\ rec' = "STAGED" /\ nRestarts' = nRestarts + 1 /\ upgDrained' = FALSE
    /\ UNCHANGED <<srv, srvVer, disk, execPending, wk, procs, passes, wanted, nCrashes, nOps>>

\* POST /ops/backup -> BACKED_UP (noteBackupForRestart :412), /pause, /swapped
\* -- advanceRestart :379, stampTransition's rank check :308
Advance(to) ==
    /\ srv = "up" /\ Active(rec) /\ Rank(to) > Rank(rec)
    /\ rec' = to
    /\ UNCHANGED <<srv, srvVer, disk, execPending, wk, procs, passes, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* blanket upgrade renames the staged binary into place after PAUSED
\* (command/upgrade.go execAndVerify; docs/upgrade.md "The upgrade sequence").
SwapBinary ==
    /\ rec = "PAUSED" /\ disk = "old"
    /\ disk' = "new"
    /\ UNCHANGED <<rec, srv, srvVer, execPending, wk, procs, passes, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* POST /ops/restart/drain -- drainRestart :451 -> StopWorkersForRestart,
\* lib/bolt/restart.go:103. One transaction: the state and every stop.
\* The drain's wait (waitForDrain) only reads; a timeout answers drained:false
\* and the CLI goes on to exec (command/upgrade.go:589-596).
Drain ==
    /\ srv = "up" /\ Active(rec) /\ Rank("DRAINING") > Rank(rec)
    /\ rec' = "DRAINING"
    /\ wk' = [w \in Workers |->
                IF wk[w].stopped THEN wk[w]      \* restart.go:128 already stopped: left alone
                ELSE [wk[w] EXCEPT !.stopped = TRUE, !.reason = "restart-drain",
                                   !.intent = TRUE, !.attempts = 0]]
    /\ upgDrained' = (disk = "new")
    /\ UNCHANGED <<srv, srvVer, disk, execPending, procs, passes, wanted, nRestarts, nCrashes, nOps>>

\* POST /ops/restart/exec -- opsRestartExec, serve_restart.go:181: commit
\* EXECING, answer 202, hand the teardown to another goroutine.
ExecCommit ==
    /\ srv = "up" /\ Active(rec) /\ Rank("EXECING") > Rank(rec) /\ ~execPending
    /\ rec' = "EXECING" /\ execPending' = TRUE
    /\ UNCHANGED <<srv, srvVer, disk, wk, procs, passes, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* handleRestart, server/lifecycle.go:267: teardown, then re-exec or exit 75.
\* Either way this process lets go of the lock; Boot is the next one.
Teardown ==
    /\ srv = "up" /\ execPending
    /\ srv' = "down" /\ execPending' = FALSE /\ passes' = [s \in Slots |-> NoPass]
    /\ UNCHANGED <<rec, srvVer, disk, wk, procs, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* POST /ops/restart/abort, and the watchdog when the deadline lapses
\* (restartWatchdogLoop :769) -- both are abortRestart :547. The record goes
\* to IDLE in one transaction; if it had reached DRAINING the handler then
\* runs respawnWorkers itself.
Abort ==
    /\ AllowAbort /\ srv = "up" /\ Active(rec) /\ ~passes["abort"].on
    /\ rec' = "IDLE"
    /\ passes' = IF Rank(rec) >= Rank("DRAINING")
                 THEN [passes EXCEPT !["abort"] = StartPass("abort")] ELSE passes
    /\ upgDrained' = FALSE      \* an abort undoes the drain on purpose
    /\ UNCHANGED <<srv, srvVer, disk, execPending, wk, procs, wanted, nRestarts, nCrashes, nOps>>

-----------------------------------------------------------------------------
(* Server process lifecycle.                                               *)

\* kill -9, power loss, a panic: the process is gone between any two steps.
Crash ==
    /\ srv = "up" /\ nCrashes < MaxCrashes
    /\ srv' = "down" /\ execPending' = FALSE /\ passes' = [s \in Slots |-> NoPass]
    /\ nCrashes' = nCrashes + 1
    /\ UNCHANGED <<rec, srvVer, disk, wk, procs, wanted, nRestarts, nOps, upgDrained>>

\* Serve (server/lifecycle.go:141): adoptRestartRecordOnBoot clears whatever
\* record is there (restart.go:601); after the listener is up,
\* `go respawnWorkers("boot")` (lifecycle.go:234). Merged into one step:
\* nothing reads the workers bucket between the clear and the listing.
Boot ==
    /\ srv = "down"
    /\ srv' = "up" /\ srvVer' = disk /\ rec' = "IDLE"
    /\ passes' = [passes EXCEPT !["boot"] = StartPass("boot")]
    /\ UNCHANGED <<disk, execPending, wk, procs, wanted, nRestarts, nCrashes, nOps, upgDrained>>

-----------------------------------------------------------------------------
(* respawnWorker, server/restart.go:689, one worker at a time.            *)

Done(s) == [passes EXCEPT ![s].cur = 0, ![s].ph = "pick"]

\* The record still shows the drain's stop: the live process is the one the
\* drain stopped, not a respawned one (a claim clears Stopped).
StillDrained(w) == wk[w].stopped /\ wk[w].reason = "restart-drain" /\ wk[w].intent

Other(s) == IF s = "boot" THEN "abort" ELSE "boot"

PassPick(s) ==
    /\ srv = "up" /\ passes[s].on /\ passes[s].ph = "pick"
    /\ IF passes[s].todo = {}
       THEN passes' = [passes EXCEPT ![s] = NoPass]
       \* FIX: one respawnWorker at a time (ServerConfig.respawnMu), so two
       \* passes never work on the same worker at once.
       ELSE /\ FIXED => passes[Other(s)].cur = 0
            /\ \E w \in passes[s].todo :
              passes' = [passes EXCEPT ![s].todo = @ \ {w}, ![s].cur = w, ![s].ph = "guard"]
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, wk, procs, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* The three storm guards, from the listing's copy of the record (`w`).
\* Merged with the transaction each branch then runs.
\* FIX: under the mutex, re-read the record rather than trust the listing
\* (respawnWorker's GetWorker), and count a process as running only once it
\* has registered (WorkerConf.Running vs .Registering).
PassGuard(s) ==
    LET w == passes[s].cur
        sn == IF FIXED THEN [pid |-> wk[w].pid, attempts |-> wk[w].attempts, recent |-> wk[w].recent]
              ELSE passes[s].snap[w]
    IN
    /\ srv = "up" /\ passes[s].on /\ passes[s].ph = "guard"
    /\ IF FIXED /\ (Active(rec) \/ ~wk[w].intent \/ (Alive(sn.pid) /\ procs[sn.pid].pc = "reg"))
         \* FIX: a newer restart is in flight (its drain owns the intents
         \* now), the intent is already settled, or a spawn is still
         \* registering. Leave it for the periodic pass.
         THEN wk' = wk /\ passes' = Done(s)
       ELSE IF Alive(sn.pid) /\ ~(FIXED /\ StillDrained(w))
         \* :692 already running -> ClearWorkerRespawn(id, "")
         \* (lib/bolt/restart.go:209): intent and attempts only; Stopped stays.
         THEN /\ wk' = [wk EXCEPT ![w].intent = FALSE, ![w].attempts = 0]
              /\ passes' = Done(s)
       ELSE IF FIXED /\ Alive(sn.pid) /\ StillDrained(w)
         \* FIX: the live process is the one the drain stopped, still
         \* finishing its task. On abort, undo the drain (un-stop it). The
         \* intent stays either way: if the process had already seen the stop
         \* and is exiting (worker.go:600 -> os.Exit, a step this model makes
         \* atomic with Poll), a later pass respawns it; if it carries on, a
         \* later pass finds it alive and not stopped and clears the intent.
         THEN /\ wk' = IF s = "abort"
                       THEN [wk EXCEPT ![w].stopped = FALSE, ![w].reason = "",
                                       ![w].attempts = 0]
                       ELSE wk
              /\ passes' = Done(s)
       ELSE IF sn.attempts >= MaxAttempts
         \* :700 generation cap -> ClearWorkerRespawn(id, reason)
         THEN /\ wk' = [wk EXCEPT ![w].intent = FALSE, ![w].attempts = 0,
                                  ![w].stopped = TRUE, ![w].reason = "gaveup"]
              /\ passes' = Done(s)
       ELSE IF sn.recent
         \* :710 minimum interval: leave the intent "for a later pass"
         THEN /\ wk' = wk /\ passes' = Done(s)
       ELSE /\ wk' = wk /\ passes' = [passes EXCEPT ![s].ph = "claim"]
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, procs, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* ClaimWorkerRespawn, lib/bolt/restart.go:179: one transaction, before the fork.
PassClaim(s) ==
    LET w == passes[s].cur IN
    /\ srv = "up" /\ passes[s].on /\ passes[s].ph = "claim"
    \* FIX: the claim transaction re-checks what the guard checked
    \* (ClaimWorkerRespawn -> database.ErrRespawnDeferred).
    /\ IF ~wk[w].intent \/ (FIXED /\ (Active(rec) \/ Alive(wk[w].pid)))
         THEN wk' = wk /\ passes' = Done(s)          \* :724 an operator stop won
         ELSE /\ wk' = [wk EXCEPT ![w].stopped = FALSE, ![w].reason = "",
                                  ![w].attempts = @ + 1, ![w].recent = TRUE, ![w].pid = 0]
              /\ passes' = [passes EXCEPT ![s].ph = "spawn"]
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, procs, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* spawnRespawnedWorker -> launchWorkerAndWait (server/serve_workers.go:298):
\* fork `blanket worker -d` from os.Executable(), i.e. whatever is on disk.
PassSpawn(s) ==
    LET w == passes[s].cur IN
    /\ srv = "up" /\ passes[s].on /\ passes[s].ph = "spawn"
    /\ \/ /\ Len(procs) < MaxProcs
          /\ procs' = Append(procs, [w |-> w, ver |-> disk, pc |-> "reg"])
          /\ passes' = [passes EXCEPT ![s].ph = "wait"]
          \* FIX: record cmd.Process.Pid at fork, not at registration
          \* (launchWorkerAndWait -> RecordWorkerSpawn, WorkerConf.SpawnPid).
          \* Merged with the fork: the code writes it right after
          \* cmd.Start returns, and a crash between the two is the only
          \* window left where a second fork is possible.
          /\ wk' = IF FIXED THEN [wk EXCEPT ![w].pid = Len(procs) + 1] ELSE wk
       \/ /\ SpawnCanFail                     \* cmd.Start failed: intent stays (:731)
          /\ procs' = procs /\ passes' = Done(s) /\ wk' = wk
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* launchWorkerAndWait polls for a nonzero Pid, then :742 ClearWorkerRespawn.
\* Poll and clear merged.
PassWait(s) ==
    LET w == passes[s].cur IN
    /\ srv = "up" /\ passes[s].on /\ passes[s].ph = "wait"
    /\ \/ /\ IF FIXED THEN wk[w].pid \in 1..Len(procs) /\ procs[wk[w].pid].pc # "reg"
                    ELSE wk[w].pid # 0
          \* FIX: clear only the intent this pass claimed (SettleWorkerRespawn).
          \* A record stopped again since the claim (a newer drain, the
          \* reaper) keeps its intent.
          /\ wk' = IF FIXED /\ wk[w].stopped THEN wk
                   ELSE [wk EXCEPT ![w].intent = FALSE, ![w].attempts = 0]
          /\ passes' = Done(s)
       \/ /\ SpawnCanFail                     \* ErrWorkerNotRegistered: intent stays
          /\ wk' = wk /\ passes' = Done(s)
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, procs, wanted, nRestarts, nCrashes, nOps, upgDrained>>

-----------------------------------------------------------------------------
(* Worker processes, worker/worker.go.                                     *)

\* MustRegister -> PUT /worker/:id -> UpdateWorker (lib/bolt/database.go:127):
\* the field merge keeps every server-owned field; the pid is the worker's.
Register(i) ==
    LET w == procs[i].w IN
    /\ srv = "up" /\ procs[i].pc = "reg"
    /\ wk' = [wk EXCEPT ![w].pid = i]
    /\ procs' = [procs EXCEPT ![i].pc = "loop"]
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, passes, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* MustRegister gives up after RegisterRetryDeadline and the process exits
\* (log.Fatal, worker.go:420).
RegisterGiveUp(i) ==
    /\ srv = "down" /\ procs[i].pc = "reg"
    /\ procs' = [procs EXCEPT ![i].pc = "exit"]
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, wk, passes, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* One ProcessTasks iteration (worker.go:561): Refetch + Heartbeat; a
\* Stopped record ends the loop and the process exits; otherwise claim a task.
Poll(i) ==
    LET w == procs[i].w IN
    /\ srv = "up" /\ procs[i].pc = "loop"
    /\ procs' = [procs EXCEPT ![i].pc = IF wk[w].stopped THEN "exit" ELSE "task"]
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, wk, passes, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* ProcessOne returns. The task's child runs locally, so this needs no server.
TaskDone(i) ==
    /\ procs[i].pc = "task"
    /\ procs' = [procs EXCEPT ![i].pc = "loop"]
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, wk, passes, wanted, nRestarts, nCrashes, nOps, upgDrained>>

-----------------------------------------------------------------------------
(* Operators and the reaper.                                               *)

\* PUT /worker/:id/stop with no reason -- StopWorker (database.go:236):
\* an explicit stop clears any respawn intent.
OpStop(w) ==
    /\ srv = "up" /\ nOps < MaxOps /\ ~wk[w].stopped
    /\ wk' = [wk EXCEPT ![w].stopped = TRUE, ![w].reason = "operator",
                        ![w].intent = FALSE, ![w].attempts = 0]
    /\ wanted' = [wanted EXCEPT ![w] = FALSE] /\ nOps' = nOps + 1
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, procs, passes, nRestarts, nCrashes, upgDrained>>

\* PUT /worker/:id/restart -- restartWorker (serve_workers.go:187): refused
\* while paused, else StartWorker then launchWorkerAndWait. Merged.
OpRestart(w) ==
    /\ srv = "up" /\ nOps < MaxOps /\ wk[w].stopped /\ ~PausesSpawn(rec)
    /\ Len(procs) < MaxProcs
    \* FIX: refuse (409) while the stopped worker's process is still finishing
    \* a task, and take over any respawn the restart still owes (restartWorker,
    \* StartWorker). Merged: the code's StartWorker and the fork are two
    \* steps, both under respawnMu; a crash between them leaves a record
    \* that says running with no process, which the reaper marks.
    /\ FIXED => ~Alive(wk[w].pid)
    \* FIX: and it takes the same respawnMu as respawnWorker.
    /\ FIXED => \A s \in Slots : passes[s].cur # w
    /\ wk' = IF FIXED THEN [wk EXCEPT ![w].stopped = FALSE, ![w].reason = "",
                                      ![w].intent = FALSE, ![w].attempts = 0,
                                      ![w].pid = Len(procs) + 1]   \* pid at fork
             ELSE [wk EXCEPT ![w].stopped = FALSE, ![w].reason = ""]
    /\ procs' = Append(procs, [w |-> w, ver |-> disk, pc |-> "reg"])
    /\ wanted' = [wanted EXCEPT ![w] = TRUE] /\ nOps' = nOps + 1
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, passes, nRestarts, nCrashes, upgDrained>>

\* CleanupStalledWorkers (lib/bolt/reap.go:90), suppressed while a restart
\* is in flight (server/reaper.go:218). Stale = nothing heartbeating for w.
\* Timing assumption: workerDeadAfter (10m, reaper.go:83) outlasts a
\* registering worker's RegisterRetryDeadline (30s, worker.go:51), so the
\* reaper never stops a worker whose process is still registering, nor one
\* a claim has just stamped (LastHeardTs, lib/bolt/restart.go:189).
ReapEnabled(w) ==
    /\ srv = "up" /\ ~Active(rec) /\ ~wk[w].stopped /\ ~Alive(wk[w].pid) /\ ~wk[w].recent
    /\ ~\E i \in 1..Len(procs) : procs[i].w = w /\ procs[i].pc \in {"reg", "loop"}
    \* ...nor one the server is mid-way through spawning (claim to registered
    \* is bounded by MAX_REQUEST_TIME_SECONDS = 5s, server_util.go:12).
    /\ ~\E s \in Slots : passes[s].on /\ passes[s].cur = w /\ passes[s].ph \in {"spawn", "wait"}
Reap(w) ==
    /\ ReapEnabled(w)
    /\ wk' = [wk EXCEPT ![w].stopped = TRUE, ![w].reason = "reaper"]
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, procs, passes, wanted, nRestarts, nCrashes, nOps, upgDrained>>

\* MinRespawnInterval (30s) passes since w's last respawn. A pass's listing
\* holds the same or an older LastRespawnTs, so it has expired there too.
Tick(w) ==
    /\ wk[w].recent
    /\ wk' = [wk EXCEPT ![w].recent = FALSE]
    /\ passes' = [s \in Slots |-> [passes[s] EXCEPT !.snap[w].recent = FALSE]]
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, procs, wanted, nRestarts, nCrashes, nOps, upgDrained>>

-----------------------------------------------------------------------------
(* FIX only: a periodic pass retries intents nothing else will              *)
(* (respawnRetryLoop, every restart.respawnRetryInterval).                  *)
PeriodicEnabled ==
    /\ FIXED /\ srv = "up" /\ ~Active(rec) /\ ~passes["boot"].on
    /\ \E w \in Workers : wk[w].intent
PeriodicPass ==
    /\ PeriodicEnabled
    /\ passes' = [passes EXCEPT !["boot"] = StartPass("periodic")]
    /\ UNCHANGED <<rec, srv, srvVer, disk, execPending, wk, procs, wanted, nRestarts, nCrashes, nOps, upgDrained>>

Next ==
    \/ Begin \/ Drain \/ ExecCommit \/ Teardown \/ Abort \/ SwapBinary
    \/ \E to \in {"BACKED_UP", "PAUSED", "SWAPPED"} : Advance(to)
    \/ Crash \/ Boot \/ PeriodicPass
    \/ \E s \in Slots : PassPick(s) \/ PassGuard(s) \/ PassClaim(s) \/ PassSpawn(s) \/ PassWait(s)
    \/ \E i \in 1..Len(procs) : Register(i) \/ RegisterGiveUp(i) \/ Poll(i) \/ TaskDone(i)
    \/ \E w \in Workers : OpStop(w) \/ OpRestart(w) \/ Reap(w) \/ Tick(w)

Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
(* Rules. Each is one sentence a reader can agree to.                      *)

\* Nothing is happening by itself any more: the server is up with no restart
\* in flight, no respawn pass running, no process about to exit or register,
\* no task mid-run, and nothing for the reaper to do.
Settled ==
    /\ srv = "up" /\ rec = "IDLE" /\ ~execPending
    /\ \A s \in Slots : ~passes[s].on
    /\ \A i \in 1..Len(procs) :
         \/ procs[i].pc = "exit"
         \/ procs[i].pc = "loop" /\ ~wk[procs[i].w].stopped
    /\ \A w \in Workers : ~ReapEnabled(w) /\ ~wk[w].recent   \* and 30s have passed
    /\ ~PeriodicEnabled

\* "A restart never silently loses a worker": once things settle, every
\* worker nobody chose to stop is running, still owed a respawn, or stopped
\* with a reason that says it is not coming back (the generation cap, or the
\* reaper finding its process gone).
NoLostWorker ==
    Settled => \A w \in Workers :
        wanted[w] => (Live(w) \/ wk[w].intent \/ wk[w].reason \in {"gaveup", "reaper"})

\* "A respawn the restart owes is paid while the server is up, not at
\* some future restart."
DebtPaid ==
    Settled => \A w \in Workers : wanted[w] => ~wk[w].intent

\* "One worker id is never run by two processes at once."
NoDuplicateWorker == \A w \in Workers : LiveCount(w) <= 1

\* "A worker an operator stopped stays stopped."
OperatorStopWins ==
    Settled => \A w \in Workers : ~wanted[w] => ~Live(w)

\* "After an upgrade that drained settles, no worker still runs the old binary."
NoOldWorkerAfterUpgrade ==
    (Settled /\ srvVer = "new" /\ upgDrained) =>
        \A i \in 1..Len(procs) : procs[i].pc # "exit" => procs[i].ver = "new"

=============================================================================
