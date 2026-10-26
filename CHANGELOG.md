# Changelog

Notable changes to blanket, by release. This file's per-tag section is
what `.github/workflows/release.yml` publishes as the GitHub Release
body (see `Extract changelog section` in that workflow) — see
[GitHub Releases](https://github.com/turtlemonvh/blanket/releases) for
the exact binaries, `SHA256SUMS`, and offline bundle attached to each
tag.

## v0.4.1

A fast follow to v0.4.0. It makes `blanket upgrade` honest and
recoverable when an upgrade fails after the new binary is already
installed, which is how the v0.3.0 → v0.4.0 upgrade fails when a
server-started worker is running. No schema change.

### Breaking / behaviour changes

- **`blanket upgrade` checks the running server, not only the installed
  binary** (#264). When the installed binary is already the target but
  the server on the port still runs an older version, `upgrade` and
  `upgrade --check` now exit **11** (installed, not restarted) instead
  of 10 ("nothing to do"). The message names both versions and the next
  step: `blanket upgrade --resume --yes` when the server has a restart
  in flight, otherwise a restart under the server's supervisor.
  `--json` gains `serverVersion`. With no server, or a dev build, the
  answer is unchanged.
- **`--no-restart` no longer suggests `--resume`** (#265). It now says
  to restart the server under its supervisor. Resume never started a
  restart that hadn't begun; it waited out its verify budget and exited
  12.

### Fixes

- **`blanket upgrade --abort` after a failed post-swap upgrade** no
  longer says "nothing was installed" (#265). It says the new binary was
  left in place and names `blanket rollback --yes`. When the aborted
  restart had already drained workers, it warns that any worker not
  back needs `PUT /worker/:id/restart`.
- **`blanket upgrade --resume --yes` after a failed post-swap upgrade**
  finishes the restart instead of refusing with "the journal records no
  staged binary" (#265). If the server no longer has a restart in flight,
  for example because the watchdog aborted it, resume refuses with exit
  13 and says to restart the server under its supervisor. It never
  starts a new restart.

### Upgrading

- **From v0.4.0:** `blanket upgrade --yes`, or `--bundle` offline. The
  server drains its workers, waits for running tasks, restarts on
  v0.4.1 and respawns the workers. `blanket rollback --yes` returns you
  to v0.4.0. Both were tested on a supervised server with a
  server-started worker, idle and mid-task.
- **From v0.3.0:** follow v0.4.0's Upgrading notes below. The v0.3.0 CLI
  does the upgrade, so its bugs apply.
- **`--no-restart` from v0.4.0** still prints v0.4.0's hint to run
  `--resume`. Restart the server under its supervisor instead.
- **Known issue: a task that outlasts the drain is killed** (#271,
  present since v0.3.0). Under the systemd unit `blanket service
  install` writes, a restart stops everything in the unit, server-started
  workers and their tasks included. If a task is still running when
  `restart.drainTimeout` (60s by default) runs out, the upgrade kills
  it, the task ends `ERROR`, and the upgrade can exit 12 even though it
  succeeded. Until #271 is fixed:
  - Upgrade when no long task is running, or raise
    `restart.drainTimeout` past your longest task, along with
    `restart.deadline`.
  - After `--no-restart` and a supervisor restart, or after an
    interrupted upgrade, check that your workers came back. If one
    didn't, run `PUT /worker/:id/restart` or restart it from the UI
    (#272).

## v0.4.0

Everything merged since `v0.3.0`. No schema change: a v0.3.0 binary can
still open a database this release has used. The next release adds
authentication and a schema migration.

### Highlights

- **MCP for agents.** `blanket_docs` searches the docs by section
  instead of returning whole pages (#230). The new `blanket_task_logs`
  tool greps and pages a task's log on the server, so an agent no longer
  pulls a whole log into its context (#231). The `new_task_type` prompt
  walks an agent through writing a task type (#232). Every tool carries
  MCP annotations (read-only or destructive), unknown task types and
  doc pages get "did you mean" suggestions, `blanket_tasks(id=...)`
  shows the task's `resultDir`, and the opt-in
  `tasks.requireExplicitTypeOverwrite` makes `blanket_write_task_type`
  refuse to replace an existing file without `overwrite=true` (#229).
  See [docs/mcp.md](docs/mcp.md).
- **Docs in the web UI.** A Docs page lists, renders and searches the
  same docs the MCP tool serves (#234).
- **Phones.** Below 640px the task, worker, upcoming and task-type
  lists become cards, with a bottom tab bar. The UI installs as a PWA.
  Every cancel and delete asks for confirmation, and a new Duration
  column shows how long a task ran. `ui.instanceName` names the
  instance in the top bar; it defaults to the hostname (#216).
- **Tasks and workers keep their records straight.** Delete refuses a
  task or worker that is still running, a claim or cancel that races a
  delete can no longer bring a task back or orphan its process, and a
  worker with no tags claims only untagged tasks. Details under
  Breaking / behaviour changes.
- **Under the hood.** Blanket's own file follower replaces
  `hpcloud/tail` for log streaming (#142). An in-tree cron describer,
  stdlib calls and a vendored copy of `tomb.v1` retire four more
  third-party dependencies. Dependabot,
  weekly `govulncheck` and `grype`, a license gate, and an SPDX SBOM on
  every release are in place (#144). CI pulls a prebuilt toolchain image
  and skips the expensive jobs on docs-only changes (#53). The docs had
  three concision passes (#242), and
  [docs/examples/tailscale.md](docs/examples/tailscale.md) covers reaching blanket over
  Tailscale (#190).

### Breaking / behaviour changes

- **`DELETE /worker/:id` checks the stored worker, not the request
  body** (#246). It takes no body. It returns 400 unless the worker is
  stopped, 404 if there is no such worker (it used to return 200), and
  200 `{"id": ...}` once deleted. Before, a body claiming
  `"stopped": true` let a running worker be deleted.
- **`DELETE /task/:id` returns 404 for a missing task** (#249). It used
  to answer 200 for a well-formed id naming no task; it now follows the
  same 400/404 rule as every other `:id` route, `DELETE /worker/:id`
  included. A script deleting tasks that may already be gone should
  treat 404 as success. `blanket rm` of a missing task now exits 1 with
  `error: 404 no such task <id>` (`--force` still ignores it).
- **`DELETE /task/:id` refuses a task that is still running** (#252). A
  `CLAIMED` or `RUNNING` task is now a 400, `Cannot delete a task that is
  still running; cancel it first`, and is left alone. The docs said
  delete killed a running task, but it never did. It removed the record,
  the worker's next poll got a 404, and the process kept running with
  nothing left to cancel it by. A force-cancelled task is also a 400 for
  the moment until its worker reports the kill. The new `stopPendingTs`
  task field tracks this, and the wait ends after 60s if no report
  arrives. `blanket rm` prints the 400. `blanket rm --force` now cancels a
  running task with force, waits for the kill, and deletes it. It no
  longer hides errors other than "no such task", and it exits 1 if the
  task survives. `blanket_cancel_task(force=true, delete=true)` waits up
  to 15s for the kill before deleting.
- **`powershell` tasks run with `-NoProfile -NonInteractive`.** A task
  no longer inherits the machine owner's PowerShell `$PROFILE`, which
  matches `bash -c` (a non-interactive shell never sourced `~/.bashrc`),
  and a prompt is now an error instead of a task that hangs until its
  timeout. A type that relied on something its profile set must set it
  in `environment` or `command`. The `windows_powershell` example's
  timeout rose from 10s to 60s to cover interpreter startup (#169).
- A worker with no tags now claims only untagged tasks, however it was
  launched. A worker claims a task only if its tags are a superset of
  the task's, but the claim filter skipped that check when a worker's
  tag set was empty, so a tagless worker could claim every task,
  including tagged ones it may not be able to run. `blanket worker`
  with no `-t` only escaped this by accident, by registering `[""]`.
  **Workers launched or registered without tags through
  `blanket_launch_worker`, `POST /worker/` or `PUT /worker/:id` stop
  claiming tagged tasks**; give them the tags those tasks need.
  `blanket_launch_worker` now accepts an empty `tags` list instead of
  rejecting it, and every launch path stores an empty tag set rather
  than `[""]`. Task listing is unchanged: an absent or empty `maxTags`
  still means no tag filter (#259).

### Fixes

- `blanket upgrade --resume` no longer trusts a journal that still says
  `PAUSED` when the binary swap had already completed before the CLI
  was killed. Resuming from that state used to copy the *new* binary
  into a rollback slot named for the *old* version, so a later `blanket
  rollback` reinstalled the version it was asked to undo; it reached CI
  as an intermittent smoke failure roughly one run in twenty. The resume
  now checks the installed binary's digest against the journal's
  `sha256` and finds the slot by `upgradeId` instead (#203).
- `blanket upgrade` no longer fails against a server with a forked
  worker. The server now reaps the workers it forks, so a drain no
  longer waits out `restart.drainTimeout` on an exited worker's zombie,
  and the CLI's drain call no longer times out after 5s whatever its
  deadline (#227).
- A worker claiming a task checks and writes the task record in one
  transaction, and so does a user's cancel. A claim racing a delete used
  to bring the deleted task back as `CLAIMED`. A cancel racing a claim
  used to leave a `CLAIMED` task `STOPPED`, so it could be deleted while
  its worker started it (#261).

### Upgrading

- **From v0.3.0:** `blanket upgrade --yes` (or `--check` first). No
  migration runs and the database is not rewritten; `stopPendingTs` is
  a new task field that older records simply lack. **Back up first
  anyway** (`blanket backup`). See [docs/upgrade.md](docs/upgrade.md).
- **If a server-started worker is running, the v0.3.0 `blanket upgrade`
  fails.** That means a worker launched from the UI, `POST /worker/` or
  MCP; workers started with `blanket worker -d`, and upgrades with no
  worker, are unaffected. After about 5s the CLI exits 13 with
  `timeout awaiting response headers`. This is the v0.3.0 CLI bug fixed
  in this release (#227). v0.4.0 is already installed at that point, but
  the old server is still running with worker spawn paused. To finish:
  - Don't run `--resume` (it refuses) or `--abort`.
  - Wait until no task is `RUNNING`. The drained worker finishes its
    current task first; restarting earlier kills that task.
  - Restart the server under its supervisor, for example `systemctl
    --user restart blanket`. v0.4.0 comes up, completes the paused
    restart and brings the worker back. `blanket rollback --yes` still
    returns you to v0.3.0.
  - If you already ran `--abort`, or left it longer than
    `restart.deadline` (5 minutes by default), restart the server the
    same way, then restart the worker from the UI or with `curl -X PUT
    localhost:8773/worker/<id>/restart`.
- **From before v0.3.0:** use `blanket upgrade --bundle
  blanket-bundle-v0.4.0.tar.gz --yes`, or replace the binary by hand;
  see v0.3.0's Upgrading notes below.
- **Mixed versions work during the swap.** A v0.3.0 worker talks to a
  v0.4.0 server unchanged.
- **Downgrading** to v0.3.0 works with `blanket rollback`, since the
  schema is unchanged. That stops being true after the next release's
  migration.

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
    have a requeue double-execute it. The first terminal state wins: a
    worker's late `ERROR` report after a user cancel is now a 200
    no-op instead of a rejection the worker retried until it gave up.
    Worker poll sleeps are jittered (full-jitter backoff on failure),
    so a fleet does not reconnect in lockstep after a server restart.
  - `PUT /worker/:id` merges at the field level, with the worker
    owning its registration fields and the server owning `stopped` and
    the respawn bookkeeping; a worker re-registering can no longer undo
    a stop that landed between its polls.
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
- **`blanket task-validate` and the tag ontology.** Coded checks
  001-009 (command, executor, template references, inputs,
  documentation, `result_file`) plus a tag lint (010-014) against a
  known-tag set built from a seed vocabulary, `.blanket/known-tags.conf`
  files and the tags already in use, with `--json`, `--strict` and
  `--dump-known-tags`. Tags are namespaced `namespace:value`, and the
  shipped example types moved to it (`bash` → `exec:bash`, `unix` →
  `os:unix`, `python` → `runtime:python3`, `windows` → `os:windows`).
  Unnamespaced tags still work. See
  [docs/tag_ontology.md](docs/tag_ontology.md).
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
- Records from before this release read as they are: tasks have no
  stored `exitCode` and return `null`; a task with no
  `blanket.combined.ndjson` falls back to the grouped (all stdout, then
  all stderr) view in the UI's "both" log pane; an outcome journal
  without `pidStartTs` reads `0`, which the reaper treats as
  inconclusive rather than dead.
- A mixed-version install works during the swap: a worker from before
  this release sends no `runId`, and the server accepts an empty token
  on `run`/`progress`/`finish` — only two tokens that are both present
  and different conflict.
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
