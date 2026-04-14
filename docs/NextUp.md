# Next Up

Running backlog of work on blanket. Living document — add, reorder, or drop
items as priorities shift. Entries should capture enough context that a cold
reader (or a future AI session) can pick them up without conversation history.

## Fix Sooner

Small, known-scope items to clear before the next big refactor.

- **Go version mismatch in `scripts/setup.sh`** — pins `GO_VERSION=1.22.4`, but
  `go.mod` now requires `go 1.23` (bumped by bbolt). Fresh `make setup` will
  install a Go that can't build the project. Fix: bump the default pin to a
  1.23.x release.
- **`TestStreamLogSingleSub` flaky** (`lib/tailed_file/tailed_file_test.go`) —
  ~1 in 5 runs fail. Test's own comment: `// FIXME: Kind of brittle, but gives
  file tailer time to flush`. Root cause is a fixed 1-second sleep before
  closing the tail. Fix: replace with a deterministic "all lines received"
  signal or a poll-until-n-lines loop.

## Test Coverage Expansion

Captured at the top of the relevant test files as TODO blocks. Implement and
delete the TODO line as each one lands.

### `worker/worker_test.go`

- Two-task happy path (currently `TestProcessOne` covers one-task case)
- Task timeout (task 1 exceeds timeout, gets killed; task 2 still succeeds)
- Worker SIGTERM shutdown (stops cleanly before task 2 runs)
- Stopped-task state (api-stop mid-flight → `STOPPED`; task 2 still succeeds)
- Log production (both worker log and per-task stdout/stderr are written)
- Goroutine-leak check across a run (use metrics API)
- Time acceleration via `TimeMultiplier` for the above

### `server/serve_tasks_test.go`

- `POST /task/` multipart form with file uploads (files land at task work dir root)
- `GET /task/` with the full filter flag set (not just state — also type,
  tags, created-before/after, limit, offset, sort)
- `PUT /task/:id/stop` (distinct from cancel: applies to `RUNNING`, signals worker)
- Cancel-then-still-try-to-run: worker observes the tombstone and stops cleanly
- `PUT /task/:id/finish`: valid transition, missing task → 404, wrong-state rejected
- `PUT /task/:id/progress`: missing task → 404, wrong-state rejected
- `POST /task/claim/:workerid`: missing worker → 4xx; no matching task → appropriate status

## Deferred (Non-Urgent)

- **UI stack modernization** — AngularJS 1.6 + bower + gulp are all deprecated.
  Not blocking anything until we touch the UI. Bigger project: likely a
  rewrite against a current framework, plus regenerating `server/ui_dist/`.
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
