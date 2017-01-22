# Borrowed from: 
# https://github.com/silven/go-example/blob/master/Makefile
# https://vic.demuzere.be/articles/golang-makefile-crosscompile/

BINARY = blanket
VET_REPORT = vet.report
TEST_REPORT = tests.xml
GOARCH = amd64

# Path to blanket UI: https://github.com/turtlemonvh/blanket-ui
BLANKET_UI_PATH=/Users/timothy/Projects/blanket-ui

COMMIT=$(shell git rev-parse HEAD)
BRANCH=$(shell git rev-parse --abbrev-ref HEAD)

# Symlink into GOPATH
GITHUB_USERNAME=turtlemonvh

# Setup the -ldflags option for go build here, interpolate the variable values
LDFLAGS = -ldflags "-X main.COMMIT=${COMMIT} -X main.BRANCH=${BRANCH}"

# Build the project
all: clean test-xunit vet linux darwin windows

# Setup for bindata
setup-bindata:
	go get github.com/jteeuwen/go-bindata/...
	go get github.com/elazarl/go-bindata-assetfs/...

update-bindata:
	# Change 'public' to 'dev' for un-minified code
	cd ${BLANKET_UI_PATH} && go-bindata-assetfs -pkg=server dev/...
	mv ${BLANKET_UI_PATH}/bindata_assetfs.go server

linux: 
	GOOS=linux GOARCH=${GOARCH} go build ${LDFLAGS} -o ${BINARY}-linux-${GOARCH} .

darwin:
	GOOS=darwin GOARCH=${GOARCH} go build ${LDFLAGS} -o ${BINARY}-darwin-${GOARCH} .

windows:
	GOOS=windows GOARCH=${GOARCH} go build ${LDFLAGS} -o ${BINARY}-windows-${GOARCH}.exe .

test:
	# To test just a module:
	# go test ./tasks
	go test -v ./...

test-xunit:
	# To test just a module:
	# go test ./tasks
	if ! hash go2xunit 2>/dev/null; then go install github.com/tebeka/go2xunit; fi
	go test -v ./... 2>&1 | go2xunit -output ${TEST_REPORT}

vet:
	go vet ./... > ${VET_REPORT} 2>&1

fmt:
	go fmt $$(go list ./... | grep -v /vendor/)

clean:
	-rm -f ${TEST_REPORT}
	-rm -f ${VET_REPORT}
	-rm -f ${BINARY}-*

.PHONY: linux darwin windows test test-xunit vet fmt clean update-bindata
