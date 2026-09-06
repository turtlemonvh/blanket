#!/usr/bin/env bash
# Shared subprocess-test harness for the built blanket binary.
#
# Some behaviour can only be tested against a real process: signal
# handling, the shutdown sequence, SIGUSR2's re-exec-in-place, the BoltDB
# flock (a cross-process invariant), and anything reading os.Executable()
# (which resolves to the test binary under `go test`). Those tests spawn
# the binary, and they all need the same scaffolding: a free port, a
# throwaway workdir, a generated config, readiness polling, and cleanup on
# any exit path.
#
# This library is that scaffolding, factored out of scripts/smoke.sh so
# scripts/restart.sh (and turtlemonvh/blanket#23 phase 5's restart
# state-machine tests) don't duplicate it.
#
# Usage:
#
#     source "$REPO_ROOT/scripts/lib/harness.sh"
#     harness_find_binary "${1:-}"       # -> BINARY (absolute)
#     harness_init                       # -> WORKDIR, PORT, BASE, CONFIG
#     trap harness_cleanup EXIT INT TERM
#     harness_start_server               # -> SERVER_PID
#     harness_wait_ready
#     ...
#
# Every function is prefixed `harness_`; the variables it exports are
# BINARY, WORKDIR, PORT, BASE, CONFIG, SERVER_PID and SERVER_LOG.

# ---------------------------------------------------------------------------
# Binary discovery
# ---------------------------------------------------------------------------

