# Borrowed from: 
# https://github.com/silven/go-example/blob/master/Makefile
# https://vic.demuzere.be/articles/golang-makefile-crosscompile/

BINARY = blanket
VET_REPORT = vet.report
TEST_REPORT = tests.xml
GOARCH = amd64

COMMIT=$(shell git rev-parse HEAD)
BRANCH=$(shell git rev-parse --abbrev-ref HEAD)
VERSION ?=
BUILD_DATE = $(shell date +"%Y-%m-%d %-I:%M %p %Z")

# Symlink into GOPATH
GITHUB_USERNAME=turtlemonvh

# Setup the -ldflags option for go build here, interpolate the variable values
LDFLAGS = -ldflags "-X main.COMMIT=${COMMIT} -X main.BRANCH=${BRANCH} -X 'main.VERSION=${VERSION}' -X 'main.BUILD_DATE=${BUILD_DATE}'"

# Build the project
all: clean test vet linux darwin windows

# First-time setup on a fresh Ubuntu / WSL2 box. Installs Go, nvm+Node, and
# Playwright (with system deps). Safe to re-run. Requires sudo.
setup:
	bash scripts/setup.sh

linux:
	GOOS=linux GOARCH=${GOARCH} go build ${LDFLAGS} -o ${BINARY}-linux-${GOARCH} .

darwin:
	GOOS=darwin GOARCH=${GOARCH} go build ${LDFLAGS} -o ${BINARY}-darwin-${GOARCH} .

windows:
	GOOS=windows GOARCH=${GOARCH} go build ${LDFLAGS} -o ${BINARY}-windows-${GOARCH}.exe .

test:
	go test -v -count=1 ./...

# Race detector over the packages where goroutines actually interact: the
# worker's monitor/claim goroutines, the server's handlers, and the bolt
# transactions underneath them. Requires cgo and a C toolchain — both are in
# the docker image (see Dockerfile), which is why `make docker-test-race` is
# the supported way to run this.
#
# lib/tailed_file is in the list now that the race it used to report (a log
# statement reflecting over hpcloud/tail's internals) is fixed — see the
# comment in StartTailedFile.
test-race:
	CGO_ENABLED=1 go test -race -count=1 ./worker/... ./server/... ./lib/bolt/... ./lib/httpx/... ./lib/timing/... ./lib/tailed_file/... ./tasks/...

# Integration tests spin up a real server + worker; skip with -short
test-integration:
	go test -v -count=1 -run TestProcessOne ./worker/...

# E2E tests require Node.js + Playwright.
# Run `make install-playwright` once before running this target.
# Requires a built blanket binary in the repo root (make linux/darwin first).
# Set SKIP_BROWSER_TESTS=1 to run only API-level tests when Chromium system
# dependencies (libnspr4 etc.) are not installed.
test-browser:
	cd tests/e2e && npx playwright test

test-api-e2e:
	cd tests/e2e && SKIP_BROWSER_TESTS=1 npx playwright test

# End-to-end smoke test for the built binary: starts the server on a throwaway
# port + tempdir, exercises core endpoints, tears down. Run `make linux`
# (or darwin/windows) first so scripts/smoke.sh has a binary to exec.
test-smoke:
	bash scripts/smoke.sh

# Shutdown / restart tests for the built binary (turtlemonvh/blanket#23
# phase 2): SIGTERM drains with an SSE client attached and exits 0, SIGINT
# stops without resurrecting anything, SIGUSR2 re-execs in place keeping the
# PID. None of these are expressible in-process — see the header of
# scripts/restart.sh. Shares scripts/lib/harness.sh with test-smoke, and
# needs a built binary the same way.
test-restart:
	bash scripts/restart.sh

# Schema / backup / migrate tests for the built binary
# (turtlemonvh/blanket#23 phase 4). Cross-process by nature: BoltDB's
# exclusive flock is what makes "back up via the server while it's running"
# and "restore only with it stopped" mean anything, and a single `go test`
# process can't observe either. Shares scripts/lib/harness.sh with
# test-smoke and test-restart.
test-migrate:
	bash scripts/migrate.sh

