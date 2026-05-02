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

install-playwright:
	cd tests/e2e && npm install && npx playwright install --with-deps chromium

vet:
	go vet ./... > ${VET_REPORT} 2>&1

fmt:
	go fmt $$(go list ./... | grep -v /vendor/)

# Fails if any Go file isn't gofmt-clean. Wired into CI so formatting drift
# gets caught at review time instead of piling up.
check-fmt:
	@out=$$(gofmt -l $$(find . -name '*.go' -not -path './tests/e2e/*' -not -path './vendor/*')); \
	if [ -n "$$out" ]; then \
		echo "gofmt would reformat these files (run 'make fmt'):"; \
		echo "$$out"; \
		exit 1; \
	fi

clean:
	-rm -f ${TEST_REPORT}
	-rm -f ${VET_REPORT}
	-rm -f ${BINARY}-*

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
DOCKER_RUN = docker run --rm \
	-v $(CURDIR):/src \
	-v blanket-dev-cache:/go \
	-v blanket-npm-cache:/src/tests/e2e/node_modules \
	-w /src \
	$(DOCKER_IMAGE)

docker-image:
	docker build -t $(DOCKER_IMAGE) .

docker-check-fmt: docker-image
	$(DOCKER_RUN) make check-fmt

docker-test: docker-image
	$(DOCKER_RUN) make test

docker-test-browser: docker-image
	$(DOCKER_RUN) make linux test-browser

docker-test-smoke: docker-image
	$(DOCKER_RUN) make linux test-smoke

docker-build: docker-image
	$(DOCKER_RUN) make linux darwin windows VERSION=$(VERSION)

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

.PHONY: setup linux darwin windows test test-integration test-browser test-api-e2e test-smoke install-playwright vet fmt check-fmt clean docker-image docker-check-fmt docker-test docker-test-browser docker-test-smoke docker-build docker-shell docker-clean
