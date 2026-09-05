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
make docker-test-race      # Go unit tests under the race detector
make docker-test-browser   # Playwright suite
make docker-test-smoke     # binary smoke test
make docker-shell          # interactive shell, source mounted at /src
```

See `Dockerfile` for what the image carries.

## Build & Test

### Docker targets (authoritative CI path)

```
make docker-test           # Go unit tests
make docker-test-race      # Go unit tests under -race (worker/server/bolt/…)
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
make test-race             # run Go unit tests under -race (needs cgo + gcc)
make test-smoke            # run smoke tests
make test-browser          # run Playwright tests
make fmt                   # gofmt all Go files
make check-fmt             # fail if any Go file isn't gofmt-clean
```

After bumping `go.sum` or `tests/e2e/package-lock.json`, run
`make docker-clean` so the next `make docker-*` rebuilds the named
volumes from the freshly built image layer.

### Windows

There's no Docker path on Windows (see the `windows` CI job below), so
verify Windows-specific changes natively:

```powershell
go build -o blanket-windows-amd64.exe .
go test ./worker/... ./command/... ./lib/service/...
pwsh scripts/smoke.ps1 -Binary .\blanket-windows-amd64.exe
```

`scripts/smoke.ps1` starts the built binary against a scratch config,
submits a task through each native Windows executor example
(`examples/types/windows_echo.toml` for `cmd`,
`examples/types/windows_powershell.toml` for `powershell`), runs a real
`blanket worker` to drain them, and asserts both reach `SUCCESS` — the
Windows counterpart to `scripts/smoke.sh`.

## CI

`.github/workflows/ci.yml` runs on PRs and master pushes.

- **`test`** (required check): builds the image, then runs
  `docker-check-fmt`, `docker-test`, `docker-test-smoke`,
  `docker-test-browser` in sequence. Uploads Playwright HTML report
  as an artifact on failure.
- **`windows`**: runs natively on `windows-latest` — no Docker (Docker
  and Playwright are out of scope on Windows; see the issue #79
  discussion). Builds the binary with `go build` (Go version pinned via
  `actions/setup-go`'s `go-version-file: go.mod`, so it tracks the same
  toolchain pin everything else uses — see CLAUDE.md's "three Go version
  pins" gotcha), runs `go test` for the packages with Windows-specific
  code (`./worker/...`, `./command/...`, `./lib/service/...`), then a
  PowerShell smoke pass (`scripts/smoke.ps1` — see below) and two runs of
  `scripts/install.ps1` against the just-built binary, asserting its
  `$PROFILE` shell-integration block is written idempotently. It does
  **not** run the Docker-based Go test suite, `docker-test-smoke`, or the
  Playwright browser suite — those stay Linux-only. Follow-up: once the
  task-scheduling work (#94) lands, its unit tests should run here too
  (currently everything scheduling-related is Linux-only).
- **`cross-compile`** (master pushes only): `make docker-build` —
  catches platform-only breakage without spending minutes on every PR.

Branch protection on master requires `test` green and up-to-date with
master (`strict: true`); `windows` is not (yet) a required check — a red
`windows` job is informational, not blocking. Admins can bypass branch
protection; the normal workflow is PR → merge, not direct push.

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
latest release from the GitHub API by default. They also accept
`BINARY_PATH`, `TYPES_SRC`, and `SKILLS_SRC` env vars to install from
local files instead, for offline/local-user-only environments, and
`INSTALL_SKILLS=1|0` to decide up front whether to install the
`blanket-task-type` Claude Code skill (offered interactively when a
supported agent harness is detected) — see
[`docs/offline_install.md`](docs/offline_install.md).

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
Templates, CSS, and vendored htmx live under `server/ui/` and are
pulled into the binary via `//go:embed` (see `server/ui.go`). No
separate asset deploy, no runtime filesystem lookups.

