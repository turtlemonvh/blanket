# Contributing to Blanket

## Development Setup

Two options — pick one.

**Native (Ubuntu / WSL2)** — installs Go, Node, and Playwright locally
(uses `sudo` for apt + `/usr/local/go`). See `scripts/setup.sh`:

```bash
make setup
```

**Docker** — reproducible toolchain image; the same image CI will run. No
local Go or Node install needed:

```bash
make docker-test           # Go unit tests in the container
make docker-test-browser   # Playwright suite
make docker-test-smoke     # binary smoke test
make docker-shell          # interactive shell, source mounted at /src
```

See `Dockerfile` for what the image carries.

## Build & Test

### Docker targets (authoritative CI path)

```
make docker-test           # Go unit tests
make docker-test-smoke     # built binary end-to-end (scripts/smoke.sh)
make docker-test-browser   # Playwright suite
make docker-shell          # interactive container for ad-hoc work
make docker-build          # cross-compile linux/darwin/windows
make docker-clean          # drop persisted Go + npm cache volumes
```

### Native targets

```
make linux                 # build for Linux
make darwin                # build for macOS
make windows               # build for Windows
make test                  # run Go unit tests
make test-smoke            # run smoke tests
make test-browser          # run Playwright tests
make fmt                   # gofmt all Go files
make check-fmt             # fail if any Go file isn't gofmt-clean
```

After bumping `go.sum` or `tests/e2e/package-lock.json`, run
`make docker-clean` so the next `make docker-*` rebuilds the named
volumes from the freshly built image layer.

## CI

`.github/workflows/ci.yml` runs on PRs and master pushes.

- **`test`** (required check): builds the image, then runs
  `docker-check-fmt`, `docker-test`, `docker-test-smoke`,
  `docker-test-browser` in sequence. Uploads Playwright HTML report
  as an artifact on failure.
- **`cross-compile`** (master pushes only): `make docker-build` —
  catches platform-only breakage without spending minutes on every PR.

Branch protection on master requires `test` green and up-to-date with
master (`strict: true`). Admins can bypass; the normal workflow is
PR → merge, not direct push.

Test adds must keep all three surfaces green. The suites overlap
intentionally: unit tests hit handlers directly, smoke exercises the
built binary over real HTTP, Playwright drives the UI.

## Release Process

1. Merge changes to `master` and ensure CI passes.
2. Tag the commit: `git tag v0.2.0 && git push origin v0.2.0`
3. The `.github/workflows/release.yml` workflow triggers on `v*` tags:
   - Builds the Docker toolchain image
   - Cross-compiles via `make docker-build VERSION=<tag>`
   - Creates a GitHub Release with auto-generated notes
   - Attaches binaries: `blanket-linux-amd64`, `blanket-darwin-amd64`,
     `blanket-windows-amd64.exe`

The `VERSION` make variable is passed through as an ldflags `-X` value,
along with `BUILD_DATE` (local time at minute precision). Tagged builds
produce version output like `blanket v0.2.0 (built 2026-05-01 11:14 PM EDT)`.

Install scripts (`scripts/install.sh`, `scripts/install.ps1`) fetch the
latest release from the GitHub API.

## Code Conventions

- **Run `go fmt` before committing.** `make check-fmt` fails CI if any
  file isn't gofmt-clean.
- **Platform-specific code uses `//go:build` tags, not runtime switches.**
  See `worker/daemon_unix.go` / `worker/daemon_windows.go` for the
  pattern: one file per platform, tagged at the top, implementing a
  shared function signature. Do NOT import unix-only syscall fields in a
  file compiled on all platforms.
- **Logging is mixed** (`log.Printf` stdlib + logrus). New code should
  prefer `log "github.com/sirupsen/logrus"` to match the dominant style.
- **IDs are `lib/objectid.ObjectId`** — a 24-char hex MongoDB-style id.
  Do not hand-roll UUIDs.
- **Error status codes on `server/` handlers** — normalize missing-id
  errors to 404 via `lib/database.ItemNotFoundError`. Don't add new
  handlers that return 500 for missing-resource cases.

## Commit Style

`[AI] <imperative short summary>` for AI-authored commits, followed by
a body explaining the *why*. Example:

```
[AI] fix windows cross-compile: split daemon attrs by platform

cmd.SysProcAttr.Setpgid is unix-only — windows' syscall.SysProcAttr
has no Setpgid field, ...
```

Match the repo's existing subject-line style; check `git log --oneline`
if in doubt.

## Single-binary Architecture

`go build` produces a single static binary with the web UI baked in.
Templates, CSS, and vendored htmx live under `server/ui_next/` and are
pulled into the binary via `//go:embed` (see `server/ui_next.go`). No
separate asset deploy, no runtime filesystem lookups.

To refresh the vendored htmx bundle:

```bash
curl -sSfL https://unpkg.com/htmx.org@1.9.12/dist/htmx.min.js \
    -o server/ui_next/static/htmx.min.js
curl -sSfL https://unpkg.com/htmx.org@1.9.12/dist/ext/sse.js \
    -o server/ui_next/static/htmx-sse.js
```

## Task Type Schema

TOML files under any directory in `tasks.typesPaths` (config). Loader
is `tasks/task_types.go`; the name is the filename stem.

```toml
tags = ["bash", "unix"]   # worker-capability match; worker must advertise all
timeout = 300             # seconds; default 3600
command = "..."           # Go text/template, .ExecEnv is the env map
executor = "bash"         # bash (default), cmd, powershell, or any -c executor

  [[environment.required]]
  name = "DEFAULT_COMMAND"
  description = "..."

  [[environment.default]]
  name = "NAME"
  value = "world"
```

`{{.VAR}}` substitutes at submit time AND `$VAR` works at exec time
(blanket sets them both). See `examples/types/*.toml` for copy-paste
starters; `testdata/types/echo_task.toml` stays minimal for smoke.
