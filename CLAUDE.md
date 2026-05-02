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
See [CONTRIBUTORS.md](CONTRIBUTORS.md) for the full target list, CI
details, and release process.

```
make docker-test           # Go unit tests
make docker-test-smoke     # built binary end-to-end (scripts/smoke.sh)
make docker-test-browser   # Playwright suite
make docker-build          # cross-compile linux/darwin/windows
```

Test adds must keep all three surfaces green (unit, smoke, Playwright).

## Code conventions

See [CONTRIBUTORS.md](CONTRIBUTORS.md) for the full list. Key points
for AI sessions:

- **Run `go fmt` before committing.** `make check-fmt` fails CI.
- **Platform-specific code uses `//go:build` tags, not runtime switches.**
- **Logging:** prefer `log "github.com/sirupsen/logrus"`.
- **IDs are `lib/objectid.ObjectId`** — don't hand-roll UUIDs.
- **Error status codes:** missing-id → 404 via `ItemNotFoundError`.

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
