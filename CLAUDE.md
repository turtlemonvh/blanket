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

- **Go 1.25** (pinned in Dockerfile), `go.mod`-managed. The exact patch
  version is also pinned via `go.mod`'s `toolchain` directive, so `go
  build`/`go test` auto-download the right toolchain even if the
  ambient system `go` has drifted (see Gotchas below).
- **BoltDB** for storage (`lib/bolt`); internal queue abstraction at
  `lib/queue` + `lib/bolt/queue.go`.
- **Gin** for HTTP routing; `//go:embed` bakes the UI into the binary.
- **Server-rendered Go templates + htmx** under `server/ui/` —
  there is no SPA, no JS build step.
- **Playwright (TS)** for the browser journey suite under `tests/e2e/`.

## Where things live

- `server/` — HTTP handlers, UI rendering, embedded assets.
  Handler files are split by resource: `serve_tasks.go`, `serve_workers.go`,
  `serve_task_types.go`, `serve_config.go`, `ui.go`.
- `worker/` — claim loop, task exec, daemonization.
- `tasks/` — `Task` + `TaskType` types and TOML loading.
- `lib/` — `bolt/`, `database/`, `queue/`, `objectid/`, `tailed_file/`.
- `command/` — Cobra CLI subcommands (`submit`, `ps`, `rm`, `worker`).
- `examples/types/` — realistic task-type TOMLs users can copy.
- `testdata/types/echo_task.toml` — the minimal smoke-test fixture.
  Kept tiny on purpose; don't add examples here.
- `docs/` — user and maintainer docs. Filenames are `snake_case.md`.
  Index is `docs/README.md`. Planned/backlog work lives as GitHub issues
  with `status:` labels, not a docs file — there's no `docs/next_up.md`
  anymore (see #43). See "Issue workflow" below.
  - `docs/api.md` — REST endpoint reference. **Keep in sync with
    `server/server.go`** — when you add, remove, or change a route,
    update `docs/api.md` in the same PR. Easy to drift; easy to catch
    in review if the doc lives next to the code change.
  - `docs/task_flow.md` — task + worker state machines and basic flow
    narrative. Update when state transitions change.

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

Delete a branch (local and remote) once its PR is merged — don't leave
merged branches sitting around. (Older merged branches predate this
convention and were left as-is; this applies going forward.)

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
- **Three Go version pins must stay in sync:** `go.mod`'s `toolchain`
  directive, the Dockerfile's `ARG GO_VERSION`, and `scripts/setup.sh`'s
  `GO_VERSION`. They drifted once (system Go upgraded to 1.27 mid-session
  while all three were still pinned to 1.25.9, and a bare `gofmt` on
  `PATH` reformatted differently than CI expected — broke #73 and #75).
  `go.mod`'s `toolchain` directive is the load-bearing fix: with
  `GOTOOLCHAIN=auto` (the Go default), any `go` subcommand — `go build`,
  `go test`, `go env`, etc. — re-execs into the pinned toolchain even
  when the ambient system `go` has drifted. That's also why `make
  check-fmt` resolves gofmt via `` $(go env GOROOT)/bin/gofmt `` instead
  of a bare `gofmt` on `PATH` — a raw `gofmt` binary isn't a `go`
  subcommand, so it doesn't get toolchain-switched on its own. When
  bumping the Go version, update all three pins in the same PR.

## Issue workflow

Every open issue carries exactly one `status:` label
(`needs-triage`/`needs-design`/`needs-info`/`in-review`/`ready`/
`in-progress`), and the assignee says whose turn it is — assigned to
`turtlemonvh` means the human acts next, unassigned means Claude may.
Every label change is justified with a comment; Claude's carry a
`<!-- blanket-label-note -->` marker. Full rules, the state machine, and
the merge workflow live in [CONTRIBUTORS.md](CONTRIBUTORS.md#issue-workflow).
Use the `blanket-issue-triage`, `blanket-issue-audit`, and
`blanket-ready-batch` skills rather than applying labels ad hoc.

## Working unattended

Every `status: ready` issue carries `autonomy:` and `risk:` labels; read
them before starting (see CONTRIBUTORS.md "Autonomy, risk, and model
labels"). Rules for running with nobody watching — never stop while
ready issues remain, batch questions once with proposed answers, treat
pre-approval as approval, leave a morning summary, clean up worktrees and
monitors before idling — are injected at session start by the `core`
plugin from `.claude/night-crew.json`. Do not duplicate them here.

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
