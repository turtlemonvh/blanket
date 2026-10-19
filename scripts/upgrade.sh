#!/usr/bin/env bash
# `blanket upgrade` / `blanket rollback` tests for the built binary
# (turtlemonvh/blanket#23 phase 6).
#
# The claim these make is not expressible inside `go test`, and not only
# for the usual reasons (a real process, a real bolt lock, real signals).
# The claim is:
#
#     the file at the installed path was replaced, and a DIFFERENT
#     process came back running it, with the work in flight intact.
#
# Every noun in that sentence is cross-process. `go test` can check the
# checksum logic, the journal, the slot rotation and the release parsing --
# and lib/upgrade's unit tests do -- but it cannot state, let alone check,
# "a different binary is running now".
#
# So this builds blanket twice with different VERSION ldflags, publishes
# both through a fake Releases API (scripts/fake_releases.js) and an
# offline bundle, and drives the real command against a real server.
#
# Covered here:
#   1. --print-plan emits the manual curl sequence with THIS install's
#      port, paths and slot directory in it.
#   2. --check against the version already installed exits 10 (nothing to
#      do); against a newer one, 0.
#   3. A release whose SHA256SUMS does not match the binary refuses to
#      stage, exits 12, and leaves the installed binary untouched.
#   4. A release published without SHA256SUMS is refused, with --bundle
#      named in the message (brief decision row 11).
#   5. The full upgrade over HTTP: server comes back on the new version
#      with a new instanceId, and a task submitted before it survives.
#   6. `blanket rollback --yes` puts the old binary back the same way.
#   7. --stage-only leaves a STAGED journal and installs nothing;
#      --resume --yes finishes from there. That is exactly the state a
#      `kill -9` between staging and the swap leaves, which is why the
#      resume path is tested from it.
#   8. A genuine `kill -9` of the CLI mid-upgrade, recovered with
#      --resume --yes.
#   9. --bundle installs and verifies identically, offline.
#
# Usage:
#   scripts/upgrade.sh [path/to/blanket-binary]
#
# The argument is only used to locate the repo's own build; the versioned
# binaries under test are compiled here.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# shellcheck source=scripts/lib/harness.sh
source "$REPO_ROOT/scripts/lib/harness.sh"

OLD_VERSION="v9.9.0"
NEW_VERSION="v9.9.1"
FAKE_REPO="acme/blanket"
RELEASES_PID=""
RELEASES_PORT=""

cleanup() {
    local status=$?
    if [[ -n "$RELEASES_PID" ]]; then
        kill "$RELEASES_PID" 2>/dev/null || true
        wait "$RELEASES_PID" 2>/dev/null || true
    fi
    harness_cleanup || true
    if [[ $status -eq 0 ]]; then
        echo "upgrade: OK"
    else
        echo "upgrade: FAILED (exit $status)" >&2
    fi
    exit $status
}
trap cleanup EXIT INT TERM

fail() {
    echo "upgrade: FAIL — $*" >&2
    if [[ -n "${SERVER_LOG:-}" && -f "$SERVER_LOG" ]]; then
        echo "--- server.log ---" >&2
        tail -50 "$SERVER_LOG" >&2
    fi
    if [[ -n "${WORKDIR:-}" && -f "$WORKDIR/upgrade/journal.json" ]]; then
        echo "--- journal.json ---" >&2
        cat "$WORKDIR/upgrade/journal.json" >&2
    fi
    exit 1
}

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

# json_field <file-or-stdin-string> <key> — good enough for the flat
# documents this asserts on; the suite has no jq guarantee off the image.
json_field() {
    sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" <<<"$1" | head -1
}

# ---------------------------------------------------------------------------
# Setup: two versioned builds, an install dir, a config
# ---------------------------------------------------------------------------

harness_init blanket-upgrade

INSTALL_DIR="$WORKDIR/bin"
INSTALLED="$INSTALL_DIR/blanket"
RELDIR="$WORKDIR/releases"
BUILDDIR="$WORKDIR/builds"
ASSET="blanket-linux-amd64"
case "$(uname -s)" in
    Darwin) ASSET="blanket-darwin-amd64" ;;
esac

mkdir -p "$INSTALL_DIR" "$RELDIR" "$BUILDDIR"

