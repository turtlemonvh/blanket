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

cleanup() {
    local status=$?
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
