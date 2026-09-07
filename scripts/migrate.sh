#!/usr/bin/env bash
# Schema, backup, and migrate tests for the built blanket binary
# (turtlemonvh/blanket#23 phase 4).
#
# These need a real process for the same reason scripts/restart.sh does,
# plus one of their own: BoltDB's exclusive flock is a *cross-process*
# invariant, and the whole design here turns on it. "The CLI can back up
# only when the server is down", "restore refuses while the lock is held",
# "the ops endpoint is the only way to back up a live install" — none of
# those statements has any meaning inside a single `go test` process.
#
# Covered here:
#   1. `blanket migrate --check` against a fresh database: reports v1,
#      exits 0, and writes nothing.
#   2. POST /ops/backup from loopback succeeds with the header and is
#      refused without it. (The non-loopback case is a Go handler test —
#      see server/serve_ops_test.go — because this harness only ever has
#      a loopback address to offer.)
#   3. `blanket backup` while the server is up goes via the server.
#   4. `blanket backup` with the server down opens the database directly.
#   5. A full backup -> mutate -> restore round trip through the CLI, and
#      a restore refused while the server holds the lock.
#
# Usage:
#   scripts/migrate.sh [path/to/blanket-binary]

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# shellcheck source=scripts/lib/harness.sh
source "$REPO_ROOT/scripts/lib/harness.sh"

harness_find_binary "${1:-}"

cleanup() {
    local status=$?
    harness_cleanup || true
    if [[ $status -eq 0 ]]; then
        echo "migrate: OK"
    else
        echo "migrate: FAILED (exit $status)" >&2
    fi
    exit $status
}
trap cleanup EXIT INT TERM

fail() {
    echo "migrate: FAIL — $*" >&2
    if [[ -n "${SERVER_LOG:-}" && -f "$SERVER_LOG" ]]; then
        echo "--- server.log ---" >&2
        cat "$SERVER_LOG" >&2
    fi
    exit 1
}

# blanket <args...> — run the CLI against the harness config from WORKDIR.
blanket() {
    (cd "$WORKDIR" && "$BINARY" --config "$CONFIG" "$@")
}

harness_init blanket-migrate

# --------------------------------------------------------------------------
# 1. `blanket migrate --check` on a fresh database.
# --------------------------------------------------------------------------
echo "migrate: --check against a fresh database"

# Bring the database into existence, then stop, so --check has something to
# read and nothing holding its lock.
harness_start_server
harness_wait_ready || exit 1
harness_stop_server TERM

set +e
check_out="$(blanket migrate --check 2>&1)"
check_rc=$?
set -e

[[ $check_rc -eq 0 ]] || fail "migrate --check should exit 0 on an up-to-date database, got $check_rc: $check_out"
grep -q 'schema version: 1' <<<"$check_out" || fail "expected schema version 1: $check_out"
grep -q 'target version: 1' <<<"$check_out" || fail "expected target version 1: $check_out"
grep -q 'Up to date' <<<"$check_out" || fail "expected 'Up to date': $check_out"

# --check must not have created a backups directory or written anything.
[[ ! -d "$WORKDIR/backups" ]] || fail "--check wrote a backups directory; it must change nothing"

echo "migrate:   ok — v1, nothing pending, nothing written"

# --------------------------------------------------------------------------
# 2. POST /ops/backup: loopback + header succeeds, header missing is 403.
# --------------------------------------------------------------------------
echo "migrate: POST /ops/backup"

harness_start_server
harness_wait_ready || exit 1

# Something in the database whose survival a restore can be checked against.
task_resp="$(curl -fsS -X POST -H 'Content-Type: application/json' \
    -d '{"type":"echo_task"}' "$BASE/task/")"
grep -q '"state":"WAITING"' <<<"$task_resp" || fail "could not create a task: $task_resp"
task_id="$(sed -n 's/.*"id":"\([0-9a-f]\{24\}\)".*/\1/p' <<<"$task_resp")"
[[ -n "$task_id" ]] || fail "could not read the task id out of: $task_resp"

