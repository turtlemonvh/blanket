#!/usr/bin/env bash
#
# Print the content tag for the blanket-dev toolchain image, e.g.
#
#   $ scripts/toolchain-hash.sh
#   3f9c1a0e5b7d2846c1f0b9a4e8d35721
#
# This script is the single definition of that tag (turtlemonvh/blanket#53).
# Three places need it and they must agree exactly or the whole scheme is
# worse than useless -- a one-byte disagreement makes every pull miss
# silently and fall back to building, which looks like a broken registry
# rather than a broken hash:
#
#   - .github/workflows/toolchain-image.yml  (what master pushes)
#   - .github/actions/toolchain-image        (what CI jobs pull)
#   - the Makefile's docker-pull target      (what a developer pulls)
#
# The obvious alternative was GitHub Actions' hashFiles(). It was rejected
# precisely because the Makefile can't call it: reproducing hashFiles()'
# algorithm in shell is fiddly, and "CI and local agree by coincidence" is
# not a property worth depending on. Owning the algorithm makes them agree
# by construction.
#
# Caveat worth knowing: this hashes bytes on disk, so a checkout that
# rewrote line endings (git autocrlf on native Windows) produces a
# different tag than CI's. The effect is a miss and a local build, never a
# wrong image.

set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Everything the built image's contents depend on. The Dockerfile COPYs
# only go.mod/go.sum and the two e2e npm manifests; .dockerignore is in
# here because it determines the build context, and the Dockerfile itself
# because it pins GO_VERSION and PLAYWRIGHT_VERSION as ARGs.
#
# Source is bind-mounted at runtime rather than COPYed (see the Dockerfile's
# closing comment), which is exactly why this list is short and why the tag
# is stable across branches that only touch Go source.
INPUTS=(
	Dockerfile
	.dockerignore
	go.mod
	go.sum
	tests/e2e/package.json
	tests/e2e/package-lock.json
)

# macOS has shasum, not sha256sum; both emit "<hex>  <path>".
sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$@"
	else
		shasum -a 256 "$@"
	fi
}

for f in "${INPUTS[@]}"; do
	if [ ! -f "$f" ]; then
		echo "toolchain-hash: missing input '$f'" >&2
		echo "toolchain-hash: the INPUTS list in $0 is out of date with the Dockerfile" >&2
		exit 1
	fi
done

# Hash each input (path included, so a rename is a different tag), then
# hash the concatenation. Order comes from the INPUTS array, not from the
# filesystem, so it is stable everywhere.
#
# 32 hex chars, not the full 64: 128 bits is far past what a build-cache
# key needs, and short tags keep `docker pull` lines readable in a log.
for f in "${INPUTS[@]}"; do
	sha256 "$f"
done | sha256 | cut -c1-32
