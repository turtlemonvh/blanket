# MCP Interface

Blanket exposes an [MCP](https://modelcontextprotocol.io) server at
`/mcp` on the same port as the REST API (default `8773`) whenever
`mcp.enabled` is true (the default). Any MCP-capable agent pointed at a
running `blanket serve` can author task types, submit tasks, launch and
inspect workers, and debug a failed task — no repo checkout, no skill
install, no second process.

## Security — read this first

By default the MCP server binds every network interface (same as the
REST API) and `mcp.mode = "all"`, meaning any host that can reach the
port can write a task-type TOML and launch a worker to run it — that's
arbitrary code execution as the blanket user. This is a difference in
degree, not kind, from the REST API's existing unauthenticated
`POST /task/`, but it's a notably cleaner primitive.

The default posture trades this off deliberately for zero-effort setup —
the expected deployment is behind a private overlay network (e.g.
[Tailscale](https://tailscale.com/)), which delivers traffic to the
host's own network interface rather than `127.0.0.1`, so binding only
loopback would actually break that use case. **If you're running blanket
somewhere more exposed than a private network, set `mcp.mode =
"readonly"` or `mcp.enabled = false`.** Token auth and a loopback-default
`bindAddress` option are tracked in
[issue #44](https://github.com/turtlemonvh/blanket/issues/44).

## What's exposed

Eleven tools, gated by `mcp.mode`:

| Tool | Args | Tier |
| --- | --- | --- |
| `blanket_docs` | `query?`, `page?`, `section?` | readonly |
| `blanket_task_types` | `name?` | readonly |
| `blanket_tasks` | `id?`, `states?`, `types?`, `limit?`, `log_lines?` | readonly |
| `blanket_task_logs` | `id`, `stream?`, `grep?`, `context?`, `offset?`, `limit?` | readonly |
| `blanket_workers` | `id?`, `log_lines?` | readonly |
| `blanket_write_task_type` | `name`, `toml`, `overwrite?` | create |
| `blanket_submit_task` | `type`, `env?`, `notBefore?`, `cron?` | create |
| `blanket_run_task` | `type`, `env?`, `waitSeconds?` | create |
| `blanket_launch_worker` | `tags`, `count?` | create |
| `blanket_cancel_task` | `id`, `force?`, `delete?` | all |
| `blanket_stop_worker` | `id`, `delete?` | all |

`create` mode includes `readonly`'s tools; `all` includes both.

### Tool annotations

Each tool carries MCP
[tool annotations](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)
in `tools/list`, so a client can auto-approve the reads and warn before a
destructive call:

| Tools | `readOnlyHint` | `destructiveHint` | `idempotentHint` |
| --- | --- | --- | --- |
| `blanket_docs`, `blanket_task_types`, `blanket_tasks`, `blanket_task_logs`, `blanket_workers` | `true` | — | `false` |
| `blanket_submit_task`, `blanket_run_task`, `blanket_launch_worker` | `false` | `false` | `false` |
| `blanket_write_task_type`, `blanket_cancel_task`, `blanket_stop_worker` | `false` | `true` | `false` |

`blanket_write_task_type` is marked destructive, unlike the rest of the
`create` tier, because it can replace an existing task type's file (see
`tasks.requireExplicitTypeOverwrite` below to make that need an explicit
`overwrite=true`).

The spec has clients treat annotations as untrusted hints, so they are
an aid to a client's approval prompts, not a security boundary.
`mcp.mode` is still what decides which tools exist at all.

### Errors and task detail

- An unknown task type name (`blanket_task_types`, `blanket_submit_task`,
  `blanket_run_task`) or doc page key (`blanket_docs`) errors with
  `did you mean: ...?` naming the three closest valid names by edit
  distance, so a typo costs one retry rather than a listing call.
- `blanket_tasks(id=...)` includes a `resultDir:` line: the task's result
  directory on the worker's machine. An agent on that machine can read
  the task's output files directly; fetching them from another machine
  is [#235](https://github.com/turtlemonvh/blanket/issues/235).

### `blanket_submit_task` vs `blanket_run_task`

Two verbs rather than one tool with a mode flag: model tool selection is
driven by name and description, and "queue this and don't wait" and "run
this and give me the output" are different intentions.

- **`blanket_submit_task`** queues a task and returns its id and state.
  It is the one that takes `notBefore` / `cron`, since a scheduled or
  recurring submission is by definition not something to wait for.
- **`blanket_run_task`** submits, waits for a terminal state, and returns
  state, exit code, both output tails, and the parsed
  [`result_file`](task_type_definitions.md#result_file) in a single tool
  result — one call where submit-then-poll-then-fetch-logs was three.
  Reach for it for anything short.

  `waitSeconds` defaults to `tasks.sync.defaultWait` (30s) and is
  **clamped** to `tasks.sync.maxWait` (300s) rather than rejected, with a
  note in the result — unlike `POST /task/?wait`, which answers 400 over
  the cap, because a model can't cheaply retry a rejected call. If the
  wait runs out the tool says so and names the task id; the task keeps
  running and `blanket_tasks(id=...)` picks it back up.

  Output tails are cut to `mcp.maxLogLines` (default 50 lines per
  stream), tighter than the REST payload's `tasks.sync.maxLogLines`,
  because a tool result lands directly in a context window.

Both need a worker whose tags are a superset of the task type's tags
before anything actually runs — `blanket_run_task` with no matching
worker just burns its wait and reports the task still `WAITING`.

### Reading a task's log: `blanket_task_logs`

`blanket_tasks(id=...)` returns a short tail of stdout, which is often
enough. For anything more, `blanket_task_logs` searches the log on the
server so only the lines that matter reach the model. There are
deliberately no MCP resources for logs: a resource invites pulling a
whole file, and task logs can be tens of megabytes.

- `stream` — `stdout` (default) or `stderr`.
- `grep` — a [Go RE2](https://pkg.go.dev/regexp/syntax) regex. Returns
  each matching line with its line number plus `context` lines either
  side (default 2, max 10), formatted like `grep -n -C`: `12:` marks a
  match, `11-` a context line, `--` separates non-adjacent groups. The
  header gives the total match count.
- `offset` / `limit` — 0-based; with `grep` they count matches, without
  it lines. A negative `offset` counts back from the end, so
  `offset=-50` is the last 50 lines (or, with `grep`, the last 50
  matches). `limit` defaults to 50 and is capped by `mcp.maxLogLines`.
  When there is more, the result ends with `more: offset=N` — pass that
  back for the next page.

Each line is cut at 2,000 characters with a `…[+N chars]` marker. A page
whose total text would still be over 40,000 characters (~10k tokens) is
refused with an error suggesting a narrower `grep`, less `context`, or a
smaller `limit` — never silently cut short. The file is read line by
line, so a large log costs a scan, not memory; a negative `offset` costs
one extra counting pass.

Every tool returns plain text (compact tables for lists, a labeled
key-value block for a single item), not JSON — this keeps the response
small and just as readable to an agent.

### `blanket_docs`: search, page, section

The docs are searchable and fetchable a section at a time, so more
guidance can live in them without costing context up front
(turtlemonvh/blanket#230):

- **`query`** returns the 5 best-matching sections across every page:
  page key, section id, heading, and a ~200-character snippet.
- **`page`** alone returns the whole page if it is at most 12,000
  characters (`authoring`, which the server instructions point at, is
  well under). A bigger page (`api`, `flow`, `upgrade`, `usage`) returns
  a table of contents — section ids, headings, sizes — plus its opening
  text.
- **`page` + `section`** returns that section, `##` sections including
  their `###` subsections. One over the cap returns its subsections'
  table of contents instead: refused and redirected, never truncated.

Section ids are GitHub-style heading anchors (`#errors`), the same ones
the docs link to; the leading `#` is optional. Search is BM25 over the
`#`/`##`/`###` sections in `lib/docs`, heading terms weighted ×3, a
lowercase alphanumeric tokenizer with a short stop-word list and no
stemming. The index lives in memory only: the server starts building it
in the background at startup (a few milliseconds, about 1 MB), and a
search that arrives first waits for that build rather than seeing an
empty index.

## Context cost

`tools/list` plus the server's instructions text is kept under **7,000
characters (~1,750 tokens)** in the default `mcp.mode = "all"` — the
worst case, since narrower modes register fewer tools. This is a
test-enforced budget (`TestToolListFitsContextBudget`), not just a
target; the actual measured size is logged by that test on every run.
As of this writing that measured size is 6,591 characters -- 409 short
of the budget. Adding another doc page means trimming something first.
Server-side tool search was considered in #44 and rejected (clients
already do it); the fix for guidance outgrowing the budget is
section-level `blanket_docs` search (#230, above), which costs only its
arguments up front.

The budget has been raised four times, each time for a tool surface
that grew rather than prose that sprawled: 4,000 → 4,400 when
`blanket_submit_task` gained `notBefore`/`cron` and
`blanket_cancel_task`'s description grew to cover the new task states it
can cancel (turtlemonvh/blanket#61's pause/resume/schedule rework);
4,400 → 5,000 for the tenth tool, `blanket_run_task`
(turtlemonvh/blanket#27) — submit-and-wait is the interaction an agent
actually wants, and a tool it can't see is worth nothing, and
descriptions were trimmed first; 5,000 → 6,000 for
[tool annotations](#tool-annotations) and `blanket_write_task_type`'s
`overwrite` argument (turtlemonvh/blanket#229), which took the measured
size from 4,986 to 5,822. About 750 of those characters are the
annotations objects themselves: go-sdk serializes every hint field on
every tool, and there is no prose in them to trim. Then 6,000 → 7,000
for the eleventh tool, `blanket_task_logs` (turtlemonvh/blanket#231),
which took the measured size from 5,822 to 6,591 (~770 characters
including its annotations); `blanket_tasks`' argument descriptions were
trimmed first, and a docs-concision pass (#242) is meant to win
some of it back.

When a new tool or argument trips that test, the levers are, in order:
trim the `jsonschema:"..."` argument descriptions, then the tool
`Description` strings, then move prose out of both and into
`blanket_docs`. Every handler already returns plain text rather than
JSON, which suppresses `outputSchema` generation — the single biggest
saving, and not a lever to give back. Raising the constant is the last
resort, and only for a tool surface that genuinely grew.

If you're tight on context budget elsewhere, set `mcp.mode = "readonly"`
to cut this further.

## Setup

**Claude Code**, user scope (available in every project):

```
claude mcp add --transport http blanket http://localhost:8773/mcp
```

Or project scope, committed to the repo as `.mcp.json`:

```json
{
  "mcpServers": {
    "blanket": { "type": "http", "url": "http://localhost:8773/mcp" }
  }
}
```

Run `/mcp` inside Claude Code afterward to confirm the connection and see
the tool list.

**Claude Desktop / other MCP clients**: point them at
`http://<host>:8773/mcp` using their own streamable-HTTP MCP config —
the endpoint is a standard MCP streamable HTTP server, not
Claude-Code-specific.

## Permissions

Set `mcp.mode` in your blanket config file:

```json
{
  "mcp": {
    "mode": "readonly"
  }
}
```

- `readonly` — the five read tools only. Safe to expose broadly.
- `create` — adds write-a-task-type, submit-a-task, launch-a-worker.
  Code execution as the blanket user, same as the REST API today.
- `all` (default) — adds cancel-task and stop-worker.

Set `mcp.enabled = false` to not mount `/mcp` at all.

## Worked example

1. `blanket_docs(page="authoring")` — read the task-type authoring
   guide.
2. `blanket_write_task_type(name="hello", toml="...")` — validates
   first; refuses to write on any error-level finding, and returns the
   findings either way so you can fix and retry.
3. `blanket_workers()` — check whether a worker exists whose tags are a
   superset of `hello`'s tags. If not, `blanket_launch_worker(tags=[...])`.
4. `blanket_run_task(type="hello")` — submits and waits; returns the
   state, exit code and output in one result. Or
   `blanket_submit_task(type="hello")` to queue it and move on, which
   returns just the id.
5. `blanket_tasks(id="<id>", log_lines=50)` — for a task you queued
   rather than ran: check status
   (`WAITING`/`RUNNING`/`SUCCESS`/`ERROR`/...) and the last 50 lines of
   its stdout log.
6. `blanket_task_logs(id="<id>", grep="(?i)error|panic")` — when the
   tail isn't enough: every matching line in the log with two lines of
   context and its line number. Then
   `blanket_task_logs(id="<id>", offset=1180, limit=40)` to read the
   stretch around a hit, or `stream="stderr"` for the other stream.

## Configuration reference

```
mcp.enabled         true    # mount /mcp at all
mcp.mode            "all"   # readonly | create | all
mcp.writeTypesPath  ""      # defaults to the first tasks.typesPaths entry
mcp.validateStrict  false   # also refuse blanket_write_task_type on warnings
mcp.maxLogLines     200     # hard cap on log_lines / blanket_task_logs limit

tasks.requireExplicitTypeOverwrite  false  # replacing an existing task type file needs overwrite=true
```

`tasks.requireExplicitTypeOverwrite` lives under `tasks.` rather than
`mcp.` because it is a rule about task-type files, not about one
interface: `blanket_write_task_type` honors it today, and a REST
task-type write endpoint, when one is added, is meant to honor the same
key. With it `false` (the default), `blanket_write_task_type` replaces an
existing file and says `overwrote`; with it `true`, the call is refused
with a message naming `overwrite=true` unless that argument is set.
Writing a new file never needs the flag.
