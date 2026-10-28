#!/usr/bin/env python3
"""Explicit-state BFS over BlanketRestart.tla, action for action.

A dependency-free mirror of the TLA+ spec so the check runs anywhere
python3 does (TLC needs Java and a download). Same variables, actions and
rules as the spec, so on the same constants the distinct-state count must
match TLC's -- that equality is the check that the two have not drifted.
Prints the shortest counterexample for each broken rule; exits 1 if any.

    python3 formal/restart/check.py                 # the code as it is
    python3 formal/restart/check.py --pre-278       # the code before #278: the bugs it fixed
    python3 formal/restart/check.py --mutate stop-keeps-intent
"""
import argparse
import sys
from collections import deque

ORDER = ["IDLE", "STAGED", "BACKED_UP", "PAUSED", "SWAPPED", "DRAINING", "EXECING"]
RANK = {s: i for i, s in enumerate(ORDER)}
SLOTS = ("abort", "boot")  # sorted, like TLC's function domain

ap = argparse.ArgumentParser()
ap.add_argument("--nw", type=int, default=1)
ap.add_argument("--max-procs", type=int, default=3)
ap.add_argument("--max-attempts", type=int, default=2)
ap.add_argument("--max-restarts", type=int, default=2)
ap.add_argument("--max-crashes", type=int, default=1)
ap.add_argument("--max-ops", type=int, default=1)
ap.add_argument("--spawn-can-fail", action="store_true")
ap.add_argument("--pre-278", dest="fixed", action="store_false",
                help="FIXED = FALSE: the code before turtlemonvh/blanket#278")
ap.add_argument("--no-abort", action="store_true", help="AllowAbort = FALSE")
ap.add_argument("--mutate", default="none", choices=["none", "stop-keeps-intent", "drain-two-tx"],
                help="break the code on purpose, to show a rule can fail")
ap.add_argument("--only", default="", help="comma-separated invariants to check")
A = ap.parse_args()
W = list(range(1, A.nw + 1))

# --- state encoding --------------------------------------------------------
# wk[w]   = (stopped, reason, intent, attempts, recent, pid)
# proc    = (w, ver, pc)
# pass    = (on, kind, todo(frozenset), cur, ph, snap)  snap[w] = (pid, attempts, recent)
# state   = (rec, srv, srvVer, disk, execPending, wk, procs, passes, wanted,
#            nRestarts, nCrashes, nOps)
ZSNAP = tuple((0, 0, False) for _ in W)
NOPASS = (False, "none", frozenset(), 0, "pick", ZSNAP)


def init():
    wk = tuple((False, "", False, 0, False, w) for w in W)
    procs = tuple((w, "old", "loop") for w in W)
    return ("IDLE", "up", "old", "old", False, wk, procs, (NOPASS, NOPASS),
            tuple(True for _ in W), 0, 0, 0, False, False)


def S(st, **kw):
    names = ["rec", "srv", "srvVer", "disk", "execPending", "wk", "procs",
             "passes", "wanted", "nRestarts", "nCrashes", "nOps", "upgDrained", "drainPending"]
    l = list(st)
    for k, v in kw.items():
        l[names.index(k)] = v
    return tuple(l)


def setw(wk, w, **kw):
    f = ["stopped", "reason", "intent", "attempts", "recent", "pid"]
    r = list(wk[w - 1])
    for k, v in kw.items():
        r[f.index(k)] = v
    return wk[:w - 1] + (tuple(r),) + wk[w:]


def setp(passes, s, p):
    i = SLOTS.index(s)
    return passes[:i] + (p,) + passes[i + 1:]


def getp(passes, s):
    return passes[SLOTS.index(s)]


def alive(procs, pid):
    return 1 <= pid <= len(procs) and procs[pid - 1][2] != "exit"


def live(procs, w):
    return any(p[0] == w and p[2] != "exit" for p in procs)


def live_count(procs, w):
    return sum(1 for p in procs if p[0] == w and p[2] != "exit")


def start_pass(kind, wk):
    return (True, kind, frozenset(w for w in W if wk[w - 1][2]), 0, "pick",
            tuple((wk[w - 1][5], wk[w - 1][3], wk[w - 1][4]) for w in W))


def done(p):
    return (p[0], p[1], p[2], 0, "pick", p[5])


