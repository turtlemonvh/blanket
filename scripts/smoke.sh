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
WORKER_PID=""

cleanup() {
    local status=$?
    if [[ -n "$WORKER_PID" ]] && kill -0 "$WORKER_PID" 2>/dev/null; then
        kill "$WORKER_PID" 2>/dev/null || true
        wait "$WORKER_PID" 2>/dev/null || true
    fi
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

# Scheduled tasks (turtlemonvh/blanket#61): a task submitted with a future
# notBefore starts SCHEDULED (not WAITING/claimable), and the background
# scheduler loop (default 2s tick) promotes it to WAITING once due.
scheduled_resp="$(curl -fsS -X POST -H 'Content-Type: application/json' \
    -d '{"type":"echo_task","notBefore":"2s"}' "$BASE/task/")"
grep -q '"state":"SCHEDULED"' <<<"$scheduled_resp" || fail "notBefore task not SCHEDULED: $scheduled_resp"

scheduled_id="$(jq -r '.id' <<<"$scheduled_resp")"
[[ -n "$scheduled_id" && "$scheduled_id" != "null" ]] || fail "could not extract scheduled task id: $scheduled_resp"

immediate_check="$(curl -fsS "$BASE/task/$scheduled_id")"
grep -q '"state":"SCHEDULED"' <<<"$immediate_check" || fail "scheduled task promoted before its notBefore time: $immediate_check"

promoted=0
for _ in $(seq 1 40); do
    check="$(curl -fsS "$BASE/task/$scheduled_id")"
    if grep -q '"state":"WAITING"' <<<"$check"; then
        promoted=1
        break
    fi
    sleep 0.25
done
[[ "$promoted" -eq 1 ]] || fail "scheduled task was not promoted to WAITING within 10s: $check"

# ---------------------------------------------------------------------------
# Synchronous ("blocking") submission -- POST /task/?wait
# (turtlemonvh/blanket#27).
#
# This is the only suite that can prove it end to end, because it's the
# only one that runs a real worker: ?wait returns whatever the worker
# actually did, so without one every wait would simply time out. The
# worker is started here, *after* the assertions above, on purpose -- a
# live worker would otherwise race the scheduled-task check by claiming
# the promoted task before the poll loop observes it in WAITING.
# ---------------------------------------------------------------------------

# Two extra task types, written into the throwaway types dir rather than
# added to testdata/types/: the server re-reads that directory per
# request, and testdata/types is deliberately kept to the one minimal
# fixture (see CLAUDE.md).
cat > "$WORKDIR/types/sync_result_task.toml" <<'EOF'
tags = ["exec:bash", "os:unix"]
timeout = 10
description = "Writes a JSON result artifact, for the ?wait smoke test"
command = "echo 'sync stdout line'; echo '{\"answer\": 42}' > result.json"
executor = "bash"
result_file = "result.json"
EOF

cat > "$WORKDIR/types/sync_fail_task.toml" <<'EOF'
tags = ["exec:bash", "os:unix"]
timeout = 10
description = "Exits 3, for the ?wait&fail_on_error smoke test"
command = "echo 'failing on purpose' >&2; exit 3"
executor = "bash"
EOF

(
    cd "$WORKDIR"
    "$REPO_ROOT/$BINARY" --config "$WORKDIR/config.json" worker \
        --tags "exec:bash,os:unix" \
        --checkinterval 0.5 \
        --logfile "$WORKDIR/worker.log" \
        > "$WORKDIR/worker.stdout.log" 2>&1 &
    echo $! > "$WORKDIR/worker.pid"
) || true
WORKER_PID="$(cat "$WORKDIR/worker.pid")"

worker_ready=0
for _ in $(seq 1 50); do
    if [[ "$(curl -fsS "$BASE/worker/" | jq -r 'length')" != "0" ]]; then
        worker_ready=1
        break
    fi
    if ! kill -0 "$WORKER_PID" 2>/dev/null; then
        echo "smoke: worker exited before registering; log follows:" >&2
        cat "$WORKDIR/worker.log" "$WORKDIR/worker.stdout.log" 2>/dev/null >&2
        exit 1
    fi
    sleep 0.2
done
[[ "$worker_ready" -eq 1 ]] || fail "worker did not register within 10s"

# A successful synchronous submit returns state, exit code, output, and
# the parsed result_file in the one response.
sync_resp="$(curl -fsS -X POST -H 'Content-Type: application/json' \
    -d '{"type":"sync_result_task"}' "$BASE/task/?wait=20s")"
jq -e '.waitOutcome == "completed"' <<<"$sync_resp" > /dev/null \
    || fail "?wait response waitOutcome not 'completed': $sync_resp"
jq -e '.task.state == "SUCCESS"' <<<"$sync_resp" > /dev/null \
    || fail "?wait task did not reach SUCCESS: $sync_resp"
jq -e '.task.exitCode == 0' <<<"$sync_resp" > /dev/null \
    || fail "?wait task exitCode not 0: $sync_resp"
jq -e '.stdout | contains("sync stdout line")' <<<"$sync_resp" > /dev/null \
    || fail "?wait response missing task stdout: $sync_resp"
jq -e '.result.answer == 42' <<<"$sync_resp" > /dev/null \
    || fail "?wait response missing parsed result_file: $sync_resp"
jq -e '.resultError == null' <<<"$sync_resp" > /dev/null \
    || fail "?wait response carried a resultError: $sync_resp"

# A failing task is a 200 by default -- the status describes the API call,
# not the task -- and carries the real exit code.
fail_body="$WORKDIR/sync-fail.json"
fail_status="$(curl -sS -o "$fail_body" -w '%{http_code}' -X POST \
    -H 'Content-Type: application/json' \
    -d '{"type":"sync_fail_task"}' "$BASE/task/?wait=20s")"
[[ "$fail_status" == "200" ]] \
    || fail "?wait on a failing task should be 200 by default, got $fail_status: $(cat "$fail_body")"
jq -e '.task.state == "ERROR" and .task.exitCode == 3' "$fail_body" > /dev/null \
    || fail "?wait on a failing task lost its state/exitCode: $(cat "$fail_body")"
jq -e '.stderr | contains("failing on purpose")' "$fail_body" > /dev/null \
    || fail "?wait response missing task stderr: $(cat "$fail_body")"

# ...and 502 with fail_on_error=true, so `curl --fail` can notice.
foe_body="$WORKDIR/sync-fail-on-error.json"
foe_status="$(curl -sS -o "$foe_body" -w '%{http_code}' -X POST \
    -H 'Content-Type: application/json' \
    -d '{"type":"sync_fail_task"}' "$BASE/task/?wait=20s&fail_on_error=true")"
[[ "$foe_status" == "502" ]] \
    || fail "?wait&fail_on_error=true on a failing task should be 502, got $foe_status: $(cat "$foe_body")"
jq -e '.task.exitCode == 3' "$foe_body" > /dev/null \
    || fail "fail_on_error response lost the exit code: $(cat "$foe_body")"

# A wait over tasks.sync.maxWait is a 400, not a silent clamp.
cap_status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
    -H 'Content-Type: application/json' \
    -d '{"type":"echo_task"}' "$BASE/task/?wait=99h")"
