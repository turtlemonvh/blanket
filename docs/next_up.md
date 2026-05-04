# Next Up

Running backlog of work on blanket. Living document — add, reorder, or drop
items as priorities shift. Entries should capture enough context that a cold
reader (or a future AI session) can pick them up without conversation history.

## Fix Sooner

Small, known-scope items to clear before the next big refactor.

- **Normalize task-handler error status codes** (`server/serve_tasks.go`) —
  `updateTaskProgress` returns 500 for a missing task id; `markTaskAsFinished`
  returns 400; `claimTask` returns 500 when the worker isn't in the DB.
  `ItemNotFoundError` already exists in `lib/database` and is used in one
  branch of `claimTask` — extend that pattern so missing-id errors map to 404
  consistently. Current tests assert the existing (non-404) behavior; update
  them when this lands. (Partial: `claimTask` now returns 204 for the
  empty-queue steady state via `queue.ErrQueueEmpty` — the missing-worker
  and missing-task-id cases still need normalization.)
- **`updateTaskProgress` doesn't check task state** — a progress update on a
  terminal (SUCCESS/STOPPED) task silently succeeds. Add a state guard that
  rejects progress updates on non-RUNNING tasks, then add a regression test.

## Test Coverage Expansion

Remaining gaps — the TODO blocks at the top of each test file are the
authoritative source. Items listed here are the ones deferred as higher
effort than a normal test add.

### `worker/worker_test.go`

- **Worker SIGTERM shutdown** — needs a subprocess harness (the worker's
  `Run()` calls `os.Exit`), so can't run in-process with the other tests.
- **Goroutine-leak check** across a full run. The metrics API exposes a
  goroutine count; sample before/after and assert stable.

### `server/serve_tasks_test.go`

- **Stopping a `RUNNING` task** — `cancel` today only transitions `WAITING`
  tasks to `STOPPED`; a `RUNNING` task has no supported endpoint. The
  originally sketched `PUT /task/:id/stop` would cover this, but a single
  endpoint is fine as long as it handles both cases. If we consolidate,
  extend `cancel` to also accept `RUNNING` tasks (signals the worker via
  the STOPPED tombstone) and add an explicit opt-in parameter
  (e.g. `?force=true` or `?allowRunning=true`) so a caller can't
  accidentally kill a running task when they meant to cancel a queued one.
  Add regression tests for both paths once the handler change lands.

## Build & CI

- **GHCR push of `blanket-dev:latest`** — currently every CI run rebuilds
  the toolchain image from scratch (GHA layer cache helps, but full cache
  misses cost ~5m). Pushing the image to `ghcr.io/turtlemonvh/blanket-dev`
  from master would let future parallel jobs pull instead of rebuild, and
  would give local developers a fast path (`docker pull …` instead of
  `make docker-image`).

## Docs

- **Add mermaidjs diagrams** — current docs are text-heavy. Task and
  worker state machines now live in `docs/task_flow.md`. Still
  missing: the component diagram (server ↔ worker ↔ DB ↔ queue), the
  worker claim loop, and the `tailed_file` subscribe/backfill flow.

## Branding

- **Project branding pass** — pick a logo, color palette, and tagline for
  blanket. Surface it in the README header, the UI navbar, and the favicon.
  Consistent branding makes the project feel maintained and gives the docs
  somewhere to hang visual identity.

## UI follow-ups

The HTMX + Go-template UI is now the only UI (Phase C complete — Angular,
`gulp`, `bower`, and `server/ui_dist/` are gone). Remaining polish:

- **Worker management `FIXME`s in `server/serve_workers.go`**:
  - Make the stop-worker update atomic (currently read-modify-write)
  - Update `lastHeardTs` on stop
  - Allow a `force` option that sends signals on supported platforms
  - `deleteWorker` should validate the worker is stopped before deleting
- **Rename `server/ui_next/` → `server/ui/`** and the `uiNext*` funcs to
  drop the migration-era suffix. Pure cosmetic; safe to do any time.

## Features

- **Claude Code & Codex task wrappers** — ship example task types under
  `examples/types/` that invoke the `claude` and `codex` CLIs, so users
  can drive AI-coding agents through blanket. Pattern: a TOML type with
  required env vars for the prompt + working directory, executor =
  `bash` (or `claude` directly if installed), and tags like `["ai",
  "claude"]` so workers can opt in. Useful for: long-running refactors,
  scheduled audits, batch PR reviews, etc.
- **MCP wrapper** — expose blanket as an MCP server so AI agents can
  list/submit/inspect tasks as tools. Server lives alongside the REST
  API (likely a new `blanket mcp` subcommand or a `/mcp` route). Tools
  to surface: `submit_task`, `list_tasks`, `get_task`, `get_task_log`,
  `cancel_task`, `list_task_types`. Auth and scoping TBD.
- **AI tool instructions for authoring task types** — write a markdown
  doc that AI agents can be pointed at (via Claude `CLAUDE.md`,
  Cursor rules, or an MCP resource) to generate valid blanket task
  type TOMLs. Should cover: the schema (tags, executor, command,
  timeout, environment), the `{{.VAR}}` template language, the
  difference between submit-time substitution and exec-time `$VAR`,
  common patterns (file uploads, multi-step bash, python wrappers),
  and how to validate with `blanket task-validate`.
- **Auto-start on install** — option in the install scripts to register
  blanket as a background service, so the server starts on login/boot.
  Per-platform: systemd user unit on Linux, `launchctl` plist on macOS,
  Task Scheduler entry (or `Start-Process` shortcut) on Windows.
  Off by default; opt-in via `INSTALL_AUTOSTART=1` env var or
  interactive prompt. `blanket uninstall` (new) should remove the
  service entry.
- **Docker task type** — a built-in executor (or a shipped example
  type) that wraps `docker run` so users can specify an image, an
  optional command, env vars, and volume mounts and let blanket
  manage the container lifecycle as a task. Makes it easy to run
  anything that has a published image without authoring a TOML per
  tool. Open questions: should this be a new `executor = "docker"`
  with first-class fields (`image`, `mounts`, etc.), or just an
  example `bash` type that shells out to `docker run`? Built-in is
  cleaner UX but adds a Docker dependency to the executor surface;
  the bash-based version works today with no code changes. Probably
  start with the example, promote to a real executor if usage
  warrants.
- **Task scheduling** — submit a task with a `notBefore` timestamp, or
  with a cron-style recurrence. Today every queued task is immediately
  eligible to be claimed; a scheduler would hold tasks until their
  start time. Likely needs: a new `scheduledTs` column on Task, a
  scheduler goroutine that promotes due tasks into the queue, and a
  way to express recurrence (cron string vs. interval). Recurring tasks
  spawn child tasks at each fire time so each run has its own log/result.

## Candidate Phases

Bigger bodies of work. Pick one when Phase 1 cleanup and test expansion wrap.

- **ID type refactor** — replace `objectid.ObjectId` with local `TaskID` /
  `WorkerID` newtypes. Removes the MongoDB-era leak and lets us change the
  underlying ID scheme without API churn. Caveat: breaks wire format and
  existing `.db` files (see `lib/objectid/objectid.go` for the contract we'd
  be changing).
- **Context propagation** — no `context.Context` plumbing anywhere; blocking
  operations can't be cancelled cleanly and timeouts are ad-hoc. Medium-sized
  sweep that touches most handlers and the worker loop.