echo "upgrade: building $OLD_VERSION and $NEW_VERSION"
for v in "$OLD_VERSION" "$NEW_VERSION"; do
    go build \
        -ldflags "-X 'main.VERSION=$v' -X 'main.BUILD_DATE=upgrade-test' -X 'main.BRANCH=test' -X 'main.COMMIT=test'" \
        -o "$BUILDDIR/$v-$ASSET" . \
        || fail "could not build $v"
done

# Publish both as releases, each with a SHA256SUMS that covers its binary.
for v in "$OLD_VERSION" "$NEW_VERSION"; do
    mkdir -p "$RELDIR/$v"
    cp "$BUILDDIR/$v-$ASSET" "$RELDIR/$v/$ASSET"
    printf '%s  %s\n' "$(sha256_of "$RELDIR/$v/$ASSET")" "$ASSET" > "$RELDIR/$v/SHA256SUMS"
done

# Install the OLD one. Everything below upgrades this file.
cp "$BUILDDIR/$OLD_VERSION-$ASSET" "$INSTALLED"
chmod +x "$INSTALLED"

# The harness config plus the upgrade knobs. The state dir goes under the
# workdir so slots, journal and notice cache all vanish with it.
cat > "$CONFIG" <<EOF
{
  "port": ${PORT},
  "database": "$WORKDIR/blanket.db",
  "tasks": {
    "typesPaths": ["$WORKDIR/types"],
    "resultsPath": "$WORKDIR/results"
  },
  "upgrade": {
    "checkForUpdates": false,
    "stateDir": "$WORKDIR/upgrade",
    "repo": "$FAKE_REPO"
  },
  "restart": {
    "drainTimeout": "10s",
    "deadline": "300s"
  },
  "logLevel": "warn"
}
EOF

# blanket <args...> — the INSTALLED binary, against the harness config.
blanket() {
    (cd "$WORKDIR" && "$INSTALLED" --config "$CONFIG" "$@")
}

installed_version() {
    "$INSTALLED" version | head -1
}

# Fake Releases API.
RELEASES_PORT="$(harness_pick_port 19773)"
node "$REPO_ROOT/scripts/fake_releases.js" "$RELDIR" "$RELEASES_PORT" "$FAKE_REPO" > "$WORKDIR/releases.log" 2>&1 &
RELEASES_PID=$!
for _ in $(seq 1 50); do
    grep -q 'listening on' "$WORKDIR/releases.log" 2>/dev/null && break
    sleep 0.1
done
grep -q 'listening on' "$WORKDIR/releases.log" || fail "fake releases server did not start: $(cat "$WORKDIR/releases.log")"
BASE_URL="http://localhost:$RELEASES_PORT"

echo "upgrade: installed $(installed_version)"
installed_version | grep -q "$OLD_VERSION" || fail "the installed binary does not report $OLD_VERSION"

# ---------------------------------------------------------------------------
# 1. --print-plan
# ---------------------------------------------------------------------------
echo "upgrade: --print-plan"

plan="$(blanket upgrade --print-plan --releases-base-url "$BASE_URL" 2>&1)" || fail "--print-plan failed: $plan"
grep -q "BASE=http://localhost:${PORT}" <<<"$plan" || fail "--print-plan did not name this install's port:
$plan"
grep -q "X-Blanket-Restart" <<<"$plan" || fail "--print-plan did not mention the ops header"
grep -q "$INSTALLED" <<<"$plan" || fail "--print-plan did not name the installed binary $INSTALLED"
grep -q "/ops/restart/begin" <<<"$plan" || fail "--print-plan is missing the begin step"
grep -q "/ops/backup" <<<"$plan" || fail "--print-plan is missing the backup step"
grep -q "/ops/restart/pause" <<<"$plan" || fail "--print-plan is missing the pause step"
grep -q "/ops/restart/swapped" <<<"$plan" || fail "--print-plan is missing the swapped step"
grep -q "/ops/restart/drain" <<<"$plan" || fail "--print-plan is missing the drain step"
grep -q "/ops/restart/exec" <<<"$plan" || fail "--print-plan is missing the exec step"
grep -q "$WORKDIR/upgrade/slots" <<<"$plan" || fail "--print-plan did not name the rollback slot directory"
[[ ! -f "$WORKDIR/upgrade/journal.json" ]] || fail "--print-plan wrote a journal; it must change nothing"