def periodic_enabled(st):
    return (A.fixed and st[1] == "up" and st[0] == "IDLE" and not getp(st[7], "boot")[0]
            and any(s[2] for s in st[5]))


def still_drained(ws):
    return ws[0] and ws[1] == "restart-drain" and ws[2]


def reap_enabled(st, w):
    rec, srv, _, _, _, wk, procs = st[:7]
    s = wk[w - 1]
    return (srv == "up" and rec == "IDLE" and not s[0] and not alive(procs, s[5]) and not s[4]
            and not any(p[0] == w and p[2] in ("reg", "loop") for p in procs)
            and not any(q[0] and q[3] == w and q[4] in ("spawn", "wait") for q in st[7]))


# --- actions ---------------------------------------------------------------
def nexts(st):
    rec, srv, srvVer, disk, ep, wk, procs, passes, wanted, nR, nC, nO, ud, dp = st
    up = srv == "up"
    act = rec != "IDLE"

    if up and rec == "IDLE" and nR < A.max_restarts:
        yield "Begin", S(st, rec="STAGED", nRestarts=nR + 1, upgDrained=False)
    for to in ("BACKED_UP", "PAUSED", "SWAPPED"):
        if up and act and RANK[to] > RANK[rec]:
            yield f"Advance({to})", S(st, rec=to)
    if rec == "PAUSED" and disk == "old":
        yield "SwapBinary", S(st, disk="new")
    if up and act and RANK["DRAINING"] > RANK[rec]:
        nwk = tuple(s if s[0] else (True, "restart-drain", True, 0, s[4], s[5]) for s in wk)
        if A.mutate == "drain-two-tx":
            if not dp:
                yield "DrainStops", S(st, wk=nwk, upgDrained=(disk == "new"), drainPending=True)
        else:
            yield "Drain", S(st, rec="DRAINING", wk=nwk, upgDrained=(disk == "new"))
    if up and dp and act and RANK["DRAINING"] > RANK[rec]:
        yield "DrainRecord", S(st, rec="DRAINING", drainPending=False)
    if up and act and RANK["EXECING"] > RANK[rec] and not ep:
        yield "ExecCommit", S(st, rec="EXECING", execPending=True)
    if up and ep:
        yield "Teardown", S(st, srv="down", execPending=False, passes=(NOPASS, NOPASS), drainPending=False)
    if up and act and not getp(passes, "abort")[0] and not A.no_abort:
        np = passes
        if RANK[rec] >= RANK["DRAINING"]:
            np = setp(passes, "abort", start_pass("abort", wk))
        yield "Abort", S(st, rec="IDLE", passes=np, upgDrained=False, drainPending=False)
    if up and nC < A.max_crashes:
        yield "Crash", S(st, srv="down", execPending=False, passes=(NOPASS, NOPASS), nCrashes=nC + 1, drainPending=False)
    if srv == "down":
        yield "Boot", S(st, srv="up", srvVer=disk, rec="IDLE",
                        passes=setp(passes, "boot", start_pass("boot", wk)))
    if periodic_enabled(st):
        yield "PeriodicPass", S(st, passes=setp(passes, "boot", start_pass("periodic", wk)))

    # respawn passes
    for s in SLOTS:
        p = getp(passes, s)
        on, kind, todo, cur, ph, snap = p
        if not (up and on):
            continue
        if ph == "pick":
            if not todo:
                yield f"PassPick({s})", S(st, passes=setp(passes, s, NOPASS))
            elif not (A.fixed and getp(passes, "boot" if s == "abort" else "abort")[3] != 0):
                for w in sorted(todo):
                    yield f"PassPick({s},w{w})", S(st, passes=setp(passes, s, (on, kind, todo - {w}, w, "guard", snap)))
        elif ph == "guard":
            w = cur
            spid, satt, srec = snap[w - 1]
            if A.fixed:
                spid, satt, srec = wk[w - 1][5], wk[w - 1][3], wk[w - 1][4]
            stopped = still_drained(wk[w - 1])
            if A.fixed and (act or not wk[w - 1][2] or (alive(procs, spid) and procs[spid - 1][2] == "reg")):
                yield f"PassGuard({s},w{w}):restart-in-flight", S(st, passes=setp(passes, s, done(p)))
            elif alive(procs, spid) and not (A.fixed and stopped):
                yield f"PassGuard({s},w{w}):alive->clear", S(st, wk=setw(wk, w, intent=False, attempts=0), passes=setp(passes, s, done(p)))
            elif A.fixed and alive(procs, spid) and stopped:
                nwk = setw(wk, w, stopped=False, reason="", attempts=0) if s == "abort" else wk
                yield f"PassGuard({s},w{w}):alive-drained", S(st, wk=nwk, passes=setp(passes, s, done(p)))
            elif satt >= A.max_attempts:
                yield f"PassGuard({s},w{w}):cap", S(st, wk=setw(wk, w, intent=False, attempts=0, stopped=True, reason="gaveup"), passes=setp(passes, s, done(p)))
            elif srec:
                yield f"PassGuard({s},w{w}):too-recent", S(st, passes=setp(passes, s, done(p)))
            else:
                yield f"PassGuard({s},w{w}):ok", S(st, passes=setp(passes, s, (on, kind, todo, cur, "claim", snap)))
        elif ph == "claim":
            w = cur
            if not wk[w - 1][2] or (A.fixed and (act or alive(procs, wk[w - 1][5]))):
                yield f"PassClaim({s},w{w}):intent-gone", S(st, passes=setp(passes, s, done(p)))
            else:
                ws = wk[w - 1]
                yield f"PassClaim({s},w{w})", S(st, wk=setw(wk, w, stopped=False, reason="", attempts=ws[3] + 1, recent=True, pid=0),
                                                 passes=setp(passes, s, (on, kind, todo, cur, "spawn", snap)))
        elif ph == "spawn":
            w = cur
            if len(procs) < A.max_procs:
                nwk = setw(wk, w, pid=len(procs) + 1) if A.fixed else wk
                yield f"PassSpawn({s},w{w})->pid{len(procs)+1}", S(st, wk=nwk, procs=procs + ((w, disk, "reg"),), passes=setp(passes, s, (on, kind, todo, cur, "wait", snap)))
            if A.spawn_can_fail:
                yield f"PassSpawn({s},w{w}):failed", S(st, passes=setp(passes, s, done(p)))
        elif ph == "wait":
            w = cur
            pid = wk[w - 1][5]
            reg = (1 <= pid <= len(procs) and procs[pid - 1][2] != "reg") if A.fixed else pid != 0
            if reg:
                nwk = wk if (A.fixed and wk[w - 1][0]) else setw(wk, w, intent=False, attempts=0)
                yield f"PassWait({s},w{w}):registered->clear", S(st, wk=nwk, passes=setp(passes, s, done(p)))
            if A.spawn_can_fail:
                yield f"PassWait({s},w{w}):timeout", S(st, passes=setp(passes, s, done(p)))

    # worker processes
    for i, (w, ver, pc) in enumerate(procs, start=1):
        if up and pc == "reg":
            yield f"Register(pid{i},w{w})", S(st, wk=setw(wk, w, pid=i), procs=procs[:i - 1] + ((w, ver, "loop"),) + procs[i:])
        if srv == "down" and pc == "reg":
            yield f"RegisterGiveUp(pid{i},w{w})", S(st, procs=procs[:i - 1] + ((w, ver, "exit"),) + procs[i:])
        if up and pc == "loop":
            npc = "exit" if wk[w - 1][0] else "task"
            yield f"Poll(pid{i},w{w})->{npc}", S(st, procs=procs[:i - 1] + ((w, ver, npc),) + procs[i:])
        if pc == "task":
            yield f"TaskDone(pid{i},w{w})", S(st, procs=procs[:i - 1] + ((w, ver, "loop"),) + procs[i:])

    for w in W:
        ws = wk[w - 1]
        if up and nO < A.max_ops and not ws[0]:
            nwanted = wanted[:w - 1] + (False,) + wanted[w:]
            if A.mutate == "stop-keeps-intent":
                yield f"OpStop(w{w})", S(st, wk=setw(wk, w, stopped=True, reason="operator"), wanted=nwanted, nOps=nO + 1)
            else:
                yield f"OpStop(w{w})", S(st, wk=setw(wk, w, stopped=True, reason="operator", intent=False, attempts=0), wanted=nwanted, nOps=nO + 1)
        if up and nO < A.max_ops and ws[0] and RANK[rec] < RANK["PAUSED"] and len(procs) < A.max_procs:
            nwanted = wanted[:w - 1] + (True,) + wanted[w:]
            if A.fixed and not alive(procs, ws[5]) and all(q[3] != w for q in passes):
                yield f"OpRestart(w{w})->pid{len(procs)+1}", S(st, wk=setw(wk, w, stopped=False, reason="", intent=False, attempts=0, pid=len(procs) + 1), procs=procs + ((w, disk, "reg"),), wanted=nwanted, nOps=nO + 1)
            elif not A.fixed:
                yield f"OpRestart(w{w})->pid{len(procs)+1}", S(st, wk=setw(wk, w, stopped=False, reason=""), procs=procs + ((w, disk, "reg"),), wanted=nwanted, nOps=nO + 1)
        if reap_enabled(st, w):
            yield f"Reap(w{w})", S(st, wk=setw(wk, w, stopped=True, reason="reaper"))
        if ws[4]:
            np = tuple(q[:5] + (q[5][:w - 1] + ((q[5][w - 1][0], q[5][w - 1][1], False),) + q[5][w:],) for q in passes)
            yield f"Tick(w{w})", S(st, wk=setw(wk, w, recent=False), passes=np)