# harness_find_binary [path]
#
# Sets BINARY to an absolute path. With no argument, prefers a host-native
# build in the repo root. Exits 1 if nothing usable is found.
harness_find_binary() {
    local candidate="${1:-}"

    if [[ -z "$candidate" ]]; then
        for c in "./blanket-linux-amd64" "./blanket-darwin-amd64" "./blanket-windows-amd64.exe"; do
            if [[ -x "$REPO_ROOT/$c" ]]; then
                candidate="$c"
                break
            fi
        done
    fi

    if [[ -z "$candidate" ]]; then
        echo "harness: no blanket binary found; build one with 'make linux' (or darwin/windows) first" >&2
        exit 1
    fi

    # Accept either an absolute path or one relative to the repo root.
    if [[ "$candidate" != /* ]]; then
        candidate="$REPO_ROOT/${candidate#./}"
    fi

    if [[ ! -x "$candidate" ]]; then
        echo "harness: '$candidate' is not an executable blanket binary" >&2
        exit 1
    fi

    BINARY="$candidate"
}

# ---------------------------------------------------------------------------
# Port selection
# ---------------------------------------------------------------------------

# harness_pick_port [base]
#
# Echoes a TCP port nothing is currently listening on. Starts from `base`
# (default 18773, the port smoke.sh has always used) and walks upward.
# Inherently racy -- nothing holds the port between the check and the
# server's bind -- but good enough for a test box, and far better than a
# hardcoded port when two suites run concurrently.
harness_pick_port() {
    local port="${1:-18773}"
    local limit=$((port + 200))
    while [[ $port -lt $limit ]]; do
        if ! harness_port_in_use "$port"; then
            echo "$port"
            return 0
        fi
        port=$((port + 1))
    done
    echo "harness: could not find a free port in [${1:-18773}, $limit)" >&2
    return 1
}

harness_port_in_use() {
    local port="$1"
    # Bash's /dev/tcp is always available and needs no ss/netstat/lsof,
    # none of which are guaranteed inside the toolchain image.
    (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null && exec 3<&- && return 0
    return 1
}

# ---------------------------------------------------------------------------
# Workdir + config
# ---------------------------------------------------------------------------

# harness_init [name]
#
# Creates the throwaway workdir, picks a port, copies in the echo_task
# fixture, and writes a config. Sets WORKDIR, PORT, BASE, CONFIG,
# SERVER_LOG.
harness_init() {
    local name="${1:-blanket-harness}"

    WORKDIR="$(mktemp -d -t "${name}-XXXXXX")"
    PORT="$(harness_pick_port)"
    BASE="http://localhost:${PORT}"
    CONFIG="$WORKDIR/config.json"
    SERVER_LOG="$WORKDIR/server.log"
    SERVER_PID=""

    mkdir -p "$WORKDIR/types" "$WORKDIR/results"
    cp "$REPO_ROOT/testdata/types/echo_task.toml" "$WORKDIR/types/echo_task.toml"

    harness_write_config
}

# harness_write_config [logLevel]
harness_write_config() {
    local log_level="${1:-warn}"
    cat > "$CONFIG" <<EOF
{
  "port": ${PORT},
  "database": "$WORKDIR/blanket.db",
  "tasks": {
    "typesPaths": ["$WORKDIR/types"],
    "resultsPath": "$WORKDIR/results"
  },
  "logLevel": "${log_level}"
}
EOF
}

# ---------------------------------------------------------------------------
# Server lifecycle
# ---------------------------------------------------------------------------

# harness_start_server [extra args...]
#
# Starts the server in the background from inside WORKDIR (so relative
# paths resolve predictably) and sets SERVER_PID.
#
# Deliberately NOT wrapped in a `( cd … ; cmd & )` subshell: that makes the
# server a grandchild, and `wait` on a grandchild's PID fails, so its exit
# status is unobservable. scripts/restart.sh asserts on that exit status.
harness_start_server() {
    local prev="$PWD"
    cd "$WORKDIR"
    "$BINARY" --config "$CONFIG" "$@" >> "$SERVER_LOG" 2>&1 &
    SERVER_PID=$!
    cd "$prev"
    echo "$SERVER_PID" > "$WORKDIR/server.pid"
}

# harness_wait_exit [timeout_seconds]
#
# Waits for the server to exit, with a watchdog so a hung shutdown fails
# the test instead of hanging the suite. Sets HARNESS_EXIT_CODE and
# HARNESS_EXIT_MS (wall time spent waiting). A watchdog kill shows up as a
# nonzero exit code, which is exactly what the caller should assert on.
harness_wait_exit() {
    local timeout_s="${1:-10}"
    local start_ms
    start_ms="$(harness_now_ms)"

    (
        sleep "$timeout_s"
        kill -9 "$SERVER_PID" 2>/dev/null || true
    ) &
    local watchdog=$!

    set +e
    wait "$SERVER_PID"
    HARNESS_EXIT_CODE=$?
    set -e

    kill "$watchdog" 2>/dev/null || true
    wait "$watchdog" 2>/dev/null || true

    HARNESS_EXIT_MS=$(( $(harness_now_ms) - start_ms ))
    SERVER_PID=""
}

harness_now_ms() {
    # date +%s%3N is a GNU extension; fall back to whole seconds elsewhere.
    local ns
    ns="$(date +%s%N 2>/dev/null || true)"
    if [[ "$ns" =~ ^[0-9]+$ ]]; then
        echo $((ns / 1000000))
    else
        echo $(( $(date +%s) * 1000 ))
    fi
}

# harness_wait_ready [timeout_tenths]
#
# Polls /version until the server answers. Fails loudly (with the log) if
# the process dies first or the budget runs out. Default budget 5s.
harness_wait_ready() {
    local tries="${1:-50}"
    local i
    for ((i = 0; i < tries; i++)); do
        if curl -fsS "$BASE/version" > /dev/null 2>&1; then
            return 0
        fi
        if [[ -n "$SERVER_PID" ]] && ! kill -0 "$SERVER_PID" 2>/dev/null; then
            echo "harness: server exited before becoming ready; log follows:" >&2
            cat "$SERVER_LOG" >&2
            return 1
        fi
        sleep 0.1
    done
    echo "harness: server did not respond on $BASE in $((tries / 10))s; log follows:" >&2
    cat "$SERVER_LOG" >&2
    return 1
}

# harness_server_alive
harness_server_alive() {
    [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null
}

# harness_stop_server [signal]
#
# Signals the server and waits for it to exit. Default TERM.
harness_stop_server() {
    local sig="${1:-TERM}"
    if harness_server_alive; then
        kill "-$sig" "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
}

# harness_cleanup
#
# EXIT trap body: stop the server, remove the workdir, preserve the exit
# status. Scripts that want their own message should wrap this.
harness_cleanup() {
    local status=$?
    if harness_server_alive; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    [[ -n "${WORKDIR:-}" ]] && rm -rf "$WORKDIR"
    return $status
}