[[ "$cap_status" == "400" ]] || fail "?wait over maxWait should be 400, got $cap_status"

# ---------------------------------------------------------------------------
# Streaming submission -- POST /task/?wait&stream, and `blanket submit
# --follow` on top of it (turtlemonvh/blanket#27 PR 2).
#
# Both task types below sleep for a moment on purpose: the stream can
# only attach to a task's log files once the worker has created them and
# moved the task to RUNNING, so a task that starts and exits inside one
# poll interval would prove nothing about *live* delivery (its output
# would still arrive, but only in the terminal result event).
# ---------------------------------------------------------------------------

cat > "$WORKDIR/types/sync_stream_task.toml" <<'EOF'
tags = ["exec:bash", "os:unix"]
timeout = 20
description = "Writes output, then lingers, for the &stream smoke test"
command = "echo 'stream stdout line'; sleep 2; echo 'stream done'"
executor = "bash"
EOF

cat > "$WORKDIR/types/sync_follow_task.toml" <<'EOF'
tags = ["exec:bash", "os:unix"]
timeout = 20
description = "Writes to both streams and exits 3, for the --follow smoke test"
command = "echo 'follow stdout line'; echo 'follow stderr line' >&2; sleep 2; exit 3"
executor = "bash"
EOF

# The NDJSON stream: one JSON object per line, ending with a self-
# contained result event.
stream_body="$WORKDIR/stream.ndjson"
curl -fsS -X POST -H 'Content-Type: application/json' \
    -d '{"type":"sync_stream_task"}' "$BASE/task/?wait=60s&stream" -o "$stream_body" \
    || fail "streaming submit failed; body: $(cat "$stream_body" 2>/dev/null)"

