#!/usr/bin/env bash
# Dependency license gate (turtlemonvh/blanket#143, following the audit in
# #131). Single source of truth for the allowlist so CI and a local run
# check the exact same policy — see CONTRIBUTORS.md's "Dependency
# licenses" section for the rationale.
#
# MPL-2.0 is deliberately allowed: it's file-level copyleft (modifications
# to MPL-licensed *files* must stay open, but linking/distributing
# alongside MIT code is unaffected), so it's compatible with distributing
# blanket under MIT.
#
#   scripts/licenses.sh          -> check + report (default)
#   scripts/licenses.sh check    -> just the allowlist gate
#   scripts/licenses.sh report   -> just the CSV report (written to stdout
#                                    and, if REPORT_OUT is set, to a file)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Pinned version (turtlemonvh/blanket#143) -- bump deliberately, not as a
# side effect of an unrelated change.
GO_LICENSES_PKG="github.com/google/go-licenses/v2@v2.0.1"

ALLOWED_LICENSES="MIT,BSD-2-Clause,BSD-3-Clause,Apache-2.0,ISC,MPL-2.0,Unlicense"

MODE="${1:-all}"
REPORT_OUT="${REPORT_OUT:-}"

# go-licenses needs the module cache populated before it can classify
# anything.
go mod download

run_check() {
    echo "go-licenses check: allowed licenses = ${ALLOWED_LICENSES}"
    go run "${GO_LICENSES_PKG}" check ./... \
        --allowed_licenses="${ALLOWED_LICENSES}"
}

run_report() {
    if [[ -n "$REPORT_OUT" ]]; then
        go run "${GO_LICENSES_PKG}" report ./... | tee "$REPORT_OUT"
    else
        go run "${GO_LICENSES_PKG}" report ./...
    fi
}

case "$MODE" in
    check)
        run_check
        ;;
    report)
        run_report
        ;;
    all)
        run_check
        run_report
        ;;
    *)
        echo "usage: $0 [check|report|all]" >&2
        exit 1
        ;;
esac