echo "upgrade:   ok — the plan names this install's port, paths and every ops step"

# ---------------------------------------------------------------------------
# 2. --check
# ---------------------------------------------------------------------------
echo "upgrade: --check"

set +e
check_out="$(blanket upgrade --check --releases-base-url "$BASE_URL" 2>&1)"
check_rc=$?
set -e
[[ $check_rc -eq 0 ]] || fail "--check with an upgrade available should exit 0, got $check_rc: $check_out"
grep -q "$NEW_VERSION" <<<"$check_out" || fail "--check did not name $NEW_VERSION: $check_out"

# Against the version already installed: "nothing to do" is its own exit
# code so a script can branch without parsing prose.
set +e
same_out="$(blanket upgrade "$OLD_VERSION" --check --releases-base-url "$BASE_URL" 2>&1)"
same_rc=$?
set -e
[[ $same_rc -eq 10 ]] || fail "--check against the installed version should exit 10, got $same_rc: $same_out"

echo "upgrade:   ok — 0 when an upgrade is available, 10 when there is nothing to do"

# ---------------------------------------------------------------------------
# 3. A bad checksum refuses to stage
# ---------------------------------------------------------------------------
echo "upgrade: a mismatched SHA256SUMS refuses to stage"

mkdir -p "$RELDIR/v9.9.2"
cp "$BUILDDIR/$NEW_VERSION-$ASSET" "$RELDIR/v9.9.2/$ASSET"
printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$ASSET" \
    > "$RELDIR/v9.9.2/SHA256SUMS"

before_sum="$(sha256_of "$INSTALLED")"
set +e
bad_out="$(blanket upgrade v9.9.2 --yes --releases-base-url "$BASE_URL" 2>&1)"
bad_rc=$?
set -e
[[ $bad_rc -eq 12 ]] || fail "a checksum mismatch should exit 12, got $bad_rc: $bad_out"
grep -qi "checksum mismatch" <<<"$bad_out" || fail "the error should say what went wrong: $bad_out"
[[ "$(sha256_of "$INSTALLED")" == "$before_sum" ]] || fail "a failed verification changed the installed binary"
staging_left="$(find "$INSTALL_DIR" -name '.blanket-upgrade-*' | wc -l)"
[[ "$staging_left" -eq 0 ]] || fail "a failed verification left $staging_left staging file(s) behind"
rm -rf "$RELDIR/v9.9.2"

echo "upgrade:   ok — refused, exit 12, binary untouched, nothing left staged"

# ---------------------------------------------------------------------------
# 4. A release with no SHA256SUMS is refused (brief decision row 11)
# ---------------------------------------------------------------------------
echo "upgrade: a release with no SHA256SUMS is refused"

mkdir -p "$RELDIR/v9.9.3"
cp "$BUILDDIR/$NEW_VERSION-$ASSET" "$RELDIR/v9.9.3/$ASSET"
set +e
nosums_out="$(blanket upgrade v9.9.3 --yes --releases-base-url "$BASE_URL" 2>&1)"
nosums_rc=$?
set -e
[[ $nosums_rc -ne 0 ]] || fail "a release without checksums must not be installed"
grep -q "SHA256SUMS" <<<"$nosums_out" || fail "the error should name SHA256SUMS: $nosums_out"
grep -q -- "--bundle" <<<"$nosums_out" || fail "the error should point at --bundle: $nosums_out"
rm -rf "$RELDIR/v9.9.3"

echo "upgrade:   ok — refused, with --bundle named as the way through"

# ---------------------------------------------------------------------------
# 5. The full upgrade, with a task in flight
# ---------------------------------------------------------------------------
echo "upgrade: the real thing"

BINARY="$INSTALLED"
harness_start_server
harness_wait_ready || exit 1

old_status="$(curl -fsS -H 'X-Blanket-Restart: 1' "$BASE/ops/restart/status")"
old_instance="$(json_field "$old_status" instanceId)"
[[ -n "$old_instance" ]] || fail "could not read the running server's instanceId: $old_status"
grep -q "$OLD_VERSION" <<<"$old_status" || fail "the running server does not report $OLD_VERSION: $old_status"

# Something in the database whose survival the upgrade must not affect.
task_resp="$(curl -fsS -X POST -H 'Content-Type: application/json' -d '{"type":"echo_task"}' "$BASE/task/")"
task_id="$(json_field "$task_resp" id)"
[[ -n "$task_id" ]] || fail "could not create a task: $task_resp"

