#!/usr/bin/env bash
# Restart state-machine tests for the built blanket binary
# (turtlemonvh/blanket#23 phase 5).
#
# The state machine's claim is not "each transition works" -- that is a Go
# test (server/serve_restart_test.go, lib/bolt/restart_test.go). Its claim
# is "a machine that loses power between any two transitions comes back to
# a documented place". That is only observable across a process boundary,
# and only at a point a wall-clock `kill -9` could never reliably hit: the
# instant after one transition's transaction commits and before the next
# call arrives.
#
# So the binary carries a crash-injection hook. BLANKET_TEST_CRASH_AT=<STATE>
# makes the server exit(97) immediately after committing the transition into
# that state, and this script parametrizes over every state in turn:
#
#     STAGED     nothing has changed. The next server clears the record and
#                is fully functional; worker spawn works.
#     BACKED_UP  same, plus a backup file that is still on disk.
#     PAUSED     the pause must NOT survive. A boot is proof the process
#                that paused us is gone, so the new server spawns workers.
#     SWAPPED    same as PAUSED.
#     DRAINING   workers were stopped with respawn intent. The next server
#                brings them back.
#     EXECING    same as DRAINING: the exec never happened, but the debt to
#                the workers did.
#
# Also covered, because they are the same kind of cross-process fact:
#
#   * the happy path end to end -- begin/pause/drain/exec with
#     --exec-mode=exec, asserting the pid is preserved and the drained
#     worker comes back;
#   * --exec-mode=exit exits with server.RestartExitCode (75, not 0: the
#     unit blanket installs says Restart=on-failure);
#   * PAUSED refuses POST /worker/ with 409 over real HTTP;
#   * the deadline watchdog un-pauses a server whose driver never came back.
#
# Usage:
#   scripts/restart_machine.sh [path/to/blanket-binary]

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# shellcheck source=scripts/lib/harness.sh
source "$REPO_ROOT/scripts/lib/harness.sh"

harness_find_binary "${1:-}"

# Windows has no SIGUSR2, never self-restarts, and this script is bash.
# The Go half of phase 5 runs there via `go test`; see CONTRIBUTORS.md.
case "$(uname -s)" in
    MINGW* | MSYS* | CYGWIN* | Windows_NT)
        echo "restart-machine: skipping on windows (see server/restart_windows.go)"
        exit 0
        ;;
esac

WORKER_PIDS=()

cleanup() {
    local status=$?
    for p in "${WORKER_PIDS[@]:-}"; do
        [[ -n "$p" ]] && kill -9 "$p" 2>/dev/null || true
    done
    harness_cleanup || true
    if [[ $status -eq 0 ]]; then
        echo "restart-machine: OK"
    else
        echo "restart-machine: FAILED (exit $status)" >&2
    fi
    exit $status
}
trap cleanup EXIT INT TERM

fail() {
    echo "restart-machine: FAIL — $*" >&2
    if [[ -n "${SERVER_LOG:-}" && -f "$SERVER_LOG" ]]; then
        echo "--- server.log (tail) ---" >&2
        tail -60 "$SERVER_LOG" >&2
    fi
    exit 1
}

# ops <method> <path> [curl args...]
#
# Every /ops/ call needs the loopback address (which we have) and the
# X-Blanket-Restart header (which is what provokes the CORS preflight the
# endpoints are carved out of). `-f` is deliberately absent: these tests
# assert on status codes including 409, which `-f` would turn into an exit
# status with no body.
ops() {
    local method="$1" path="$2"; shift 2
    curl -sS -X "$method" -H 'X-Blanket-Restart: 1' "$@" "$BASE$path"
}

restart_state() {
    ops GET /ops/restart/status | sed -n 's/.*"state":"\([A-Z_]*\)".*/\1/p' | head -1
}

# start_worker — launch a real `blanket worker` against the harness server
# and block until the server has it registered with a pid.
start_worker() {
    # Not in a `( cd … ; cmd & )` subshell, for the same reason
    # harness_start_server isn't: that makes the worker a grandchild, whose
    # pid this shell can neither wait on nor reliably kill.
    local prev="$PWD"
    cd "$WORKDIR"
    "$BINARY" --config "$CONFIG" worker \
        --tags "$1" \
        --checkinterval 0.5 \
        --logfile "$WORKDIR/worker-$$-${#WORKER_PIDS[@]}.log" \
        >> "$WORKDIR/worker.stdout.log" 2>&1 &
    WORKER_PIDS+=("$!")
    cd "$prev"

    local i
    for ((i = 0; i < 100; i++)); do
        if curl -fsS "$BASE/worker/" 2>/dev/null | grep -q '"pid":[1-9]'; then
            return 0
        fi
        sleep 0.1
    done
    fail "no worker registered within 10s"
}

running_worker_count() {
    curl -fsS "$BASE/worker/" 2>/dev/null | grep -o '"stopped":false' | wc -l | tr -d ' '
}

