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

Ten tools, gated by `mcp.mode`:

| Tool | Args | Tier |
| --- | --- | --- |
| `blanket_docs` | `page` | readonly |
| `blanket_task_types` | `name?` | readonly |
| `blanket_tasks` | `id?`, `states?`, `types?`, `limit?`, `log_lines?` | readonly |
| `blanket_workers` | `id?`, `log_lines?` | readonly |
| `blanket_write_task_type` | `name`, `toml` | create |
| `blanket_submit_task` | `type`, `env?`, `notBefore?`, `cron?` | create |
| `blanket_run_task` | `type`, `env?`, `waitSeconds?` | create |
| `blanket_launch_worker` | `tags`, `count?` | create |
| `blanket_cancel_task` | `id`, `force?`, `delete?` | all |
| `blanket_stop_worker` | `id`, `delete?` | all |

`create` mode includes `readonly`'s tools; `all` includes both.

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

Every tool returns plain text (compact tables for lists, a labeled
key-value block for a single item), not JSON — this keeps the response
small and just as readable to an agent.

## Context cost

`tools/list` plus the server's instructions text is kept under **5,000
characters (~1,250 tokens)** in the default `mcp.mode = "all"` — the
worst case, since narrower modes register fewer tools. This is a
test-enforced budget (`TestToolListFitsContextBudget`), not just a
target; the actual measured size is logged by that test on every run.
As of this writing that measured size is 4,958 characters.

The budget has been raised twice, each time for a tool surface that grew
rather than prose that sprawled: 4,000 → 4,400 when
`blanket_submit_task` gained `notBefore`/`cron` and
`blanket_cancel_task`'s description grew to cover the new task states it
can cancel (turtlemonvh/blanket#61's pause/resume/schedule rework), and
4,400 → 5,000 for the tenth tool, `blanket_run_task`
(turtlemonvh/blanket#27) — submit-and-wait is the interaction an agent
actually wants, and a tool it can't see is worth nothing. Descriptions
were trimmed first; the raise covers what was left.

If you're tight on context budget elsewhere, set `mcp.mode = "readonly"`
to cut this further, or wait for the tool-search / dynamic-discovery
mode tracked in issue #44 if the tool count grows.

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

- `readonly` — the four read tools only. Safe to expose broadly.
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
   its stdout log, for debugging a failure.

## Configuration reference

```
mcp.enabled         true    # mount /mcp at all
mcp.mode            "all"   # readonly | create | all
mcp.writeTypesPath  ""      # defaults to the first tasks.typesPaths entry
mcp.validateStrict  false   # also refuse blanket_write_task_type on warnings
mcp.maxLogLines     200     # hard cap on log_lines, regardless of what's asked
```