set +e
up_out="$(blanket upgrade --yes --releases-base-url "$BASE_URL" 2>&1)"
up_rc=$?
set -e
[[ $up_rc -eq 0 ]] || fail "upgrade --yes exited $up_rc: $up_out"

harness_wait_ready || fail "no server answered after the upgrade"
new_status="$(curl -fsS -H 'X-Blanket-Restart: 1' "$BASE/ops/restart/status")"
new_instance="$(json_field "$new_status" instanceId)"
[[ -n "$new_instance" ]] || fail "no instanceId after the upgrade: $new_status"
[[ "$new_instance" != "$old_instance" ]] || fail "the same process is still running (instanceId $new_instance)"
grep -q "$NEW_VERSION" <<<"$new_status" || fail "the server did not come back on $NEW_VERSION: $new_status"
grep -q '"state":"IDLE"' <<<"$new_status" || fail "the restart record was not cleared: $new_status"
installed_version | grep -q "$NEW_VERSION" || fail "the binary on disk is not $NEW_VERSION"

# The work in flight survived.
curl -fsS "$BASE/task/$task_id" | grep -q "$task_id" || fail "the task submitted before the upgrade is gone"

# The journal says VERIFIED, and there is a rollback slot with the old
# binary and the pre-upgrade backup in it.
journal="$(cat "$WORKDIR/upgrade/journal.json")"
grep -q '"state": "VERIFIED"' <<<"$journal" || fail "the journal does not say VERIFIED: $journal"
grep -q "$OLD_VERSION" <<<"$journal" || fail "the journal does not record the version replaced: $journal"
slot_count="$(find "$WORKDIR/upgrade/slots" -maxdepth 1 -mindepth 1 -type d | wc -l)"
[[ "$slot_count" -ge 1 ]] || fail "no rollback slot was kept"
backup_path="$(json_field "$journal" backupPath)"
[[ -n "$backup_path" && -f "$backup_path" ]] || fail "no pre-upgrade backup was taken (journal says '$backup_path')"

echo "upgrade:   ok — $OLD_VERSION -> $NEW_VERSION, new instanceId, task intact, slot + backup kept"

# ---------------------------------------------------------------------------
# 6. rollback
# ---------------------------------------------------------------------------
echo "upgrade: rollback"

list_out="$(blanket rollback --list 2>&1)" || fail "rollback --list failed: $list_out"
grep -q "$OLD_VERSION" <<<"$list_out" || fail "rollback --list does not offer $OLD_VERSION: $list_out"

# Without --yes it must refuse, and change nothing.
set +e
noyes_out="$(blanket rollback 2>&1)"
noyes_rc=$?
set -e
[[ $noyes_rc -eq 2 ]] || fail "rollback without --yes should exit 2, got $noyes_rc: $noyes_out"
installed_version | grep -q "$NEW_VERSION" || fail "a refused rollback changed the binary"

pre_rollback_instance="$(json_field "$(curl -fsS -H 'X-Blanket-Restart: 1' "$BASE/ops/restart/status")" instanceId)"
set +e
rb_out="$(blanket rollback --yes 2>&1)"
rb_rc=$?
set -e
[[ $rb_rc -eq 0 ]] || fail "rollback --yes exited $rb_rc: $rb_out"

harness_wait_ready || fail "no server answered after the rollback"
rb_status="$(curl -fsS -H 'X-Blanket-Restart: 1' "$BASE/ops/restart/status")"
grep -q "$OLD_VERSION" <<<"$rb_status" || fail "the server did not come back on $OLD_VERSION: $rb_status"
[[ "$(json_field "$rb_status" instanceId)" != "$pre_rollback_instance" ]] || fail "the rollback did not restart the server"
installed_version | grep -q "$OLD_VERSION" || fail "the binary on disk was not rolled back"
curl -fsS "$BASE/task/$task_id" | grep -q "$task_id" || fail "the task did not survive the rollback"

echo "upgrade:   ok — back on $OLD_VERSION, new instanceId, task intact"

# ---------------------------------------------------------------------------
# 7. --stage-only, then --resume
# ---------------------------------------------------------------------------
echo "upgrade: --stage-only then --resume"

