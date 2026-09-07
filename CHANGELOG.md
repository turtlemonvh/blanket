# Changelog

Notable changes to blanket, by release. This file's per-tag section is
what `.github/workflows/release.yml` publishes as the GitHub Release
body (see `Extract changelog section` in that workflow) — see
[GitHub Releases](https://github.com/turtlemonvh/blanket/releases) for
the exact binaries, `SHA256SUMS`, and offline bundle attached to each
tag.

## v0.3.0

Everything merged since `v0.2.1`.

### Highlights

- **Synchronous task execution & streaming** (#27). `POST /task/?wait`
  blocks until a task finishes and returns its exit code and result
  inline; `GET /task/:id/log` and `/worker/:id/log` stream task/worker
  output as NDJSON or SSE; `blanket submit --wait` / `--follow` on the
  CLI; the MCP `blanket_run_task` tool runs a task and hands the agent
  its result in one call instead of polling.
- **Task scheduling** (#61, #94, #97, #98, #100, #102). Submit a task
  `notBefore` a given time, or on a cron-style recurrence; new
  `SCHEDULED`, `RECURRING`, and `PAUSED` task states; `POST
  /schedule/describe` turns a cron expression into plain English; an
  Upcoming page and a per-series detail view in the web UI.
- **The clean-upgrade program** (#23, six phases) — the largest body of
  work in this release:
  - Worker retry with `RunId` fencing and an outcome journal, so a
    worker that dies mid-task can't leave the task claimed forever or
    have a requeue double-execute it.
  - The server's HTTP lifecycle moved off `graceful.v1` onto the Go
    standard library, with a `server-restarting` banner in the UI.
  - Worker heartbeats, PID liveness checks, reapers, and a `LOST`
    worker state for a worker that stops heartbeating without cleanly
    unregistering.
  - A `meta` bucket in the database tracking schema version, backups,
    and in-flight migrations, plus `blanket migrate` and `blanket
    backup`.
  - A restart state machine, `/ops/restart/*` endpoints, and
    `--exec-mode` / `--drain-mode` flags for restarting a running
    server without losing in-flight work.
  - `blanket upgrade` / `blanket rollback`, verified against a
    release's `SHA256SUMS`, plus offline install/upgrade bundles.
  - See [docs/upgrade.md](docs/upgrade.md) for all of the above.
- **Web UI.** Sync-task result panes with exit code and stderr; a
  time-ordered "both" log view backed by a new worker-written
  `blanket.combined.ndjson` file; a fix for SSE streams going stale
  after the browser's back/forward cache reuses a page (#103); a
  dedicated Workers page.
- **MCP.** A `blanket_docs` tool serves these docs to MCP clients; the
  scheduling and sync-task features above have full MCP tool coverage
  alongside the REST API. See [docs/mcp.md](docs/mcp.md).
- **Issue workflow & autonomy labels.** `autonomy:` / `risk:` /
  `model:` labels on `status: ready` issues encode how much an agent
  may do unattended — open-a-PR-and-stop vs. merge-once-CI-is-green —
  plus a dedicated agent-task issue form and `.claude/night-crew.json`
  driving unattended queue processing. See CONTRIBUTORS.md's [Issue
  workflow](CONTRIBUTORS.md#issue-workflow) section, and "Notes on
  agentic development" below.
- **MIT license** (#132). blanket is now explicitly MIT-licensed
  (`LICENSE.md`).
- Malformed `:id` path parameters now return 400 instead of 500 across
  every task/worker route (#115), and client error responses decode
  into typed errors instead of opaque strings (#112).
- **Windows CI.** A `windows-latest` job builds the binary natively and
  runs a PowerShell smoke suite alongside the Linux/Docker suites.

### Breaking / behaviour changes

- **New per-task file.** The worker now writes a third file into each
  task's result directory, `blanket.combined.ndjson`, recording the
  order stdout/stderr lines actually arrived in. `blanket.stdout.log`
  and `blanket.stderr.log` are unchanged and still written exactly as
  before.
- **Task finish is slightly slower.** The worker waits up to ~400ms of
  quiet after a task's process exits before finishing the task, to
  catch trailing output for the combined log. A task producing no
  output in that window is unaffected.
- **`PUT /worker/:id/stop` takes a `reason`.** A `reason` query
  parameter is now read; an *absent* reason means "an operator stopped
  this worker" and cancels that worker's pending restart-respawn
  intent — a worker stopping itself sends its own internal reason.
- **New server exit code.** When asked to restart under a supervisor
  (`--exec-mode=exit`), the server now exits **75** (`EX_TEMPFAIL`)
  instead of 0, so a `Restart=on-failure` systemd unit (or launchd
  `KeepAlive`) brings it back rather than treating a clean restart
  request as "don't restart me." A supervisor that branches on exit
  code 0 specifically needs updating.
- **`GET /task/:id/log` replays history on connect, consistently.**
  Both the raw SSE view (`?stream=stdout|stderr`) and the structured
  stream (`?format=ndjson`, and `POST /task/?wait&stream` via the same
  path) now give every new connection its own read of up to 500 lines
  of existing output before following live, regardless of whether
  another client is already tailing the same file — previously a warm
  file could replay an arbitrary, shorter window depending on who else
  was attached (#123). `GET /worker/:id/log` is unchanged.
- **Malformed ids are 400, not 500.** Any `:id` route that fails to
  parse its id as an `objectid.ObjectId` now returns 400; only a
  well-formed id naming nothing returns 404. A client that was
  pattern-matching on 500 for a bad id needs updating.
- **`worker.SetupExecutionDirectory`'s signature changed**, from
  `(error, func())` to `(*ExecOutput, error)`, to carry the new
  combined-log plumbing. It's exported but internal to the `worker`
  package's own call sites — flagging it in case anything external
  embeds this package directly.

### Upgrading

This is the **first release with `SHA256SUMS`**. `blanket upgrade`
verifies every downloaded binary against it and has no `--no-verify`
escape hatch, so `blanket upgrade` can only target 0.3.0 and later
releases — there is no honest way to retroactively add checksums to
v0.2.1, v0.2.0, or v0.1.0.

- **Upgrading from 0.3.0 or later:** `blanket upgrade --yes` (or
  `--check` first). See [docs/upgrade.md](docs/upgrade.md).
- **Upgrading from before 0.3.0** (v0.2.1 and earlier): `blanket
  upgrade` will refuse — those releases have no `SHA256SUMS` to verify
  against. Instead, either:
  - `blanket upgrade --bundle blanket-bundle-v0.3.0.tar.gz --yes`,
    using this release's offline bundle from the [release
    page](https://github.com/turtlemonvh/blanket/releases/tag/v0.3.0);
    or
  - stop the server, replace the binary by hand, start it again — the
    install script (`scripts/install.sh` / `scripts/install.ps1`)
    works unchanged.

  See [docs/offline_install.md](docs/offline_install.md) for both
  paths.
- Either way, your existing database needs nothing done to it first:
  the `meta` bucket is created automatically on the new binary's first
  open, your database is read as **schema version 1**, and no records
  are rewritten and no migration runs. **Back up your database first
  anyway** (`blanket backup`, or copy the `.db` file with the server
  stopped) — routine caution, not because this upgrade needs it.
- See [docs/upgrade.md](docs/upgrade.md) for the full mechanics:
  rollback slots, the restart state machine, `--drain-mode`, and what
  to do if an upgrade or migration is interrupted partway.

### Notes on agentic development

Timothy asked for this section explicitly, so here it is stated
plainly: most of the code in this release was written by Claude
(Claude Code) working from GitHub issues, not typed in directly by a
human.

The workflow is described in full in CONTRIBUTORS.md's [Issue
workflow](CONTRIBUTORS.md#issue-workflow) section. In short: every open
issue carries a `status:` label, and once one reaches `status: ready`
it also carries an `autonomy:` and a `risk:` label (and optionally
`model: opus`, for changes where taste or design judgment matters more
than throughput) that turn Timothy's pre-approval into something an
agent can act on without asking again — `autonomy: pr-only` opens a PR
and stops for a human "LGTM" comment before merging; `autonomy:
ship-to-merge` (only ever paired with `risk: low`, meaning reversible
and no data/wire-format/external-account change) opens the PR and
merges it itself once CI is green, no human review required.
Higher-uncertainty work goes through `autonomy: design-first`: a
one-page decisions brief is written and published as a Word document
for inline comment review before any code is written at all.

Provenance is meant to be visible in the history, not just asserted
here. Commits Claude authors carry an `[AI] <summary>` subject line and
a body explaining *why*, not just what's already in the diff. Comments
Claude posts on GitHub issues and PRs open with a visible `**🤖
Claude**` marker, because Claude currently authenticates to GitHub as
the same `turtlemonvh` login Timothy uses — there's no separate bot
identity yet (tracked in #90) — so without the marker a thread would
read as one person arguing with themselves. PR descriptions carry a
"Generated with Claude Code" footer linking back to the session that
produced them.

Anything touching wire format, database schema, or an external account
is `risk: medium` or `risk: high` and therefore never eligible for
`ship-to-merge`; it stops at an open PR for Timothy's review regardless
of how confident the change looks. Ship-to-merge is reserved for
changes that are cheap to undo if wrong.

## v0.2.1 and earlier

No changelog existed before this file. See [GitHub
Releases](https://github.com/turtlemonvh/blanket/releases) for v0.2.1,
v0.2.0, and v0.1.0.
