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
- **Worker claim loop spins without delay (UI-launched workers)** — observed:
  a worker started via the "Launch Worker" button on the Workers page polls
  the queue with no measurable backoff (visible as a flood of
  `POST /task/claim/...` log lines). CLI-launched workers (`./blanket worker
  -t ...`) appear fine. Likely culprit: `worker/worker.go:467` computes the
  loop timeout as `c.CheckInterval * 1000 * viper.GetFloat64("timeMultiplier")`
  ms — if `timeMultiplier` resolves to `0` in the daemonized child (the UI
  launches with `Daemon: true`, which re-execs blanket via `os.Exec`), the
  timer fires immediately and the loop hot-spins. `command/root.go:59` sets
  the default to the *string* `"1.0"`; verify that the re-execed child
  actually inherits that default and that `GetFloat64` returns 1.0 (not 0).
  Reproduce by launching from the UI and tailing the server log; fix the
  timeMultiplier propagation, and add a regression test that asserts the
  daemon child sees a non-zero CheckIntervalMs.

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

- **Tagged-release automation in CI** — `.github/workflows/ci.yml` already
  runs Go tests, smoke, Playwright, and cross-compile (the cross-compile
  job runs on `push: master` only). Next step is a tag-triggered workflow
  (`on: push: tags: ['v*']`) that runs `make docker-build` and attaches
  the three binaries to a GitHub Release via
  `softprops/action-gh-release@v2`. "Single binary that drops on any host"
  is a load-bearing promise of the project — release tags should publish
  it automatically.
- **Branch protection on master** — once the first CI run is green, wire
  the `test` check as required via `gh api -X PUT
  repos/turtlemonvh/blanket/branches/master/protection`. See the CI plan
  for the exact payload.
- **GHCR push of `blanket-dev:latest`** — currently every CI run rebuilds
  the toolchain image from scratch (GHA layer cache helps, but full cache
  misses cost ~5m). Pushing the image to `ghcr.io/turtlemonvh/blanket-dev`
  from master would let future parallel jobs pull instead of rebuild, and
  would give local developers a fast path (`docker pull …` instead of
  `make docker-image`).

## Docs

- **Add mermaidjs diagrams** — current docs are text-heavy. Cover key
  components (server ↔ worker ↔ DB ↔ queue), the task lifecycle state
  machine (`WAITING → CLAIMED → RUNNING → SUCCESS/ERROR/STOPPED/TIMEDOUT`),
  the worker claim loop, and the `tailed_file` subscribe/backfill flow.

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
- **Auto-refresh the workers + tasks list pages** — after submitting a
  task or launching a worker, the new row doesn't appear until the user
  manually reloads. The worker already polls; the UI should too. HTMX
  has a built-in pattern: add `hx-trigger="every 2s"` (or load + sse
  for push) to the `#workers-rows` and `#tasks-rows` partials so the
  list re-fetches itself. Keep the interval generous (≥2s) to avoid
  hammering the server when many tabs are open. Templates to touch:
  `server/ui_next/templates/{workers,tasks}.html` (and possibly the
  `_rows` partials they swap into).
- **No way to view worker logs in the UI** — the server already exposes
  `GET /worker/:id/logs` (`server/serve_workers.go:205`, `server.go:150`),
  so the file is reachable, but there's no template that surfaces it.
  Add a worker-detail page (mirror of `task_detail.html` with its SSE
  log stream — see `serve_logs.go` for the streaming endpoint pattern)
  and link to it from the Workers list. Useful for debugging the worker
  polling bug above without having to ssh into the host.
- **Rename `server/ui_next/` → `server/ui/`** and the `uiNext*` funcs to
  drop the migration-era suffix. Pure cosmetic; safe to do any time.

## Candidate Phases

Bigger bodies of work. Pick one when Phase 1 cleanup and test expansion wrap.

- **Logging hygiene** — codebase mixes `log.Printf` (stdlib) and
  `log "github.com/sirupsen/logrus"`. Unify on one logger (likely logrus or
  move to `log/slog` now that we're on Go 1.23). Low risk, good readability win.
- **ID type refactor** — replace `objectid.ObjectId` with local `TaskID` /
  `WorkerID` newtypes. Removes the MongoDB-era leak and lets us change the
  underlying ID scheme without API churn. Caveat: breaks wire format and
  existing `.db` files (see `lib/objectid/objectid.go` for the contract we'd
  be changing).
- **Context propagation** — no `context.Context` plumbing anywhere; blocking
  operations can't be cancelled cleanly and timeouts are ad-hoc. Medium-sized
  sweep that touches most handlers and the worker loop.
- **UI modernization** — listed above under Deferred; also belongs here as a
  phase candidate if we want to prioritize it.