# --- rules -----------------------------------------------------------------
def settled(st):
    rec, srv, _, _, ep, wk, procs, passes = st[:8]
    return (srv == "up" and rec == "IDLE" and not ep and not any(p[0] for p in passes)
            and all(pc == "exit" or (pc == "loop" and not wk[w - 1][0]) for w, _, pc in procs)
            and not any(reap_enabled(st, w) or wk[w - 1][4] for w in W) and not periodic_enabled(st))


def NoLostWorker(st):
    if not settled(st):
        return True
    wk, procs, wanted = st[5], st[6], st[8]
    return all(not wanted[w - 1] or live(procs, w) or wk[w - 1][2] or wk[w - 1][1] in ("gaveup", "reaper") for w in W)


def DebtPaid(st):
    if not settled(st):
        return True
    return all(not st[8][w - 1] or not st[5][w - 1][2] for w in W)


def NoDuplicateWorker(st):
    return all(live_count(st[6], w) <= 1 for w in W)


def OperatorStopWins(st):
    if not settled(st):
        return True
    return all(st[8][w - 1] or not live(st[6], w) for w in W)


def NoOldWorkerAfterUpgrade(st):
    if not (settled(st) and st[2] == "new" and st[12]):
        return True
    return all(pc == "exit" or ver == "new" for _, ver, pc in st[6])