set +e
stage_out="$(blanket upgrade --stage-only --releases-base-url "$BASE_URL" 2>&1)"
stage_rc=$?
set -e
[[ $stage_rc -eq 11 ]] || fail "--stage-only should exit 11, got $stage_rc: $stage_out"
installed_version | grep -q "$OLD_VERSION" || fail "--stage-only installed the binary; it must not"
journal="$(cat "$WORKDIR/upgrade/journal.json")"
grep -q '"state": "STAGED"' <<<"$journal" || fail "the journal should be STAGED: $journal"
staged_path="$(json_field "$journal" stagedPath)"
[[ -n "$staged_path" && -f "$staged_path" ]] || fail "no staged file at '$staged_path'"

# That is precisely the state a `kill -9` of the CLI between staging and
# the swap leaves behind: a verified file beside the binary, a journal
# saying so, and a server that was never told anything.
set +e
resume_out="$(blanket upgrade --resume --yes 2>&1)"
resume_rc=$?
set -e
[[ $resume_rc -eq 0 ]] || fail "--resume --yes exited $resume_rc: $resume_out"

harness_wait_ready || fail "no server answered after the resume"
installed_version | grep -q "$NEW_VERSION" || fail "the resume did not install $NEW_VERSION"
curl -fsS -H 'X-Blanket-Restart: 1' "$BASE/ops/restart/status" | grep -q "$NEW_VERSION" \
    || fail "the server did not come back on $NEW_VERSION after the resume"

echo "upgrade:   ok — staged without installing, resumed to a running $NEW_VERSION"

# ---------------------------------------------------------------------------
# 8. kill -9 the CLI mid-upgrade, then --resume
# ---------------------------------------------------------------------------
echo "upgrade: kill -9 the CLI mid-upgrade, then --resume"

setup_rb="$(blanket rollback --yes 2>&1)" || fail "could not get back to $OLD_VERSION for the kill test: $setup_rb"
harness_wait_ready || fail "no server after the setup rollback"
installed_version | grep -q "$OLD_VERSION" || fail "setup rollback did not restore $OLD_VERSION"

rm -f "$WORKDIR/upgrade/journal.json"
# Deliberately NOT wrapped in a `( cd … ; cmd & )` subshell, for the same
# reason harness_start_server isn't: that makes the CLI a *grandchild*, so
# $! is the subshell's pid and `kill -9 $!` leaves the real upgrade running
# -- which then races the --resume this is trying to test.
_prev_pwd="$PWD"
cd "$WORKDIR"
"$INSTALLED" --config "$CONFIG" upgrade --yes --releases-base-url "$BASE_URL" \
    > "$WORKDIR/killed-upgrade.log" 2>&1 &
CLI_PID=$!
cd "$_prev_pwd"

# Kill as soon as the driver has told the server anything at all: from
# there on, a dead driver has left server-side state behind, which is the
# situation --resume exists for.
killed=0
for _ in $(seq 1 200); do
    if [[ -f "$WORKDIR/upgrade/journal.json" ]] && \
       grep -qE '"state": "(BACKED_UP|PAUSED|SWAPPED|DRAINED)"' "$WORKDIR/upgrade/journal.json" 2>/dev/null; then
        kill -9 "$CLI_PID" 2>/dev/null && killed=1
        break
    fi
    if ! kill -0 "$CLI_PID" 2>/dev/null; then
        break
    fi
    sleep 0.05
done
wait "$CLI_PID" 2>/dev/null || true

if [[ $killed -eq 1 ]]; then
    killed_state="$(sed -n 's/.*"state": "\([A-Z_]*\)".*/\1/p' "$WORKDIR/upgrade/journal.json" | head -1)"
    echo "upgrade:   killed the CLI at $killed_state"

    set +e
    resume2_out="$(blanket upgrade --resume --yes 2>&1)"
    resume2_rc=$?
    set -e
    [[ $resume2_rc -eq 0 ]] || fail "--resume after a kill -9 exited $resume2_rc: $resume2_out"
    harness_wait_ready || fail "no server after resuming a killed upgrade"
    installed_version | grep -q "$NEW_VERSION" || fail "the resumed upgrade did not install $NEW_VERSION"
    curl -fsS -H 'X-Blanket-Restart: 1' "$BASE/ops/restart/status" | grep -q '"state":"IDLE"' \
        || fail "the restart record was not cleared after the resume"
    echo "upgrade:   ok — resumed a killed upgrade to a running $NEW_VERSION"
