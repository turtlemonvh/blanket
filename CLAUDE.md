# CLAUDE.md

Guidance for Claude (or any agent) working in this repo. The README is
the user-facing entrypoint; this file captures the conventions and
gotchas that save a cold session from relearning them.

## What blanket is

A single Go binary that wraps long-running command-line tasks behind a
REST API + HTMX web UI + CLI. Tasks are defined as TOML files; workers
claim them off a queue and shell out. Server and worker are the same
binary invoked with different subcommands.

## Tech stack

- **Go 1.23** (pinned in Dockerfile), `go.mod`-managed.
- **BoltDB** for storage (`lib/bolt`); internal queue abstraction at
  `lib/queue` + `lib/bolt/queue.go`.
- **Gin** for HTTP routing; `//go:embed` bakes the UI into the binary.
- **Server-rendered Go templates + htmx** under `server/ui_next/` —
  there is no SPA, no JS build step.
- **Playwright (TS)** for the browser journey suite under `tests/e2e/`.

## Where things live

- `server/` — HTTP handlers, UI rendering, embedded assets.
  Handler files are split by resource: `serve_tasks.go`, `serve_workers.go`,
  `serve_task_types.go`, `serve_config.go`, `ui_next.go`.
- `worker/` — claim loop, task exec, daemonization.
- `tasks/` — `Task` + `TaskType` types and TOML loading.
- `lib/` — `bolt/`, `database/`, `queue/`, `objectid/`, `tailed_file/`.
- `command/` — Cobra CLI subcommands (`submit`, `ps`, `rm`, `worker`).
- `examples/types/` — realistic task-type TOMLs users can copy.
- `testdata/types/echo_task.toml` — the minimal smoke-test fixture.
  Kept tiny on purpose; don't add examples here.
- `docs/NextUp.md` — the living backlog. Add/reorder items as priorities
  shift. Entries must be self-contained so a cold reader can pick them up.

## Build & test

Docker is the reproducible path — same image locally and in CI.

```
make docker-test           # Go unit tests
make docker-test-smoke     # built binary end-to-end (scripts/smoke.sh)
make docker-test-browser   # Playwright suite
make docker-shell          # interactive container for ad-hoc work
make docker-build          # cross-compile linux/darwin/windows
make docker-clean          # drop persisted Go + npm cache volumes
```

Native targets (`make test`, `make linux`, etc.) work if you ran
`make setup` first; the docker targets are the authoritative CI path.

**After bumping `go.sum` or `tests/e2e/package-lock.json`:** run
`make docker-clean` so the next `make docker-*` rebuilds the named
volumes from the freshly built image layer.

## CI

`.github/workflows/ci.yml` runs on PRs and master pushes.

- `test` (required check): builds the image, then runs `docker-check-fmt`,
  `docker-test`, `docker-test-smoke`, `docker-test-browser` in sequence.
  Uploads Playwright HTML report as an artifact on failure.
- `cross-compile` (master pushes only): `make docker-build` — catches
  platform-only breakage without spending minutes on every PR.

Branch protection on master requires `test` green and up-to-date with
master (`strict: true`). Admins can bypass; the user's normal workflow is
still PR → merge, not direct push.

**Test adds must keep all three surfaces green.** The suites overlap
intentionally: unit tests hit handlers directly, smoke exercises the
built binary over real HTTP, Playwright drives the UI.

## Code conventions

- **Run `go fmt` before committing.** `make check-fmt` fails CI if any
  file isn't gofmt-clean.
- **Platform-specific code uses `//go:build` tags, not runtime switches.**
  See `worker/daemon_unix.go` / `worker/daemon_windows.go` for the
  pattern: one file per platform, tagged at the top, implementing a
  shared function signature. Do NOT import unix-only syscall fields in a
  file compiled on all platforms.
- **Logging is mixed** (`log.Printf` stdlib + logrus). New code should
  prefer `log "github.com/sirupsen/logrus"` to match the dominant style.
  Unifying is a tracked phase candidate in NextUp.
- **IDs are `lib/objectid.ObjectId`** — a 24-char hex MongoDB-style id.
  Do not hand-roll UUIDs. The tracked refactor to `TaskID`/`WorkerID`
  newtypes is intentionally deferred (breaks wire format + on-disk state).
- **Error status codes on `server/` handlers are inconsistent** — there's
  an active NextUp item to normalize missing-id → 404 via
  `lib/database.ItemNotFoundError`. Don't add new handlers that return
  500 for missing-resource cases; use the 404 pattern.

## Task type schema

TOML files under any directory in `tasks.typesPaths` (config). Loader
is `tasks/task_types.go`; the name is the filename stem.

```
tags = ["bash", "unix"]   # worker-capability match; worker must advertise all
timeout = 300             # seconds; default 3600
command = "..."           # Go text/template, .ExecEnv is the env map
executor = "bash"         # declared but UNUSED — everything shells via bash -c

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

## Commit style

`[AI] <imperative short summary>` for AI-authored commits, followed by
a body explaining the *why* (what's already in the diff). Example:

```
[AI] fix windows cross-compile: split daemon attrs by platform

cmd.SysProcAttr.Setpgid is unix-only — windows' syscall.SysProcAttr
has no Setpgid field, ...
```

Match the repo's existing subject-line style; check `git log --oneline`
if in doubt. Don't create commits without explicit user approval.

## Gotchas

- **Bind-mount vs. image-baked dirs in docker-*.** `-v $(CURDIR):/src`
  shadows anything the Dockerfile baked under `/src`. That's why
  `tests/e2e/node_modules` is mounted as a named volume in `DOCKER_RUN`
  (Makefile) — otherwise CI runs without node_modules on disk. If you
  add another pre-warmed image path, you probably need another volume.
- **BoltDB single-writer.** Only one process can hold the `.db` file at
  a time; if startup hangs/fatals with "could not acquire lock", another
  blanket is still running. `pkill -9 -f blanket-linux-amd64` and retry.
- **Cross-compile is load-bearing.** "Single binary that drops on any
  host" is the project's main promise. Before landing platform-sensitive
  code, run `make docker-build` locally — the master-only CI job will
  otherwise catch it post-merge.
- **UI paths still carry `ui_next` naming.** There's a tracked cosmetic
  rename to `server/ui/` (and drop the `uiNext*` func prefixes). Don't
  add new `uiNext*`-prefixed names in fresh code unless you're adjacent
  to existing ones.

## Working with the user

- Keep responses tight. State results and next steps; don't narrate.
- For risky actions (merges, force-push, destructive commands), confirm
  before acting even if similar actions were approved earlier — each
  authorization is scoped, not standing.
- Prefer `make docker-*` over re-running `docker run` by hand; it keeps
  the volumes + flags consistent with CI.
- When CI fails, read the failing job's log before guessing. The
  failures are usually specific (missing file, cross-platform field,
  bind-mount shadowing), not flaky.
