#!/usr/bin/env bash
# Put origin/master on the maintainer's dev server (turtlemonvh/blanket#227).
#
# Run as `make deploy-dev`. Developer tooling, not a product feature: it
# builds a bundle from origin/master -- never from whatever this checkout
# has in it -- and hands it to the *installed* blanket's own
# `blanket upgrade --bundle`, so every deploy also dogfoods the upgrade path
# (checksum verify, atomic stage + swap, database backup, rollback slot,
# drain, exit 75 for systemd, verify the replacement came back on the new
# version). A bad deploy is undone with `blanket rollback --yes`.
#
# Every path is overridable from make, so the same script can be pointed at
# a throwaway server for testing:
#
#   DEV_BLANKET  the installed binary to upgrade     ~/.local/bin/blanket
#   DEV_CONFIG   the config the server runs with     ~/.config/blanket/config.json
#   DEV_PORT     the port the server listens on      8773
#   DEV_REF      what to deploy                      origin/master
#   DEV_REMOTE   what to fetch first ("" skips)      origin
#   DEV_UPGRADE_FLAGS  extra flags for `blanket upgrade` (e.g. --drain-mode never)
#
# Exit status is the upgrade's, except that "already running this version"
# (ExitNothingToDo, 10) is success here: a re-run on an unchanged master is
# the normal case, not an error.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

DEV_BLANKET="${DEV_BLANKET:-$HOME/.local/bin/blanket}"
DEV_CONFIG="${DEV_CONFIG:-$HOME/.config/blanket/config.json}"
DEV_PORT="${DEV_PORT:-8773}"
DEV_REF="${DEV_REF:-origin/master}"
DEV_REMOTE="${DEV_REMOTE-origin}"
DEV_UPGRADE_FLAGS="${DEV_UPGRADE_FLAGS:-}"

# Kept in sync with command/upgrade_common.go's exit codes.
EXIT_NOTHING_TO_DO=10

say() { echo "deploy-dev: $*"; }

[[ -x "$DEV_BLANKET" ]] || { say "no installed blanket at $DEV_BLANKET (set DEV_BLANKET)"; exit 2; }
[[ -f "$DEV_CONFIG" ]] || { say "no config at $DEV_CONFIG (set DEV_CONFIG)"; exit 2; }

if [[ -n "$DEV_REMOTE" ]]; then
    say "fetching $DEV_REMOTE"
    git -C "$REPO_ROOT" fetch --quiet --tags "$DEV_REMOTE"
fi

TMP="$(mktemp -d "${TMPDIR:-/tmp}/blanket-deploy-dev.XXXXXX")"
WT="$TMP/src"
cleanup() {
    git -C "$REPO_ROOT" worktree remove --force "$WT" >/dev/null 2>&1 || true
    git -C "$REPO_ROOT" worktree prune >/dev/null 2>&1 || true
    rm -rf "$TMP"
}
trap cleanup EXIT

git -C "$REPO_ROOT" worktree add --quiet --detach "$WT" "$DEV_REF"

# One version string for both the binary's ldflag and the bundle manifest.
# They have to agree: `blanket upgrade` installs the manifest's version and
# then verifies that the server which comes back reports it in its banner.
# A fresh worktree is clean, so this never carries -dirty.
VERSION="$(git -C "$WT" describe --tags --always)"
say "building $DEV_REF as $VERSION"

# Only the linux binary: the bundle carries whatever was built
# (scripts/bundle.sh skips absent platforms), and the dev box needs one.
make -C "$WT" --no-print-directory linux VERSION="$VERSION" >/dev/null
DIST="$TMP/dist" make -C "$WT" --no-print-directory bundle VERSION="$VERSION"
BUNDLE="$TMP/dist/blanket-bundle-$VERSION.tar.gz"

say "upgrading $DEV_BLANKET (port $DEV_PORT, config $DEV_CONFIG)"
rc=0
# shellcheck disable=SC2086 # DEV_UPGRADE_FLAGS is a word list on purpose
"$DEV_BLANKET" upgrade --bundle "$BUNDLE" --yes \
    --config "$DEV_CONFIG" --port "$DEV_PORT" $DEV_UPGRADE_FLAGS || rc=$?

case "$rc" in
    0) ;;
    "$EXIT_NOTHING_TO_DO") say "already on $VERSION; nothing to do" ;;
    *)
        say "blanket upgrade exited $rc; see docs/upgrade.md (Exit codes). Undo with: $DEV_BLANKET rollback --yes --config $DEV_CONFIG --port $DEV_PORT"
        exit "$rc"
        ;;
esac

say "now serving: $(curl -fsS "http://localhost:$DEV_PORT/version" || echo '(no answer)')"