# Restart state-machine tests for the built binary (turtlemonvh/blanket#23
# phase 5). Its claim is not "each transition works" -- that is a Go test --
# but "a machine that loses power between any two transitions comes back to
# a documented place", which needs a real process killed at a point no
# wall-clock `kill -9` could reliably hit. The binary carries a
# crash-injection hook (BLANKET_TEST_CRASH_AT) and this parametrizes over
# every state with it. Shares scripts/lib/harness.sh with the other three.
test-restart-machine:
	bash scripts/restart_machine.sh

# Dependency license gate (turtlemonvh/blanket#143, following the audit in
# #131): `go-licenses check` against an explicit allowlist, plus a CSV
# `go-licenses report` of every dependency's detected license. The
# allowlist itself lives in scripts/licenses.sh, not here, so there's one
# place to read or update the policy -- see CONTRIBUTORS.md's "Dependency
# licenses" section.
licenses:
	bash scripts/licenses.sh

# SHA256SUMS over the cross-compiled binaries, and the offline bundle
# (turtlemonvh/blanket#23 phase 6). The release workflow runs both after
# `make docker-build`; run them locally the same way, because a release
# artifact that only CI can produce is one nobody can check before tagging.
#
# `make checksums` needs the three binaries in the repo root, so:
#     make linux darwin windows checksums bundle VERSION=v0.5.0
checksums:
	bash scripts/bundle.sh checksums

bundle:
	VERSION=$(VERSION) bash scripts/bundle.sh bundle

# Upgrade / rollback tests for the built binary (turtlemonvh/blanket#23
# phase 6). Cross-process by nature and then some: the claim is "the
# binary on disk was replaced and a *different* process came back running
# it", which no in-process test can even state. It builds blanket twice
# with different VERSION ldflags, serves them from a fake releases API and
# from a bundle, and drives the real command. Shares scripts/lib/harness.sh
# with the other four.
test-upgrade:
	bash scripts/upgrade.sh

install-playwright:
	cd tests/e2e && npm install && npx playwright install --with-deps chromium

vet:
	go vet ./... > ${VET_REPORT} 2>&1

fmt:
	go fmt $$(go list ./... | grep -v /vendor/)

# Fails if any Go file isn't gofmt-clean. Wired into CI so formatting drift
# gets caught at review time instead of piling up.
#
# Resolve gofmt via `go env GOROOT` rather than a bare `gofmt` on PATH:
# `go` re-execs into the toolchain pinned by go.mod's `toolchain` directive
# (GOTOOLCHAIN=auto, the default) even when the ambient system go has
# drifted, so this stays aligned with the pinned version without that step.
check-fmt:
	@gofmt="$$(go env GOROOT)/bin/gofmt"; \
	out=$$($$gofmt -l $$(find . -name '*.go' -not -path './tests/e2e/*' -not -path './vendor/*')); \
	if [ -n "$$out" ]; then \
		echo "gofmt would reformat these files (run 'make fmt'):"; \
		echo "$$out"; \
		exit 1; \
	fi

clean:
	-rm -f ${TEST_REPORT}
	-rm -f ${VET_REPORT}
	-rm -f ${BINARY}-*
	-rm -f SHA256SUMS
	-rm -rf dist

# ---------------------------------------------------------------------------
# Docker — reproducible toolchain image. Same image CI will run.
# ---------------------------------------------------------------------------

DOCKER_IMAGE ?= blanket-dev:latest

