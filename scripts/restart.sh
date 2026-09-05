#!/usr/bin/env bash
# Shutdown / restart tests for the built blanket binary
# (turtlemonvh/blanket#23 phase 2).
#
# These cannot be written as Go tests. Signal delivery, exit codes, and
# SIGUSR2's re-exec-in-place are properties of a real process; under
# `go test` the port comes from process-global config, os.Executable()
# resolves to the test binary, and the BoltDB flock -- a cross-process
# invariant -- has nothing to be tested against. So: spawn the binary,
# signal it, and assert on what a real operator would see.
#
# Covered here:
#   1. SIGTERM with an SSE client attached drains inside the deadline,
#      delivers a `server-restarting` event to the client, and exits 0.
#   2. SIGUSR2 re-execs in place: same PID, server answering again,
#      BoltDB lock reacquired. Unix only.
#   3. SIGINT behaves like SIGTERM (stop, not restart).
#
# Usage:
#   scripts/restart.sh [path/to/blanket-binary]

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# shellcheck source=scripts/lib/harness.sh
source "$REPO_ROOT/scripts/lib/harness.sh"

harness_find_binary "${1:-}"

SSE_PID=""

cleanup() {
    local status=$?
    if [[ -n "$SSE_PID" ]] && kill -0 "$SSE_PID" 2>/dev/null; then
        kill "$SSE_PID" 2>/dev/null || true
    fi
    harness_cleanup || true
    if [[ $status -eq 0 ]]; then
        echo "restart: OK"
    else
        echo "restart: FAILED (exit $status)" >&2
    fi
    exit $status
}
trap cleanup EXIT INT TERM

fail() {
    echo "restart: FAIL — $*" >&2
    if [[ -n "${SERVER_LOG:-}" && -f "$SERVER_LOG" ]]; then
        echo "--- server.log ---" >&2
        cat "$SERVER_LOG" >&2
    fi
    exit 1
}

# start_sse_client <path> <outfile>
#
# Attaches a long-lived SSE reader and blocks until it has received its
# first frame, so the handler is provably inside its select before we
# signal the server.
start_sse_client() {
    local path="$1" out="$2"
    : > "$out"
    curl -N -sS "$BASE$path" > "$out" 2>/dev/null &
    SSE_PID=$!

    local i
    for ((i = 0; i < 50; i++)); do
        if [[ -s "$out" ]]; then
            return 0
        fi
        sleep 0.1
    done
    fail "SSE client on $path never received a frame"
}

# await_sse_frame <outfile> <needle>
#
# Waits for the SSE client's output to contain `needle`, then stops the
# client. Polls the file rather than `wait`-ing on curl: bash purges
# finished background jobs from its table, so a later `wait` on the PID is
# a coin flip between working and printing "No record of process".
await_sse_frame() {
    local out="$1" needle="$2" i
    for ((i = 0; i < 50; i++)); do
        if grep -q "$needle" "$out" 2>/dev/null; then
            [[ -n "$SSE_PID" ]] && kill "$SSE_PID" 2>/dev/null
            SSE_PID=""
            return 0
        fi
        sleep 0.1
    done
    [[ -n "$SSE_PID" ]] && kill "$SSE_PID" 2>/dev/null
    SSE_PID=""
    return 1
}

# --------------------------------------------------------------------------
# 1. SIGTERM: drain, tell the client, exit 0.
# --------------------------------------------------------------------------
echo "restart: SIGTERM drains with an SSE client attached"

harness_init blanket-restart
harness_start_server
harness_wait_ready || exit 1

start_sse_client "/ui/sse/tasks" "$WORKDIR/sse.out"

kill -TERM "$SERVER_PID"
harness_wait_exit 15

[[ "$HARNESS_EXIT_CODE" -eq 0 ]] \
    || fail "SIGTERM should exit 0, got $HARNESS_EXIT_CODE (137 means the watchdog had to kill a hung shutdown)"

# The server's own shutdown deadline is 5s (server.DefaultShutdownTimeout);
# a drain that takes longer than that plus slack means a streaming handler
# is not shutdown-aware.
[[ "$HARNESS_EXIT_MS" -lt 8000 ]] \
    || fail "shutdown took ${HARNESS_EXIT_MS}ms, past the 5s deadline"

await_sse_frame "$WORKDIR/sse.out" 'event:server-restarting' \
    || fail "SSE client did not receive a server-restarting event; got: $(cat "$WORKDIR/sse.out")"