respawn_intent_count() {
    curl -fsS "$BASE/worker/" 2>/dev/null | grep -o '"respawnIntent":true' | wc -l | tr -d ' '
}

# --------------------------------------------------------------------------
# 1. Crash injection at every state.
# --------------------------------------------------------------------------
#
# The parametrized core. For each state: drive the machine up to it with the
# crash armed, confirm the process died with the injected code, then start a
# fresh server on the same database and assert the documented recovery.

crash_at_state() {
    local state="$1" with_worker="$2"

    echo "restart-machine: crash at $state"

    harness_init blanket-restart-machine
    BLANKET_TEST_CRASH_AT="$state" harness_start_server
    harness_wait_ready || exit 1

    if [[ "$with_worker" == "worker" ]]; then
        start_worker "test"
        [[ "$(running_worker_count)" == "1" ]] || fail "$state: expected 1 running worker before the restart"
    fi

    # Walk the machine. Each call may be the one that kills the server, so
    # every curl tolerates a dropped connection.
    ops POST "/ops/restart/begin" -d '{"reason":"crash injection test"}' \
        -H 'Content-Type: application/json' > /dev/null 2>&1 || true
    if [[ "$state" != "STAGED" ]]; then
        ops POST "/ops/backup" > /dev/null 2>&1 || true
    fi
    case "$state" in
        STAGED|BACKED_UP) ;;
        *) ops POST "/ops/restart/pause" > /dev/null 2>&1 || true ;;
    esac
    case "$state" in
        STAGED|BACKED_UP|PAUSED) ;;
        *) ops POST "/ops/restart/swapped" > /dev/null 2>&1 || true ;;
    esac
    case "$state" in
        DRAINING|EXECING) ops POST "/ops/restart/drain?wait=false" > /dev/null 2>&1 || true ;;
    esac
    if [[ "$state" == "EXECING" ]]; then
        ops POST "/ops/restart/exec" > /dev/null 2>&1 || true
    fi

    harness_wait_exit 15
    [[ "$HARNESS_EXIT_CODE" -eq 97 ]] \
        || fail "$state: expected the injected crash (exit 97), got $HARNESS_EXIT_CODE"

    # The record is on disk in exactly the state we crashed at. It is not
    # asserted from here -- nothing outside the server can read the bolt
    # file, which is the whole reason the record and the driver's journal
    # are separate things. What is asserted is the *recovery*, which the
    # replacement makes observable over HTTP.
    #
    # A fresh process on the same database: the recovery under test.
    harness_start_server
    harness_wait_ready || fail "$state: the replacement server did not come up (the bolt lock, or a refused boot?)"

    local after
    after="$(restart_state)"
    [[ "$after" == "IDLE" ]] \
        || fail "$state: the replacement server should have cleared the record, but reports $after"

    # A booted server is never born paused: spawn must work.
    local spawn_code
    spawn_code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
        -d '{"tags":["never-claims"],"checkInterval":0.5}' "$BASE/worker/")"
    [[ "$spawn_code" == "200" ]] \
        || fail "$state: worker spawn is still refused after the replacement booted (got $spawn_code)"

    if [[ "$with_worker" == "worker" ]]; then
        # The debt survives the record: workers the drain stopped come back.
        local i back=0
        for ((i = 0; i < 100; i++)); do
            if [[ "$(respawn_intent_count)" == "0" ]] && [[ "$(running_worker_count)" -ge 1 ]]; then
                back=1
                break
            fi
            sleep 0.1
        done
        [[ "$back" -eq 1 ]] \
            || fail "$state: the drained worker was never respawned (intents: $(respawn_intent_count), running: $(running_worker_count))"
    fi

    harness_stop_server TERM
    for p in "${WORKER_PIDS[@]:-}"; do
        [[ -n "$p" ]] && kill -9 "$p" 2>/dev/null || true
    done
    WORKER_PIDS=()
    harness_cleanup || true
    echo "restart-machine:   ok — recovered from $state"
}

for state in STAGED BACKED_UP PAUSED SWAPPED; do
    crash_at_state "$state" "no-worker"
done
for state in DRAINING EXECING; do
    crash_at_state "$state" "worker"
done

# --------------------------------------------------------------------------
# 2. PAUSED refuses worker spawn over real HTTP.
# --------------------------------------------------------------------------
echo "restart-machine: PAUSED refuses a worker spawn"

harness_init blanket-restart-machine
harness_start_server
harness_wait_ready || exit 1

ops POST "/ops/restart/begin" > /dev/null
code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d '{"tags":["a"],"checkInterval":0.5}' "$BASE/worker/")"
[[ "$code" == "200" ]] || fail "STAGED must not refuse a spawn (got $code)"

ops POST "/ops/restart/pause" > /dev/null
code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d '{"tags":["b"],"checkInterval":0.5}' "$BASE/worker/")"
[[ "$code" == "409" ]] || fail "PAUSED must refuse a spawn with 409 (got $code)"