RULES = [NoLostWorker, DebtPaid, NoDuplicateWorker, OperatorStopWins, NoOldWorkerAfterUpgrade]
if A.only:
    RULES = [r for r in RULES if r.__name__ in A.only.split(",")]


def describe(st):
    rec, srv, srvVer, disk, ep, wk, procs, passes, wanted, *_ = st
    ws = "; ".join(
        f"w{w}:{'stopped' if s[0] else 'running'}"
        f"{'('+s[1]+')' if s[1] else ''}{' intent' if s[2] else ''} pid={s[5]}"
        for w, s in zip(W, wk))
    ps = " ".join(f"pid{i}=w{p[0]}/{p[1]}/{p[2]}" for i, p in enumerate(procs, 1))
    return f"server={srv}({srvVer}) disk={disk} record={rec} | {ws} | {ps}"


def main():
    s0 = init()
    parent = {s0: None}
    q = deque([s0])
    broken = {}
    deadlocks = 0
    while q:
        st = q.popleft()
        for r in RULES:
            if r.__name__ not in broken and not r(st):
                broken[r.__name__] = st
        n = 0
        for name, nx in nexts(st):
            n += 1
            if nx not in parent:
                parent[nx] = (st, name)
                q.append(nx)
        if n == 0:
            deadlocks += 1
    print(f"constants: {vars(A)}")
    print(f"distinct states: {len(parent)}")
    for r in RULES:
        name = r.__name__
        if name not in broken:
            print(f"\n[holds] {name}")
            continue
        path = []
        st = broken[name]
        while parent[st] is not None:
            prev, act = parent[st]
            path.append((act, st))
            st = prev
        path.reverse()
        print(f"\n[BROKEN] {name} -- shortest counterexample, {len(path)} steps")
        print(f"  0. Init: {describe(s0)}")
        for k, (act, s) in enumerate(path, 1):
            print(f"  {k}. {act}: {describe(s)}")
    return 1 if broken else 0


if __name__ == "__main__":
    sys.exit(main())