jq -se 'all(.[]; has("ts") and has("taskId") and has("type"))' "$stream_body" > /dev/null \
    || fail "every stream event should carry ts/taskId/type: $(cat "$stream_body")"
jq -se 'any(.[]; .type == "state" and .state == "RUNNING")' "$stream_body" > /dev/null \
    || fail "stream carried no RUNNING state event: $(cat "$stream_body")"
jq -se 'any(.[]; .type == "log" and .stream == "stdout" and (.line | contains("stream stdout line")))' "$stream_body" > /dev/null \
    || fail "stream carried no stdout log event: $(cat "$stream_body")"
jq -se '.[-1] | .type == "result" and .waitOutcome == "completed" and .task.state == "SUCCESS" and .task.exitCode == 0' "$stream_body" > /dev/null \
    || fail "stream did not end with a completed result event: $(cat "$stream_body")"
# The result event repeats the output tail, so a client that reads only
# the last line still has everything.
jq -se '.[-1].stdout | contains("stream stdout line") and contains("stream done")' "$stream_body" > /dev/null \
    || fail "terminal result event did not repeat the stdout tail: $(cat "$stream_body")"

# `blanket submit --follow` consumes that stream: task stdout goes to the
# process's stdout, task stderr to its stderr, and the process exits with
# the task's own exit code.
follow_out="$WORKDIR/follow.out"
follow_err="$WORKDIR/follow.err"
set +e
"$REPO_ROOT/$BINARY" --config "$WORKDIR/config.json" submit \
    -t sync_follow_task --follow --wait-timeout 60s > "$follow_out" 2> "$follow_err"
follow_status=$?
set -e

[[ "$follow_status" -eq 3 ]] \
    || fail "blanket submit --follow should exit with the task's exit code 3, got $follow_status (stderr: $(cat "$follow_err"))"
grep -q 'follow stdout line' "$follow_out" \
    || fail "--follow did not write task stdout to its stdout: $(cat "$follow_out")"
grep -q 'follow stderr line' "$follow_err" \
    || fail "--follow did not write task stderr to its stderr: $(cat "$follow_err")"
if grep -q 'follow stderr line' "$follow_out"; then
    fail "--follow leaked a stderr line into stdout: $(cat "$follow_out")"
fi
grep -q 'exitCode=3' "$follow_err" \
    || fail "--follow did not print its completion summary: $(cat "$follow_err")"

kill "$WORKER_PID" 2>/dev/null || true
wait "$WORKER_PID" 2>/dev/null || true
WORKER_PID=""

# task-validate --json runs against the fixture type and produces a JSON
# array (the fixture is intentionally minimal, so warnings are expected —
# this only checks the command runs and emits well-formed JSON, not that
# the fixture is warning-free).
validate_json="$("$REPO_ROOT/$BINARY" --config "$WORKDIR/config.json" task-validate --json)" \
    || fail "task-validate --json exited non-zero unexpectedly: $validate_json"