ops POST "/ops/restart/abort" > /dev/null
code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d '{"tags":["c"],"checkInterval":0.5}' "$BASE/worker/")"
[[ "$code" == "200" ]] || fail "aborting must lift the pause (got $code)"

echo "restart-machine:   ok — 409 while paused, 200 either side"
harness_stop_server TERM
harness_cleanup || true

# --------------------------------------------------------------------------
# 3. The watchdog un-pauses a restart whose driver never came back.
# --------------------------------------------------------------------------
echo "restart-machine: the watchdog un-pauses an abandoned restart"

harness_init blanket-restart-machine
harness_start_server
harness_wait_ready || exit 1

# A two-second deadline stands in for a driver that was kill -9'd: nothing
# else calls in, so the watchdog is the only thing that can end this.
ops POST "/ops/restart/begin" -H 'Content-Type: application/json' \
    -d '{"reason":"abandoned","deadlineSeconds":2}' > /dev/null
ops POST "/ops/restart/pause" > /dev/null
[[ "$(restart_state)" == "PAUSED" ]] || fail "expected PAUSED after the pause call"

freed=0
for _ in $(seq 1 200); do
    if [[ "$(restart_state)" == "IDLE" ]]; then
        freed=1
        break
    fi
    sleep 0.1
done
[[ "$freed" -eq 1 ]] || fail "the watchdog never aborted the abandoned restart (still $(restart_state))"

code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d '{"tags":["d"],"checkInterval":0.5}' "$BASE/worker/")"
[[ "$code" == "200" ]] || fail "the watchdog's abort must lift the spawn pause (got $code)"

echo "restart-machine:   ok — abandoned restart aborted, spawn works again"
harness_stop_server TERM
harness_cleanup || true

# --------------------------------------------------------------------------
# 4. --exec-mode=exit exits with RestartExitCode, not 0.
# --------------------------------------------------------------------------
echo "restart-machine: --exec-mode=exit exits 75"

harness_init blanket-restart-machine
harness_start_server --exec-mode exit
harness_wait_ready || exit 1

ops POST "/ops/restart/begin" > /dev/null
exec_body="$(ops POST "/ops/restart/exec")"
grep -q '"execMode":"exit"' <<<"$exec_body" \
    || fail "exec should report the resolved mode; got: $exec_body"

harness_wait_exit 15
# 75 = EX_TEMPFAIL. Zero would be wrong: the systemd unit blanket installs
# says Restart=on-failure, so a clean exit is exactly what would leave the
# server down.
[[ "$HARNESS_EXIT_CODE" -eq 75 ]] \
    || fail "--exec-mode=exit should exit 75 so Restart=on-failure brings it back, got $HARNESS_EXIT_CODE"

echo "restart-machine:   ok — exit 75"
harness_cleanup || true

# --------------------------------------------------------------------------
# 5. The happy path: --exec-mode=exec restarts in place and respawns.
# --------------------------------------------------------------------------
echo "restart-machine: --exec-mode=exec restarts in place and brings workers back"

harness_init blanket-restart-machine
harness_start_server --exec-mode exec
harness_wait_ready || exit 1

start_worker "test"
[[ "$(running_worker_count)" == "1" ]] || fail "expected 1 running worker before the restart"

original_pid="$SERVER_PID"

ops POST "/ops/restart/begin" -H 'Content-Type: application/json' \
    -d '{"reason":"end to end"}' > /dev/null
ops POST "/ops/backup" > /dev/null
ops POST "/ops/restart/pause" > /dev/null
drain_body="$(ops POST "/ops/restart/drain")"
grep -q '"drained":true' <<<"$drain_body" \
    || fail "the drain should have waited out the worker; got: $drain_body"
ops POST "/ops/restart/exec" > /dev/null

# syscall.Exec: same pid, new image. Wait for it to answer again.
back=0
for _ in $(seq 1 150); do
    if curl -fsS --max-time 2 "$BASE/version" > /dev/null 2>&1; then
        back=1
        break
    fi
    if ! kill -0 "$original_pid" 2>/dev/null; then
        fail "pid $original_pid disappeared; --exec-mode=exec must re-exec in place"
    fi
    sleep 0.1
done
[[ "$back" -eq 1 ]] || fail "the server did not answer again within 15s of the exec"

kill -0 "$original_pid" 2>/dev/null || fail "pid $original_pid is gone after a re-exec in place"
[[ "$(restart_state)" == "IDLE" ]] || fail "the restarted server should have cleared the record"

back=0
for _ in $(seq 1 150); do
    if [[ "$(respawn_intent_count)" == "0" ]] && [[ "$(running_worker_count)" -ge 1 ]]; then
        back=1
        break
    fi
    sleep 0.1
done
[[ "$back" -eq 1 ]] \
    || fail "the drained worker never came back (intents: $(respawn_intent_count), running: $(running_worker_count))"

echo "restart-machine:   ok — pid $original_pid preserved, record cleared, worker respawned"

harness_stop_server TERM
