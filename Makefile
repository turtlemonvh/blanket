# Borrowed from: 
# https://github.com/silven/go-example/blob/master/Makefile
# https://vic.demuzere.be/articles/golang-makefile-crosscompile/

BINARY = blanket
VET_REPORT = vet.report
TEST_REPORT = tests.xml
GOARCH = amd64

COMMIT=$(shell git rev-parse HEAD)
BRANCH=$(shell git rev-parse --abbrev-ref HEAD)

# Symlink into GOPATH
GITHUB_USERNAME=turtlemonvh

# Setup the -ldflags option for go build here, interpolate the variable values
LDFLAGS = -ldflags "-X main.COMMIT=${COMMIT} -X main.BRANCH=${BRANCH}"

# Build the project
all: clean test vet linux darwin windows

# First-time setup on a fresh Ubuntu / WSL2 box. Installs Go, nvm+Node, and
# Playwright (with system deps). Safe to re-run. Requires sudo.
setup:
	bash scripts/setup.sh

setup-ui-dev:
	cd ui; \
	npm install; \
	npm install -g bower gulp; \
	npm install --save-dev jshint gulp-jshint; \
	bower install

# Rebuild the embedded UI assets. UI source lives in ui/src; running `gulp
# build-dev` produces ui/dev/, which we sync into server/ui_dist/ — that
# directory is embedded at compile time via //go:embed in server/ui.go.
update-ui-dist:
	cd ui && gulp build-dev
	rm -rf server/ui_dist
	cp -R ui/dev server/ui_dist

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

install-playwright:
	cd tests/e2e && npm install && npx playwright install --with-deps chromium

vet:
	go vet ./... > ${VET_REPORT} 2>&1

fmt:
	go fmt $$(go list ./... | grep -v /vendor/)

clean:
	-rm -f ${TEST_REPORT}
	-rm -f ${VET_REPORT}
	-rm -f ${BINARY}-*

.PHONY: setup linux darwin windows test test-integration test-browser test-api-e2e install-playwright vet fmt clean update-ui-dist setup-ui-dev