backup_resp="$(curl -fsS -X POST -H "X-Blanket-Restart: smoke" "$BASE/ops/backup")"
grep -q '"path"' <<<"$backup_resp" || fail "ops/backup did not report a path: $backup_resp"
backup_path="$(sed -n 's/.*"path":"\([^"]*\)".*/\1/p' <<<"$backup_resp")"
[[ -f "$backup_path" ]] || fail "ops/backup reported $backup_path, which does not exist"

# Without the header it must be refused. The header's job is to force a
# CORS preflight the wildcard handler is carved out of; see
# server/serve_ops.go.
status="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/ops/backup")"
[[ "$status" == "403" ]] || fail "ops/backup without the header should be 403, got $status"

# And the browser preflight must not be approved.
preflight_origin="$(curl -s -D - -o /dev/null -X OPTIONS \
    -H 'Origin: https://evil.example' \
    -H 'Access-Control-Request-Method: POST' \
    -H 'Access-Control-Request-Headers: X-Blanket-Restart' \
    "$BASE/ops/backup" | grep -ci '^access-control-allow-origin' || true)"
[[ "$preflight_origin" == "0" ]] || fail "the wildcard CORS handler approved an /ops/ preflight"

echo "migrate:   ok — backup written to $backup_path, no header is 403, preflight refused"

# --------------------------------------------------------------------------
# 3. `blanket backup` with the server up goes via the server.
# --------------------------------------------------------------------------
echo "migrate: blanket backup with the server running"

cli_out="$(blanket backup)"
grep -q 'via the running server' <<<"$cli_out" \
    || fail "with the server up, the CLI must go through /ops/backup: $cli_out"

echo "migrate:   ok — $cli_out"

harness_stop_server TERM

# --------------------------------------------------------------------------
# 4. `blanket backup` with the server down opens the database directly.
# --------------------------------------------------------------------------
echo "migrate: blanket backup with the server stopped"

cli_out="$(blanket backup)"
grep -q 'taken directly' <<<"$cli_out" \
    || fail "with the server down, the CLI must open the database itself: $cli_out"

direct_backup="$(sed -n 's/^Wrote \(.*\) (the server.*/\1/p' <<<"$cli_out")"
[[ -f "$direct_backup" ]] || fail "reported $direct_backup, which does not exist"

# Retention: keep exactly 3, however many were taken. Two more back-to-back
# runs push the count past the limit -- and also prove the filenames don't
# collide when taken in the same second, which would silently lose one.
blanket backup > /dev/null
blanket backup > /dev/null

kept="$(find "$WORKDIR/backups" -maxdepth 1 -name 'blanket-*.db' | wc -l)"
[[ "$kept" -eq 3 ]] || fail "retention should keep exactly 3 backups, found $kept"

# The one we're about to restore from must be one of the survivors.
[[ -f "$direct_backup" ]] || fail "$direct_backup was pruned; it should be among the 3 newest"

echo "migrate:   ok — $kept backups kept, oldest pruned"

# --------------------------------------------------------------------------
# 5. Restore round trip, and a restore refused while the lock is held.
# --------------------------------------------------------------------------
echo "migrate: backup -> delete -> restore round trip"

# Delete the task, so restoring is observable as its return.
harness_start_server
harness_wait_ready || exit 1
curl -fsS -X DELETE "$BASE/task/$task_id" > /dev/null || fail "could not delete task $task_id"
after_delete="$(curl -fsS "$BASE/task/")"
grep -q "$task_id" <<<"$after_delete" && fail "task $task_id still listed after delete"

# A restore while the server holds the lock must be refused outright.
set +e
restore_out="$(blanket migrate --restore "$direct_backup" --yes 2>&1)"
restore_rc=$?
set -e
[[ $restore_rc -ne 0 ]] || fail "restore should be refused while the server holds the lock"
grep -qi 'in use' <<<"$restore_out" || fail "expected an 'in use' refusal, got: $restore_out"

echo "migrate:   ok — restore refused while the server is running"

harness_stop_server TERM

# Without --yes it must also refuse: this replaces a database.
set +e
noyes_out="$(blanket migrate --restore "$direct_backup" 2>&1)"
noyes_rc=$?
set -e
[[ $noyes_rc -ne 0 ]] || fail "restore without --yes should refuse"
grep -q 'Re-run with --yes' <<<"$noyes_out" || fail "expected a --yes prompt, got: $noyes_out"

restore_out="$(blanket migrate --restore "$direct_backup" --yes)"
grep -q 'Restored' <<<"$restore_out" || fail "restore did not report success: $restore_out"

harness_start_server
harness_wait_ready || fail "server would not start on the restored database"
after_restore="$(curl -fsS "$BASE/task/")"
grep -q "$task_id" <<<"$after_restore" \
    || fail "task $task_id should be back after restoring the backup that predates its deletion: $after_restore"

echo "migrate:   ok — task $task_id came back from $direct_backup"

harness_stop_server TERM
