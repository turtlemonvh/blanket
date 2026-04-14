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
  them when this lands.
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

- **Replace Vagrant with Docker + GitHub Actions** — drop the `Vagrantfile`
  and `/provision` scripts (neither is exercised anymore now that
  `scripts/setup.sh` handles dev setup). Add a `Dockerfile` that reproduces
  the Go + Node + Playwright toolchain, and a GitHub Actions workflow that
  runs `make test` and `make test-browser` against it on each push/PR.

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

## UI Modernization (HTMX + Go templates)

Phase A (Playwright journey harness) and Phase B (HTMX scaffold at
`/ui-next/`) are landed. Remaining:

- **Task detail page** — the Angular UI has a per-task page with log
  streaming (SSE). The scaffold doesn't cover it yet; needs a dedicated
  template plus a streaming endpoint (or reuse `/task/:id/log`).
- **Filter controls on the tasks list** — tags, states, types, date range.
  Angular's `tasks.html` has the full form; port it with HTMX form posts
  that re-render `#tasks-rows`.
- **New-worker form** — the Workers page's "New" button is inert in the
  scaffold; mirror the Angular form (tags, check interval).
- **Env var editor on the new-task form** — currently the scaffold refuses
  task types with required env vars. Add the table editor before cut over.
- **Vendor pinning / upgrade plan for htmx** — `ui_next/static/htmx.min.js`
  is vendored at 1.9.12. Document where it came from and how to refresh it
  (a one-line curl in the README is fine).
- **Phase C cut over** — once the above are done, repoint `/ui/` at the
  new UI and delete `ui/`, `server/ui_dist/`, and the legacy Angular
  bits (`gulpfile.js`, `bower.json`, `package.json`, SCSS).
- **Worker management `FIXME`s in `server/serve_workers.go`**:
  - Make the stop-worker update atomic (currently read-modify-write)
  - Update `lastHeardTs` on stop
  - Allow a `force` option that sends signals on supported platforms
  - `deleteWorker` should validate the worker is stopped before deleting

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