The `docs/*.md` pages served by the `blanket_docs` MCP tool are embedded
the same way, but from `main.go` rather than `server/`: `go:embed` can
only reach files inside (or below) its own package's directory, and
`docs/` needed to stay markdown-only (#66), so the root package — the
only package that sits next to `docs/` — is the one place that can
`//go:embed docs/*.md`. `main.go` hands that `fs.FS` to `lib/docs` via
`docs.SetFS`, which owns the page-key map and lookup logic; `go test
./server/...` doesn't run `main`, so `server`'s tests seed it themselves
in a `TestMain` (`os.DirFS("../docs")`).

To refresh the vendored htmx bundle:

```bash
curl -sSfL https://unpkg.com/htmx.org@1.9.12/dist/htmx.min.js \
    -o server/ui/static/htmx.min.js
curl -sSfL https://unpkg.com/htmx.org@1.9.12/dist/ext/sse.js \
    -o server/ui/static/htmx-sse.js
```

### SSE and the back/forward cache

`server/ui/static/sse-lifecycle.js` is **not** vendored — it's ours, and
it is load-bearing. Don't delete it.

Every UI page opens at least one SSE stream (`/ui/sse/tasks`,
`/ui/sse/workers`, `/task/:id/log`, `/worker/:id/log`). Browsers keep a
navigated-away page alive in the back/forward cache **with its
`EventSource` connections still open**, so after a handful of tab
switches the cached pages hold enough live sockets to exhaust Chrome's
six-connections-per-host HTTP/1.1 limit; the page you just loaded then
sits with its own stream and its htmx partial fetches `(pending)` until
the browser evicts something. It looks exactly like a hung server (#103).

The script closes each `[sse-connect]` element's stream on `pagehide`
and reopens it on a `pageshow` with `event.persisted`, by firing
`htmx:beforeCleanupElement` / `htmx:afterProcessNode` through
`htmx.trigger` — the two events htmx's SSE extension already listens
for, so the vendored `htmx-sse.js` stays untouched. It loads from
`_layout.html` immediately after `htmx-sse.js`; both halves are asserted
by `TestUI_Layout_LoadsSSELifecycleScript`.

The server side of the same problem lives in `sseStream`
(`server/ui.go`): it selects on `c.Request.Context().Done()` so a
dropped client is noticed at once. gin's `c.Stream` only checks its
client-gone channel *between* steps, so without that the handler would
block in its keepalive wait for up to 30s after the browser hung up,
holding a `CLOSE-WAIT` socket and a goroutine.

Playwright can't reproduce the *hang* itself: it launches Chromium with
`--disable-back-forward-cache`, and even with that switch dropped Chrome
won't bfcache a page that has a CDP client attached. What
`tests/e2e/specs/sse_bfcache.spec.ts` does instead is dispatch the
`pagehide`/`pageshow` events by hand and assert the stream actually ends
and is actually reopened — the contract the script exists to satisfy —
plus a navigation-cycle smoke check. Verify the end-to-end symptom by
hand in a real Chrome: switch tabs several times with DevTools' Network
panel open and confirm nothing stays `(pending)`, and that `ss -tn |
grep :8773` doesn't grow.

## Issue Workflow

GitHub issues + `status:` labels are authoritative for what's actionable.
`docs/next_up.md` is narrative-only, not a source of truth.

### Labels

Every open issue carries **exactly one** `status:` label. Closed issues
keep whatever label they had for history.

| Label | Meaning | Typical next action |
|---|---|---|
| `status: needs-triage` | Disposition unclear. | Read it, move it on. |
| `status: needs-design` | Enough uncertainty to need a full design pass. | Brainstorm, then a Word doc. |
| `status: needs-info` | Executable in principle; light open questions block it. | Whoever is assigned answers. |
| `status: in-review` | Something is awaiting review. | Reviewer named by the assignee. |
| `status: ready` | Clear criteria, no input needed. | Claude executes. |
| `status: in-progress` | Actively being worked; a branch/worktree exists. | Finish and open a PR. |

### Autonomy, risk, and model labels

Alongside the single `status:` label, an issue that is `status: ready`
carries exactly one `autonomy:` label and one `risk:` label, and
optionally `model: opus`. These turn Timothy's pre-approval into
something an agent can act on unattended:

| Label | Meaning for the agent |
|---|---|
| `autonomy: ship-to-merge` | Open the PR, and once CI is green **merge it yourself**, delete the branch, close the issue. No LGTM needed. Only valid together with `risk: low`. |
| `autonomy: pr-only` | Open the PR, label it `status: in-review`, assign `turtlemonvh`, stop. Merge after an LGTM comment. |
| `autonomy: design-first` | Write a one-page decisions brief (defaults + alternatives) and publish it for review before any code. If no row is `risk: high`, proceed at `pr-only` without waiting. |
| `risk: low` | Reversible; no data, wire-format, or external-account change. |
| `risk: medium` | Additive schema or protocol change, or touches CI/install. |
| `risk: high` | Migrations, deletes, external accounts, or security surface. Never ship-to-merge. |
| `model: opus` | Run on Opus or Fable rather than Sonnet: taste or design judgment matters. |

Silence is approval: a decisions brief row nobody commented on is
approved. Batched questions get a proposed answer each, and the agent
proceeds on the proposed answer unless told otherwise.

Unattended operation (the nightly `/night-crew` loop from the `core`
plugin) reads `.claude/night-crew.json` in this repo for the queue label,
concurrency, and merge policy, and injects the "Working unattended" rules
at session start. See that plugin's `night-crew` skill.

### Ownership: assignee = whose turn

Labels say what kind of work is needed; the assignee says who owns the
next action. Claude has no GitHub account, so "Claude's turn" is the
*absence* of an assignee.

```bash
gh issue list -l "status: ready"     -S "no:assignee"   # Claude can pick these up
gh issue list -l "status: in-review" -a turtlemonvh     # waiting on Timothy
gh issue list -l "status: in-review" -S "no:assignee"   # waiting on Claude
```

### Visible attribution

Claude and Timothy both post GitHub comments as `turtlemonvh` (same
login, no separate bot identity yet — see "PR / merge workflow" below),
so a rendered issue/PR thread shows no author distinction and reads as
Timothy arguing with himself. Every comment Claude posts — justification
comments and any other issue/PR comment — opens with a visible marker
line, blank line, then the body:

```markdown
**🤖 Claude**

<comment body>
```

This is separate from the hidden `<!-- blanket-label-note ... -->`
marker below, which stays machine-only and unchanged — the two solve
different problems (audit tooling vs. human readability) and both
apply. Applies going forward only; comments already posted before this
convention aren't backfilled. PR *descriptions* already carry their own
"Generated with Claude Code" footer and don't need this too.

### Justification comments

Every `status:` label application gets a comment explaining why —
whoever applied it. Claude-written notes carry a hidden marker so the
audit can tell them apart from a human's, since both currently post as
`turtlemonvh` via `gh`:

```markdown
<!-- blanket-label-note label="status: ready" by="claude" -->
**🤖 Claude**

**`status: ready`** — acceptance criteria are fully specified in the
issue body; the change is confined to `server/serve_tasks.go` and its
test. No open questions.
```

`by` is `claude` or `human`; add `via="claude-backfill"` when Claude
wrote up a human's reasoning after the fact. A human's own plain-prose
comment after their label event counts as justification without a
marker.

Per-label content requirements:

- `needs-triage` — state what would unblock it (questions, preconditions).
- `in-review` — state the review scope and link the Word doc.
- `needs-info` — enumerate the actual questions.
- Others — a sentence or two on why the state fits.

The audit's actor allowlist is `turtlemonvh` plus configured bot
logins. Comments/label events from anyone else are ignored — not a
finding, not a justification. This is a deliberate simplicity-over-
precision tradeoff for a single-maintainer repo; expand it if the repo
takes outside contributions.

### State machine

```
new issue -> needs-triage -> needs-info -\
                           -> needs-design -> in-review (assignee: timothy) -> ready
                           -> ready (agent claims) -> in-progress -> in-review (PR, assignee: timothy) -> closed
```

A design review can loop back to `needs-design`, or fan out into new
`status: ready` child issues.

### Design workflow (needs-design -> in-review)

1. `superpowers:brainstorming` — interview, approaches, sectioned design.
2. `tvh-core:publishing-plans-to-word` — publish to OneDrive for inline
   comments. The Word doc is the record; nothing design-shaped is
   committed to the repo.
3. Label `status: in-review`, assign `turtlemonvh`, justification note
   with the doc link and review scope.
4. On approval, move to `status: ready` (or child issues).

### PR / merge workflow

`blanket-ready-batch` opens a PR and, for `autonomy: ship-to-merge` +
`risk: low` issues, merges it itself once CI is green. For everything
else it stops at the PR. Flow: agent claims a `status: ready` issue (label ->
`in-progress`), opens a PR (label -> `in-review`, assignee: `turtlemonvh`).
**GitHub does not copy an issue's labels/assignee onto its linked PR** —
apply `status: in-review` + assignee to *both* the issue and the PR, or
Timothy reviewing the PR sees neither.

Claude authenticates to GitHub as `turtlemonvh` (the same account,
since there's no separate bot identity yet), so **every PR Claude opens
is self-authored** — GitHub refuses to let an author approve their own
PR, and branch protection on `master` doesn't require reviews anyway
(only the `test` status check), so there is no native "Approve" button
to use here. Timothy's review signal is a plain PR comment (e.g.
"approved" / "LGTM") rather than a GitHub review; Claude merges after
seeing it. (Future: a separate bot account/GitHub App would restore a
real Approve button; a risk-rating tag may also let low-risk PRs skip
the signal entirely — neither implemented yet.)

### Audit

Read-only by default; two `gh` calls per issue:

```bash
gh api repos/turtlemonvh/blanket/issues/N/timeline --paginate   # labeled/unlabeled events: actor + timestamp
gh issue view N --json labels,assignees,comments                # current state
```

Findings: no status label, multiple status labels, an unjustified
label, `status: ready` carrying an assignee, `status: in-progress`
stale >7 days with no linked branch/PR activity, or `status: in-review`
whose note has no URL. The audit proposes a fix per finding and asks
before writing anything.

### Skills

- **`blanket-issue-triage`** — the only sanctioned way to change a
  `status:` label. Applies label + assignee + justification comment as
  one step.
- **`blanket-issue-audit`** — runs the audit above; guided repair.
- **`blanket-ready-batch`** — dispatches up to 3 concurrent subagents
  (in worktrees) against `status: ready` + unassigned issues.

## Task Type Schema

TOML files under any directory in `tasks.typesPaths` (config). Loader
is `tasks/task_types.go`; the name is the filename stem.

```toml
tags = ["exec:bash", "os:unix"]  # worker-capability match; worker must advertise all
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
