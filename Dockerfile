# Reproducible toolchain for building and testing blanket.
#
# Base image already carries Chromium + every system lib Playwright needs,
# which is the slowest/most fragile part of the setup (scripts/setup.sh
# spends most of its time on exactly this). We layer Go + a few CLI tools
# on top so the same image runs `make test`, `make test-browser`,
# `make test-smoke`, and the cross-compile targets.
#
# Bump PLAYWRIGHT_VERSION alongside tests/e2e/package-lock.json.
# Bump GO_VERSION alongside scripts/setup.sh (and stay >= the `go` directive
# in go.mod).

ARG PLAYWRIGHT_VERSION=v1.59.1
FROM mcr.microsoft.com/playwright:${PLAYWRIGHT_VERSION}-noble

ARG GO_VERSION=1.23.4
ARG TARGETARCH=amd64

# Extra CLI tools: make for the Makefile, git for build ldflags, curl + jq
# for scripts/smoke.sh.
RUN apt-get update \
    && apt-get install -y --no-install-recommends make git curl jq ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# The repo is bind-mounted at /src with host UIDs. Without this, git refuses
# to operate ("dubious ownership"), which breaks `make linux`'s VCS ldflags
# and anything else that shells out to git inside the container.
RUN git config --system --add safe.directory '*'

# Go toolchain (official tarball, same source scripts/setup.sh uses).
RUN curl -fsSL -o /tmp/go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
    && tar -C /usr/local -xzf /tmp/go.tar.gz \
    && rm /tmp/go.tar.gz

ENV PATH="/usr/local/go/bin:/go/bin:${PATH}"
ENV GOPATH=/go
ENV GOCACHE=/go/.cache

WORKDIR /src

# Pre-warm the Go module cache. Rebuilt only when go.mod/go.sum change, so
# cold `docker run make test` doesn't re-fetch every dependency.
COPY go.mod go.sum ./
RUN go mod download

# Pre-warm the Playwright npm deps. The base image already has the Chromium
# browser installed; `npm ci` just wires up @playwright/test in node_modules.
COPY tests/e2e/package.json tests/e2e/package-lock.json ./tests/e2e/
RUN cd tests/e2e && npm ci --no-audit --no-fund

# Source is bind-mounted at runtime (see the docker-* Makefile targets), so
# no COPY of the repo here — the image stays reusable across branches.
CMD ["bash"]
