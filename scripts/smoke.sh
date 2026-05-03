#!/usr/bin/env bash
# End-to-end smoke test for the built blanket binary.
#
# Spins up the server on a non-default port against a throwaway config,
# exercises a handful of critical endpoints, then tears everything down.
# Intended to catch regressions that unit tests miss: binary layout issues,
# embedded asset problems, config/defaults drift, the bolt lock UX, etc.
#
# Usage:
#   scripts/smoke.sh [path/to/blanket-binary]
#
# If no binary is given, the script picks the first one matching
# ./blanket-<os>-<arch>[.exe] in the repo root.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

BINARY="${1:-}"
if [[ -z "$BINARY" ]]; then
    # Prefer a host-native binary if one is present.
    for candidate in "./blanket-linux-amd64" "./blanket-darwin-amd64" "./blanket-windows-amd64.exe"; do
        if [[ -x "$candidate" ]]; then
            BINARY="$candidate"
            break
        fi
    done
fi

if [[ -z "$BINARY" || ! -x "$BINARY" ]]; then
    echo "smoke: no blanket binary found; build one with 'make linux' (or darwin/windows) first" >&2
    exit 1
fi

PORT=18773
BASE="http://localhost:${PORT}"
WORKDIR="$(mktemp -d -t blanket-smoke-XXXXXX)"
SERVER_PID=""

cleanup() {
    local status=$?
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    rm -rf "$WORKDIR"
    if [[ $status -eq 0 ]]; then
        echo "smoke: OK"
    else
        echo "smoke: FAILED (exit $status)" >&2
    fi
    exit $status
}
trap cleanup EXIT INT TERM

mkdir -p "$WORKDIR/types" "$WORKDIR/results"
cp "$REPO_ROOT/testdata/types/echo_task.toml" "$WORKDIR/types/echo_task.toml"

cat > "$WORKDIR/config.json" <<EOF
{
  "port": ${PORT},
  "database": "$WORKDIR/blanket.db",
  "tasks": {
    "typesPaths": ["$WORKDIR/types"],
    "resultsPath": "$WORKDIR/results"
  },
  "logLevel": "warn"
}
EOF

# Run the server from the workdir so relative paths resolve predictably.
(
    cd "$WORKDIR"
    "$REPO_ROOT/$BINARY" --config "$WORKDIR/config.json" > "$WORKDIR/server.log" 2>&1 &
    echo $! > "$WORKDIR/server.pid"
) || true
SERVER_PID="$(cat "$WORKDIR/server.pid")"

# Poll /version until the server is listening, or give up.
ready=0
for _ in $(seq 1 50); do
    if curl -fsS "$BASE/version" > /dev/null 2>&1; then
        ready=1
        break
    fi
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
        echo "smoke: server exited before becoming ready; log follows:" >&2
        cat "$WORKDIR/server.log" >&2
        exit 1
    fi
    sleep 0.1
done

if [[ "$ready" -ne 1 ]]; then
    echo "smoke: server did not respond on $BASE within 5s; log follows:" >&2
    cat "$WORKDIR/server.log" >&2
    exit 1
fi

fail() {
    echo "smoke: FAIL — $*" >&2
    echo "--- server.log ---" >&2
    cat "$WORKDIR/server.log" >&2
    exit 1
}

# /version returns JSON with a name field.
version_body="$(curl -fsS "$BASE/version")"
grep -q '"name"' <<<"$version_body" || fail "/version missing name field: $version_body"

# / redirects to /ui/.
redirect_loc="$(curl -fsS -o /dev/null -w '%{redirect_url}' "$BASE/")"
[[ "$redirect_loc" == *"/ui/" ]] || fail "/ should redirect to /ui/, got '$redirect_loc'"

# /ui/ serves the HTMX shell.
ui_body="$(curl -fsSL "$BASE/ui/")"
grep -q '<title>Blanket' <<<"$ui_body" || fail "/ui/ missing <title>Blanket"

# /task/ starts empty.
tasks_body="$(curl -fsS "$BASE/task/")"
[[ "$tasks_body" == "[]" ]] || fail "/task/ should start empty, got '$tasks_body'"

# POST /task/ creates a task.
create_resp="$(curl -fsS -X POST -H 'Content-Type: application/json' \
    -d '{"type":"echo_task"}' "$BASE/task/")"
grep -q '"state":"WAITING"' <<<"$create_resp" || fail "new task not WAITING: $create_resp"

# /task/ now returns the new task.
tasks_body="$(curl -fsS "$BASE/task/")"
grep -q '"type":"echo_task"' <<<"$tasks_body" || fail "/task/ missing submitted task: $tasks_body"