echo "$validate_json" | jq -e 'type == "array"' > /dev/null \
    || fail "task-validate --json did not produce a JSON array: $validate_json"

# MCP: initialize -> notifications/initialized -> tools/list -> tools/call
# blanket_tasks. Confirms the streamable-HTTP handler is really mounted and
# at least one tool round-trips end to end against the built binary (not
# just in-process unit tests).
#
# Verified against a live server (curl -v): the go-sdk's streamable-HTTP
# handler responds with Content-Type: text/event-stream and SSE-frames the
# body as `event: message\ndata: {...json-rpc...}\n\n`, even for this
# single, non-streaming reply -- NOT `Content-Type: application/json` as
# might be assumed. The `grep -q '"..."'` checks below still work
# unmodified because they substring-match the raw SSE body without caring
# about the `event:`/`data:` framing around it. The Mcp-Session-Id response
# header on `initialize` matched expectations exactly (case as shown).
mcp_init_resp="$(curl -fsS -X POST "$BASE/mcp" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke-test","version":"0.0.0"}}}')"
echo "$mcp_init_resp" | grep -q '"protocolVersion"' \
    || fail "MCP initialize response missing protocolVersion: $mcp_init_resp"

mcp_session_id="$(curl -fsS -X POST "$BASE/mcp" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -D - -o /dev/null \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke-test","version":"0.0.0"}}}' \
    | grep -i '^mcp-session-id:' | tr -d '\r' | cut -d' ' -f2)"
[[ -n "$mcp_session_id" ]] || fail "MCP initialize did not return an Mcp-Session-Id header"

curl -fsS -X POST "$BASE/mcp" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $mcp_session_id" \
    -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' > /dev/null

mcp_tools_resp="$(curl -fsS -X POST "$BASE/mcp" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $mcp_session_id" \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}')"
echo "$mcp_tools_resp" | grep -q '"blanket_tasks"' \
    || fail "MCP tools/list missing blanket_tasks: $mcp_tools_resp"

mcp_call_resp="$(curl -fsS -X POST "$BASE/mcp" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $mcp_session_id" \
    -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"blanket_tasks","arguments":{}}}')"
echo "$mcp_call_resp" | grep -q '"content"' \
    || fail "MCP tools/call blanket_tasks did not return content: $mcp_call_resp"

# install.sh shell integration (issue #22): PATH + completion block.
# Exercises the installer's rc-file editing directly against a scratch
# HOME using the binary already built above, checking that two runs
# leave exactly one marked block (idempotent, not duplicated).
INSTALL_HOME="$WORKDIR/install-home"
mkdir -p "$INSTALL_HOME"
INSTALL_TEST_DIR="$INSTALL_HOME/.local/bin"

run_installer() {
    HOME="$INSTALL_HOME" \
    SHELL="/bin/bash" \
    BINARY_PATH="$REPO_ROOT/$BINARY" \
    INSTALL_DIR="$INSTALL_TEST_DIR" \
    INSTALL_SKILLS=0 \
    INSTALL_AUTOSTART=0 \
    INSTALL_SHELL_INTEGRATION=1 \
    sh "$REPO_ROOT/scripts/install.sh" < /dev/null > "$WORKDIR/install.log" 2>&1
}

run_installer || { cat "$WORKDIR/install.log" >&2; fail "install.sh (1st run) exited non-zero"; }
run_installer || { cat "$WORKDIR/install.log" >&2; fail "install.sh (2nd run) exited non-zero"; }

marker_count="$(grep -c '^# >>> blanket >>>$' "$INSTALL_HOME/.bashrc" || true)"
[[ "$marker_count" -eq 1 ]] \
    || fail "expected exactly one blanket marked block in .bashrc after two installs, got $marker_count"
grep -q 'blanket completion bash' "$INSTALL_HOME/.bashrc" \
    || fail ".bashrc missing bash completion sourcing line"