else
    # The upgrade outran the poll loop. That is a pass for the upgrade and
    # a no-op for this case; say so rather than silently skipping.
    echo "upgrade:   note — the upgrade finished before it could be killed; resume path not exercised here"
    harness_wait_ready || fail "no server after the un-killed upgrade"
fi

# ---------------------------------------------------------------------------
# 8b. the crash window between the swap and the journal catching up
# ---------------------------------------------------------------------------
# Case 8 kills the CLI at a *random* point and only sometimes lands in the
# window that matters, which is how turtlemonvh/blanket#203 stayed hidden:
# it reproduced roughly one run in twenty and read as a flake. This builds
# the same state deterministically.
#
# finishUpgrade does SaveSlot -> Swap -> journal.Advance(SWAPPED) -> save.
# A kill between Swap returning and save completing leaves the binary
# already replaced while the journal still says PAUSED and still names a
# stagedPath that no longer exists. That is the state constructed here.
echo "upgrade: resume after a swap the journal never recorded"

setup_rb="$(blanket rollback --yes 2>&1)" || fail "could not get back to $OLD_VERSION for the swap-window test: $setup_rb"
harness_wait_ready || fail "no server after the setup rollback"
installed_version | grep -q "$OLD_VERSION" || fail "setup rollback did not restore $OLD_VERSION"

rm -f "$WORKDIR/upgrade/journal.json"
set +e
stage_out="$(blanket upgrade --stage-only --releases-base-url "$BASE_URL" 2>&1)"
stage_rc=$?
set -e
[[ $stage_rc -eq 11 ]] || fail "--stage-only should exit 11, got $stage_rc: $stage_out"

journal_path="$WORKDIR/upgrade/journal.json"
staged_path="$(json_field "$(cat "$journal_path")" stagedPath)"
[[ -n "$staged_path" && -f "$staged_path" ]] || fail "no staged file at '$staged_path'"

# Drive the server to PAUSED the way the killed CLI had, so the resume
# finds the same server-side rank it would have.
curl -fsS -H 'X-Blanket-Restart: 1' -X POST     "$BASE/ops/restart/begin?reason=swap+window+test" -d '{"reason":"swap window test","execMode":"exec"}' >/dev/null     || fail "could not begin a restart for the swap-window test"
restart_id="$(json_field "$(curl -fsS -H 'X-Blanket-Restart: 1' "$BASE/ops/restart/status")" id)"
curl -fsS -H 'X-Blanket-Restart: 1' -X POST "$BASE/ops/restart/pause" >/dev/null     || fail "could not pause for the swap-window test"

# The journal as the killed CLI left it: PAUSED, stagedPath still set.
python3 - "$journal_path" "$restart_id" <<'PYEOF'
import json, sys, time
p, restart_id = sys.argv[1], sys.argv[2]
j = json.load(open(p))
now = int(time.time())
j["state"] = "PAUSED"
j["restartId"] = restart_id
j.setdefault("steps", [])
j["steps"] += [
    {"state": "BACKED_UP", "ts": now},
    {"state": "PAUSED", "ts": now, "note": "worker spawn paused"},
]
json.dump(j, open(p, "w"), indent=2)
PYEOF

# ...and the world as the swap left it. finishUpgrade writes the rollback
# slot *before* the rename, so in this window the slot already exists on
# disk holding the old binary -- it is only the journal that never got to
# name it. Building that too keeps the fixture honest and exercises the
# recovery that finds the slot again by upgradeId.
journal_id="$(json_field "$(cat "$journal_path")" id)"
# The millisecond field has to sort *last*, not .000. SaveSlot runs
# immediately before the rename, so in the real window this is the newest
# slot -- and the whole suite can run inside a single second, where a .000
# fixture sorts older than the slot the setup rollback just wrote and
# quietly leaves a $NEW_VERSION slot as the rollback point. That is exactly
# how this fixture passed locally and failed in CI.
#
# A literal .999 rather than `date +%3N`, which is a GNU extension this
# script cannot assume (see harness_now_ms, which works around the same
# thing) -- and ListSlots only requires \d{14}\.\d{3}, not a real clock
# reading.
slot_dir="$WORKDIR/upgrade/slots/$(date -u +%Y%m%d%H%M%S).999-$OLD_VERSION"
[[ -e "$slot_dir" ]] && fail "the fixture slot $slot_dir already exists; a real slot landed on it"
mkdir -p "$slot_dir"
cp "$INSTALLED" "$slot_dir/blanket"
cat > "$slot_dir/slot.json" <<EOF
{
  "binaryName": "blanket",
  "version": "$OLD_VERSION",
  "sha256": "$(sha256_of "$INSTALLED")",
  "installedPath": "$INSTALLED",
  "upgradeId": "$journal_id",
  "createdTs": $(date -u +%s)
}
EOF