grep -q '^retry:' "$WORKDIR/sse.out" \
    || fail "SSE client did not receive a retry hint; got: $(cat "$WORKDIR/sse.out")"

echo "restart:   ok — exit 0 in ${HARNESS_EXIT_MS}ms, client told to reconnect"

# The bolt lock must actually be gone: a fresh server on the same database
# proves storage was closed rather than merely abandoned.
harness_start_server
harness_wait_ready || fail "could not restart on the same database after SIGTERM (bolt lock not released?)"
kill -TERM "$SERVER_PID"
harness_wait_exit 15
[[ "$HARNESS_EXIT_CODE" -eq 0 ]] || fail "second SIGTERM exited $HARNESS_EXIT_CODE"
echo "restart:   ok — bolt lock released; a fresh server reopened the same database"

harness_cleanup || true

# --------------------------------------------------------------------------
# 2. SIGINT is a stop, not a restart.
# --------------------------------------------------------------------------
echo "restart: SIGINT stops the server"

harness_init blanket-restart
harness_start_server
harness_wait_ready || exit 1

kill -INT "$SERVER_PID"
harness_wait_exit 15
[[ "$HARNESS_EXIT_CODE" -eq 0 ]] || fail "SIGINT should exit 0, got $HARNESS_EXIT_CODE"

# `cmd && fail` would trip `set -e` when cmd fails, which is the passing
# case here -- so the check has to be an `if`.
if curl -fsS --max-time 2 "$BASE/version" > /dev/null 2>&1; then
    fail "server still answering after SIGINT — it must not resurrect itself"
fi

echo "restart:   ok — exit 0, nothing came back"

harness_cleanup || true

# --------------------------------------------------------------------------
# 3. SIGUSR2 re-execs in place, keeping the PID.
# --------------------------------------------------------------------------
case "$(uname -s)" in
    MINGW* | MSYS* | CYGWIN* | Windows_NT)
        echo "restart: skipping SIGUSR2 self-restart (no SIGUSR2 on windows; see server/restart_windows.go)"
        exit 0
        ;;
esac

echo "restart: SIGUSR2 re-execs in place"

harness_init blanket-restart
harness_start_server
harness_wait_ready || exit 1

original_pid="$SERVER_PID"

# Something in the database, so we can tell afterwards that the re-execed
# image really reopened the same store rather than starting fresh.
task_resp="$(curl -fsS -X POST -H 'Content-Type: application/json' \
    -d '{"type":"echo_task"}' "$BASE/task/")"
grep -q '"state":"WAITING"' <<<"$task_resp" || fail "could not create a task before restart: $task_resp"

start_sse_client "/ui/sse/tasks" "$WORKDIR/sse-usr2.out"

kill -USR2 "$SERVER_PID"

# The re-exec is a syscall.Exec: same PID, new process image. Wait for the
# server to answer again rather than for an exit that never comes.
back=0
for _ in $(seq 1 100); do
    if curl -fsS --max-time 2 "$BASE/version" > /dev/null 2>&1; then
        back=1
        break
    fi
    if ! kill -0 "$original_pid" 2>/dev/null; then
        fail "process $original_pid disappeared on SIGUSR2 — it should re-exec in place, not exit"
    fi
    sleep 0.1
done
[[ "$back" -eq 1 ]] || fail "server did not answer again within 10s of SIGUSR2"

kill -0 "$original_pid" 2>/dev/null \
    || fail "PID $original_pid is gone after the restart; re-exec must preserve it"
[[ "$SERVER_PID" == "$original_pid" ]] || fail "harness lost track of the server PID"

await_sse_frame "$WORKDIR/sse-usr2.out" 'event:server-restarting' \
    || fail "SSE client was not told about the restart; got: $(cat "$WORKDIR/sse-usr2.out")"

# The new image reopened the same database: the task submitted before the
# restart is still there, which also proves the bolt lock was released and
# retaken by the same PID.
tasks_after="$(curl -fsS "$BASE/task/")"
grep -q '"type":"echo_task"' <<<"$tasks_after" \
    || fail "task submitted before the restart is missing afterwards: $tasks_after"

echo "restart:   ok — PID $original_pid preserved, database reopened, client told to reconnect"

kill -TERM "$SERVER_PID"
harness_wait_exit 15
[[ "$HARNESS_EXIT_CODE" -eq 0 ]] || fail "post-restart SIGTERM exited $HARNESS_EXIT_CODE"
