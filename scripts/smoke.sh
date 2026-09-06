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

# Port selection, workdir, config generation, readiness polling and trap
# cleanup all live in the shared subprocess harness -- scripts/restart.sh
# needs the same scaffolding. See scripts/lib/harness.sh.
# shellcheck source=scripts/lib/harness.sh
source "$REPO_ROOT/scripts/lib/harness.sh"

harness_find_binary "${1:-}"
harness_init blanket-smoke

# Not part of the shared harness: only smoke.sh runs a live worker
# alongside the server (turtlemonvh/blanket#27's ?wait checks need one to
# actually claim and run tasks; scripts/restart.sh has no equivalent
# need), so it's stopped locally in this script's own cleanup rather than
# folded into scripts/lib/harness.sh.
WORKER_PID=""

cleanup() {
    local status=$?
    if [[ -n "$WORKER_PID" ]] && kill -0 "$WORKER_PID" 2>/dev/null; then
        kill "$WORKER_PID" 2>/dev/null || true
        wait "$WORKER_PID" 2>/dev/null || true
    fi
    harness_cleanup || true
    if [[ $status -eq 0 ]]; then
        echo "smoke: OK"
    else
        echo "smoke: FAILED (exit $status)" >&2
    fi
    exit $status
}
trap cleanup EXIT INT TERM

harness_start_server
harness_wait_ready || exit 1

fail() {
    echo "smoke: FAIL — $*" >&2
    echo "--- server.log ---" >&2
    cat "$SERVER_LOG" >&2
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

# Deliberately NOT wrapped in a `( cd … ; cmd & )` subshell -- same reason
# as harness_start_server: that makes the worker a grandchild, and `kill`
# + `wait` below need WORKER_PID to be a direct child of this shell.
prev_dir="$PWD"
cd "$WORKDIR"
"$BINARY" --config "$CONFIG" worker \
    --tags "exec:bash,os:unix" \
    --checkinterval 0.5 \
    --logfile "$WORKDIR/worker.log" \
    > "$WORKDIR/worker.stdout.log" 2>&1 &
WORKER_PID=$!
cd "$prev_dir"

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

kill "$WORKER_PID" 2>/dev/null || true
wait "$WORKER_PID" 2>/dev/null || true
WORKER_PID=""

# task-validate --json runs against the fixture type and produces a JSON
# array (the fixture is intentionally minimal, so warnings are expected —
# this only checks the command runs and emits well-formed JSON, not that
# the fixture is warning-free).
validate_json="$("$BINARY" --config "$CONFIG" task-validate --json)" \
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
    BINARY_PATH="$BINARY" \
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