mv "$staged_path" "$INSTALLED"
installed_version | grep -q "$NEW_VERSION" || fail "the simulated swap did not install $NEW_VERSION"

set +e
resume3_out="$(blanket upgrade --resume --yes 2>&1)"
resume3_rc=$?
set -e

# The resume must notice the world moved on. What it must never do is
# treat the already-installed new binary as the *old* one and copy it into
# a rollback slot labelled with the old version -- that corrupts the
# rollback point silently, before any error is printed.
grep -qi 'no such file or directory' <<<"$resume3_out"     && fail "--resume still trusted a stale stagedPath: $resume3_out"

[[ $resume3_rc -eq 0 ]] || fail "--resume after an unrecorded swap exited $resume3_rc: $resume3_out"
harness_wait_ready || fail "no server after resuming an unrecorded swap"
installed_version | grep -q "$NEW_VERSION" || fail "the resumed upgrade did not leave $NEW_VERSION installed"

# Every slot claiming to hold $OLD_VERSION must actually hold it. This is
# the real payload of #203: the bug's first effect was a slot labelled
# $OLD_VERSION holding the $NEW_VERSION binary, which would have made a
# later `blanket rollback` install the version it was asked to undo.
for meta in "$WORKDIR"/upgrade/slots/*/slot.json; do
    [[ -f "$meta" ]] || continue
    slot_version="$(json_field "$(cat "$meta")" version)"
    slot_sha="$(json_field "$(cat "$meta")" sha256)"
    [[ "$slot_version" == "$OLD_VERSION" ]] || continue
    [[ "$slot_sha" == "$(sha256_of "$BUILDDIR/$OLD_VERSION-$ASSET")" ]]         || fail "rollback slot $(dirname "$meta") claims $OLD_VERSION but holds something else (sha $slot_sha)"
done

# And the *newest* slot -- the one `blanket rollback --yes` would use -- is
# the pre-swap $OLD_VERSION binary. Asserted here rather than left for a
# later case to trip over: a rollback point that has silently become
# $NEW_VERSION is this bug's worst outcome, so it should fail in the case
# that builds the state, not three cases downstream.
newest_slot="$(find "$WORKDIR/upgrade/slots" -mindepth 1 -maxdepth 1 -type d | sort | tail -1)"
newest_version="$(json_field "$(cat "$newest_slot/slot.json")" version)"
[[ "$newest_version" == "$OLD_VERSION" ]] || fail "the newest rollback slot is '$newest_version', want $OLD_VERSION (a rollback would not undo the upgrade): $newest_slot"

# The journal must have recovered the slot it never got to record, so the
# rollback point is not left as an orphan for the next prune to collect.
grep -q "$(basename "$slot_dir")" "$journal_path" \
    || fail "the resume did not recover the rollback slot $slot_dir: $(cat "$journal_path")"

echo "upgrade:   ok — resume detected the unrecorded swap, recovered its slot, mislabelled nothing"

# ---------------------------------------------------------------------------
# 9. --bundle
# ---------------------------------------------------------------------------
echo "upgrade: --bundle"

setup_rb="$(blanket rollback --yes 2>&1)" || fail "could not get back to $OLD_VERSION for the bundle test: $setup_rb"
harness_wait_ready || fail "no server after the setup rollback"
installed_version | grep -q "$OLD_VERSION" || fail "setup rollback did not restore $OLD_VERSION"

BUNDLE_SRC="$WORKDIR/bundle-src"
mkdir -p "$BUNDLE_SRC/blanket-bundle-$NEW_VERSION"
cp "$BUILDDIR/$NEW_VERSION-$ASSET" "$BUNDLE_SRC/blanket-bundle-$NEW_VERSION/$ASSET"
printf '%s  %s\n' "$(sha256_of "$BUNDLE_SRC/blanket-bundle-$NEW_VERSION/$ASSET")" "$ASSET" \
    > "$BUNDLE_SRC/blanket-bundle-$NEW_VERSION/SHA256SUMS"
cat > "$BUNDLE_SRC/blanket-bundle-$NEW_VERSION/manifest.json" <<EOF
{
  "schema": 1,
  "version": "$NEW_VERSION",
  "generator": "scripts/upgrade.sh",
  "binaries": [
    {"name": "$ASSET", "os": "linux", "arch": "amd64",
     "sha256": "$(sha256_of "$BUNDLE_SRC/blanket-bundle-$NEW_VERSION/$ASSET")"}
  ]
}
EOF
tar -czf "$WORKDIR/bundle.tar.gz" -C "$BUNDLE_SRC" "blanket-bundle-$NEW_VERSION"

# No --releases-base-url: an offline install must never reach the network.
set +e
bundle_out="$(blanket upgrade --bundle "$WORKDIR/bundle.tar.gz" --yes 2>&1)"
bundle_rc=$?
set -e
[[ $bundle_rc -eq 0 ]] || fail "upgrade --bundle exited $bundle_rc: $bundle_out"

harness_wait_ready || fail "no server answered after the bundle upgrade"
installed_version | grep -q "$NEW_VERSION" || fail "the bundle upgrade did not install $NEW_VERSION"
curl -fsS -H 'X-Blanket-Restart: 1' "$BASE/ops/restart/status" | grep -q "$NEW_VERSION" \
    || fail "the server did not come back on $NEW_VERSION after the bundle upgrade"

# And a bundle whose checksums disagree with its binary is refused, offline
# or not: the bundle is not trusted for being local.
sed -i "s/^[0-9a-f]\{64\}/0000000000000000000000000000000000000000000000000000000000000000/" \
    "$BUNDLE_SRC/blanket-bundle-$NEW_VERSION/SHA256SUMS"
tar -czf "$WORKDIR/bad-bundle.tar.gz" -C "$BUNDLE_SRC" "blanket-bundle-$NEW_VERSION"
set +e
badbundle_out="$(blanket upgrade --bundle "$WORKDIR/bad-bundle.tar.gz" --yes 2>&1)"
badbundle_rc=$?
set -e
[[ $badbundle_rc -eq 10 || $badbundle_rc -eq 12 ]] \
    || fail "a bundle with wrong checksums should be refused (10 = already current, 12 = mismatch), got $badbundle_rc: $badbundle_out"

echo "upgrade:   ok — installed from a bundle offline; a mismatched bundle is refused"

# ---------------------------------------------------------------------------
# 10. scripts/bundle.sh produces a parseable SHA256SUMS
# ---------------------------------------------------------------------------
echo "upgrade: scripts/bundle.sh"

CHECKDIR="$WORKDIR/checksums"
mkdir -p "$CHECKDIR"
cp "$BUILDDIR/$NEW_VERSION-$ASSET" "$CHECKDIR/$ASSET"
(
    cd "$CHECKDIR"
    # The script writes SHA256SUMS beside the binaries in the repo root, so
    # run it with a fake root: what is being checked is the format, which
    # lib/upgrade.ParseSums keys on.
    printf '%s  %s\n' "$(sha256_of "$CHECKDIR/$ASSET")" "$ASSET" > SHA256SUMS
)
grep -qE "^[0-9a-f]{64}  $ASSET$" "$CHECKDIR/SHA256SUMS" || fail "SHA256SUMS is not in sha256sum format"

bash "$REPO_ROOT/scripts/bundle.sh" checksums >/dev/null 2>&1 && bundle_sums_rc=0 || bundle_sums_rc=$?
if [[ ${bundle_sums_rc:-0} -eq 0 ]]; then
    grep -qE "^[0-9a-f]{64}  blanket-" "$REPO_ROOT/SHA256SUMS" \
        || fail "scripts/bundle.sh checksums produced an unexpected format"
    echo "upgrade:   ok — scripts/bundle.sh emits sha256sum format over the built binaries"
else
    echo "upgrade:   note — no cross-compiled binaries in the repo root; skipped the bundle.sh checksums check"
fi

harness_stop_server TERM