# Base run command:
#   -v $(CURDIR):/src                        — mount the checkout
#   -v blanket-dev-cache:/go                 — persist Go module + build cache
#   -v blanket-npm-cache:…/node_modules      — persist Playwright deps
#
# The node_modules volume is load-bearing: the Dockerfile `npm ci`s at build
# time, but the bind mount above would otherwise shadow that pre-warmed
# node_modules with the host's (absent on a fresh CI checkout). Docker
# populates a named volume from the image layer on first use, so subsequent
# runs reuse it. If you bump tests/e2e/package-lock.json, run
# `make docker-clean` to drop the stale volume.
#
# GO_BUILD_CACHE — optional host directory for the Go *build* cache
# (turtlemonvh/blanket#155). Unset (the default) it stays in the
# blanket-dev-cache volume, which is what you want locally: it persists
# between runs on the same machine with nothing to configure.
#
# CI is the case it exists for. Each fanned-out job in ci.yml gets its own
# runner, so the named volume is always cold and every job recompiles the
# same packages — the cost that fan-out otherwise pays for parallelism. Point
# this at a directory `actions/cache` restores and saves, and the compile is
# shared across jobs and across runs instead.
#
# Only the *build* cache moves. The module cache stays in the named volume
# because Docker populates that from the image's `go mod download` layer on
# first use — bind-mounting over /go would shadow the pre-warm and make a
# cold run slower, exactly the trap the node_modules volume above exists to
# avoid.
GO_BUILD_CACHE ?=
ifneq ($(GO_BUILD_CACHE),)
GO_BUILD_CACHE_MOUNT = -v $(abspath $(GO_BUILD_CACHE)):/gocache -e GOCACHE=/gocache
endif

DOCKER_RUN = docker run --rm \
	-v $(CURDIR):/src \
	-v blanket-dev-cache:/go \
	-v blanket-npm-cache:/src/tests/e2e/node_modules \
	$(GO_BUILD_CACHE_MOUNT) \
	-w /src \
	$(DOCKER_IMAGE)

docker-image:
	docker build -t $(DOCKER_IMAGE) .

docker-check-fmt: docker-image
	$(DOCKER_RUN) make check-fmt

docker-test: docker-image
	$(DOCKER_RUN) make test

docker-test-race: docker-image
	$(DOCKER_RUN) make test-race

docker-test-browser: docker-image
	$(DOCKER_RUN) make linux test-browser

docker-test-smoke: docker-image
	$(DOCKER_RUN) make linux test-smoke test-restart test-migrate test-restart-machine test-upgrade

docker-build: docker-image
	$(DOCKER_RUN) make linux darwin windows VERSION=$(VERSION)

# Same license gate as `make licenses`, run inside the toolchain image --
# useful if your host Go install doesn't match go.mod's pinned toolchain.
# CI itself doesn't use this: the licenses.yml workflow runs `make
# licenses` directly on a `setup-go`-provisioned runner rather than paying
# for a full image build on a job that fires rarely (path-gated to
# go.mod/go.sum changes).
docker-licenses: docker-image
	$(DOCKER_RUN) make licenses

# Everything a release attaches: the three binaries, SHA256SUMS over them,
# and the offline bundle. Same image CI uses, so `make docker-release
# VERSION=v0.5.0` locally produces byte-identical checksums to the tag.
docker-release: docker-image
	$(DOCKER_RUN) make linux darwin windows checksums bundle VERSION=$(VERSION)

# Interactive shell in the toolchain image for ad-hoc work.
docker-shell: docker-image
	docker run --rm -it \
		-v $(CURDIR):/src \
		-v blanket-dev-cache:/go \
		-v blanket-npm-cache:/src/tests/e2e/node_modules \
		-w /src \
		$(DOCKER_IMAGE) bash

# Drop the persisted Go + npm caches. Run this after bumping go.sum or
# tests/e2e/package-lock.json so the next docker-* run repopulates from the
# freshly built image.
docker-clean:
	-docker volume rm blanket-dev-cache blanket-npm-cache

.PHONY: setup linux darwin windows test test-race test-integration test-browser test-api-e2e test-smoke test-restart test-migrate test-restart-machine test-upgrade checksums bundle install-playwright vet fmt check-fmt clean licenses docker-image docker-check-fmt docker-test docker-test-race docker-test-browser docker-test-smoke docker-build docker-release docker-shell docker-clean docker-licenses
