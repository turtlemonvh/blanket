# API

The blanket server exposes a JSON REST API on the configured port
(default `8773`). The same set of endpoints is used by the embedded
HTMX UI, the CLI, and any external client. All bodies are JSON unless
noted otherwise.

## Tasks

User-facing endpoints — submit, list, inspect, cancel.

```
GET    /task/                   # list tasks (filterable via query string)
GET    /task/:id                # fetch a single task
POST   /task/                   # submit a new task (JSON or multipart form)
                                 # ?wait[=30s] blocks until the task finishes
                                 # and returns its outcome — see
                                 # "Synchronous submission" below
DELETE /task/:id                # delete a task; kills it if running.
                                 # For a RECURRING/PAUSED task template,
                                 # this removes the record outright — see
                                 # PUT .../cancel below for the
                                 # keep-the-record alternative.
PUT    /task/:id/cancel         # cancel a WAITING, SCHEDULED, RECURRING, or
                                 # PAUSED task; transitions to STOPPED (record
                                 # kept). For a RUNNING task, requires
                                 # ?force=true — otherwise 400. See task_flow.md.
PUT    /task/:id/pause          # RECURRING -> PAUSED; sets pausedTs. 400 if
                                 # not currently RECURRING.
PUT    /task/:id/resume         # PAUSED -> RECURRING; clears pausedTs,
                                 # recomputes nextFireTs from now. 400 if not
                                 # currently PAUSED.
PUT    /task/:id/schedule       # change a live series' schedule -- body
                                 # {"cron": "..."} for a RECURRING/PAUSED
                                 # template, or {"notBefore": "..."} for a
                                 # SCHEDULED one-shot task. 400 on a state/body
                                 # mismatch or an invalid value.
GET    /task/:id/log            # stream stdout (SSE)
GET    /task/:id/log/tail       # last N lines of stdout
```

`POST /task/` accepts a JSON body (or a multipart form with a `data`
field holding the same JSON, plus any files to place in the task's
result dir) with these fields:

| Field         | Required | Description |
| ------------- | -------- | ----------- |
| `type`        | yes      | Name of a loaded task type. |
| `environment` | if the type has required vars | Map of string → string, merged over the type's default env. |
| `notBefore`   | no       | Delays a one-shot task. A Go duration relative to now (`"10m"`, `"30s"`), an RFC3339 timestamp, or a unix-seconds integer. If in the future, the task starts in state `SCHEDULED` instead of `WAITING`; the scheduler loop promotes it once due. Mutually exclusive with `cron`. |
| `cron`        | no       | A standard 5-field cron expression (minute hour dom month dow). Makes this task a `RECURRING` template: it never runs itself — the scheduler spawns a child task (own id, log, and result dir; `parentId` set to the template) at every fire time. Mutually exclusive with `notBefore`. |

### Synchronous submission: `POST /task/?wait`

By default `POST /task/` returns **201** the moment the task is queued and
the caller polls `GET /task/:id` until it finishes. With `?wait` the same
call blocks until the task reaches a terminal state and answers with the
task's outcome, output, and result artifact in one response — the shape
you want for a task that finishes in two seconds.

Nothing else about the endpoint changes: same JSON body (or multipart
`data` field), same validation, same file uploads, same queue and worker
executing the task. It is a query parameter on the existing route rather
than a second route precisely so there is nothing to keep in sync.

```
POST /task/                      # unchanged: 201, returns immediately
POST /task/?wait                 # block up to tasks.sync.defaultWait (30s)
POST /task/?wait=30s             # block up to 30 seconds
POST /task/?wait=30&fail_on_error=true
```

| Parameter | Value | Meaning |
| --------- | ----- | ------- |
| `wait` | absent | Unchanged behavior: 201, return immediately. |
| `wait` | present, no value | Block for `tasks.sync.defaultWait`. |
| `wait` | `30s`, `2m`, `1h`, or a bare number of seconds (`30`) | Block for that long. Over `tasks.sync.maxWait` is a **400** — a caller should never believe it waited longer than it did, so there is no silent clamp. |
| `fail_on_error` | absent / `false` | A task that failed still answers **200**; its outcome is in `state` and `exitCode`. |
| `fail_on_error` | present with no value, or `true` | A non-`SUCCESS` terminal state answers **502** instead, with the same body — gives `curl --fail` a way to notice. |

Responses:

| Situation | Status | Body |
| --------- | ------ | ---- |
| Task reached a terminal state within the wait | **200** | The completion payload below |
| ...and `fail_on_error=true` with a non-`SUCCESS` terminal state | **502** | The same completion payload |
| Wait expired, task still live | **504** | `{"id", "state", "waitOutcome": "wait_timeout", "pollUrl", "error"}` — the task is untouched and keeps running; poll `pollUrl` |
| Bad `type`/`environment`, unparseable `wait`, `wait` over the cap | **400** | The usual `{"error": "..."}` |
| Client disconnected | — | Nothing. The task keeps running and its results stay fetchable by id |

Note that a *failed task* is a 200 by default: an HTTP status describes
whether the API call worked, and the task's own outcome is carried by
`state` and `exitCode` in the body. `fail_on_error=true` is the opt-in for
callers that would rather have the status carry both.

The completion payload:

```json
{
  "task": { "id": "68b4...", "type": "echo_task", "state": "SUCCESS",
            "exitCode": 0, "resultDir": "/var/blanket/results/68b4...", "...": "..." },
  "waitOutcome": "completed",
  "stdout": "hello world\n",
  "stderr": "",
  "stdoutTruncated": false,
  "stderrTruncated": false,
  "result": { "answer": 42 },
  "resultError": null
}
```

| Field | Meaning |
| ----- | ------- |
| `task` | The existing task JSON, verbatim — same fields `GET /task/:id` returns, including the new `exitCode`. |
| `waitOutcome` | `"completed"`. (`"wait_timeout"` is the other value; it exists for the streaming variant, which cannot change its HTTP status once the body has started.) |
| `stdout` / `stderr` | The last `tasks.sync.maxLogLines` lines of each stream, read from `blanket.stdout.log` / `blanket.stderr.log` under the task's result dir. Empty if the task wrote nothing. |
| `stdoutTruncated` / `stderrTruncated` | True when earlier lines were dropped from that tail. |
| `result` | The parsed contents of the task type's declared [`result_file`](task_type_definitions.md#result_file), or `null` — a type that declares none, or a task that failed before writing it, both yield `null` without an error. |
| `resultError` | Why `result` is `null` despite a declared `result_file` (unparseable, oversized, unreadable), so a malformed result never looks like an absent one. `null` when there was nothing wrong. |

Config keys (defaults shown):

```
tasks.sync.defaultWait     30s       # applied to a bare ?wait
tasks.sync.maxWait         300s      # hard cap; a larger ?wait is a 400
tasks.sync.maxLogLines     200       # lines of stdout and of stderr in the payload
tasks.sync.maxResultBytes  1048576   # cap on the result_file size that gets parsed
```

Blanket has no authentication on any route, so a waiting call is also a
way for an unauthenticated caller to hold a connection and a server
goroutine open. `tasks.sync.maxWait` is the only control on that; its
default is chosen with that in mind, not just with task duration in mind.
Lower it if the server is reachable beyond a trusted network.

Two caveats worth knowing before reaching for `?wait`:

* Workers discover queued work by polling, so even a 100ms task takes
  roughly 2-4 seconds of wall clock. `?wait` doesn't change that.
* A worker that dies mid-task strands its task in `CLAIMED`/`RUNNING`
  (orphan recovery is not implemented yet), and a synchronous caller
  waiting on such a task simply burns its whole wait budget and gets a
  504.

A `notBefore`-in-the-future or `cron` submission returns **429** with a
JSON error body if accepting it would bring the count of live
`SCHEDULED`+`RECURRING`+`PAUSED` tasks to or past the `scheduler.maxScheduled`
config limit (default `10000`) — see
[task_flow.md](task_flow.md#scan-limit-schedulermaxscheduled) for how the
limit is enforced and configured.

Every task response (`GET`/`POST` above) includes a computed
`scheduleDescription` string field: a friendly rendering of the task's
schedule (e.g. `"Every 5 minutes"` for a cron template, or `"Once, at
2026-09-05T08:00:00-04:00"` for a delayed one-shot task), or `""` for a
task with no schedule of its own. See
[task_flow.md](task_flow.md#friendly-schedule-text).

`GET /task/?parentId=<template-id>` filters the list to a series' child
runs — every task whose `parentId` matches the given `RECURRING`/`PAUSED`/
cancelled `STOPPED` template's id. Combines with the endpoint's other
filters (`states`, `types`, `limit`, etc.).

`GET /schedule/describe?cron=<expr>` validates a cron expression and
returns `{"cron": "<expr>", "description": "<friendly text>", "next":
["<RFC3339>", "<RFC3339>", "<RFC3339>"]}` — the next three upcoming fire
times, local time — or 400 with the parser's error message if `expr` is
invalid or the `cron` query parameter is missing. This is the JSON
version; the UI's create form renders its live preview from the HTML
sibling under "Web UI" below.

See [task_flow.md](task_flow.md#scheduling-scheduledts--recurring-tasks-turtlemonvhblanket61)
for the full scheduling state machine, the pause/resume/cancel/change-schedule
lifecycle, and the scan limit, and [usage.md](usage.md#web-ui) for the
browser surfaces built on these endpoints (the Upcoming page and the series
detail view).

Worker-facing endpoints — used by `blanket worker` to advance task
state.

```
POST   /task/claim/:workerid    # claim a task matching the worker's tags
PUT    /task/:id/run            # mark CLAIMED → RUNNING
PUT    /task/:id/progress       # update percent-complete (0-100)
PUT    /task/:id/finish         # mark RUNNING → SUCCESS / ERROR / TIMEDOUT
                                 # ?state=<terminal state> (required)
                                 # ?exitCode=<int> (optional) records the
                                 # process exit status
```

`PUT /task/:id/finish` takes its terminal `state` as a query parameter,
plus an optional `exitCode` — the same way `run` passes `timeout`, `pid`,
and `typeDigest`. It lands on the task record's `exitCode` field, which
every task response carries:

* an integer once a process has run to completion (`0` for a clean exit,
  `3` for `exit 3`, ...)
* `null` when there is no exit status of its own to report — the task
  hasn't finished, its process was killed by a signal (`STOPPED`,
  `TIMEDOUT`), or it never started. The field is nullable precisely so
  "unknown" stays distinguishable from a genuine exit 0.

A present-but-unparseable `exitCode` is a **400**. Task records written
before this field existed decode with `exitCode: null`; no migration is
needed.

See [task_flow.md](task_flow.md) for the full state machine and
which endpoint drives each transition.

Note that worker-facing routes are unauthenticated, like everything else
blanket serves, so `exitCode` is spoofable by anything that can reach the
port. That is not a change in posture, but it is worth knowing before
treating an exit code as trustworthy.

## Task types

Read-only — task types are loaded from TOML files at startup. See
[task_type_definitions.md](task_type_definitions.md) for the schema.

```
GET /task_type/                 # list all loaded task types
GET /task_type/:name            # fetch one by name
```

The `description` and `documentation` fields (if set in the TOML) are
included in both responses like any other config key.

## Workers

Read.

```
GET /worker/                    # list workers
GET /worker/:id                 # fetch one
GET /worker/:id/log             # SSE stream of worker log
GET /worker/:id/log/tail        # last N lines of worker log
GET /worker/:id/logs            # full logfile download
```

Lifecycle.

```
POST   /worker/                 # launch a new worker (used by the UI)
PUT    /worker/:id              # initial creation + status updates from worker
PUT    /worker/:id/stop         # stop after current task; sets Stopped=true
                                 # ?force=true also sends an immediate kill
                                 # signal to the worker process
PUT    /worker/:id/restart      # re-start an existing stopped worker
DELETE /worker/:id              # remove from DB; only valid if stopped
```

## Server

```
GET /                           # redirects to the web UI
GET /version                    # build info as JSON
GET /config/                    # processed server config
GET /ops/status/                # runtime metrics (goroutines, memory, etc.)
```

## Web UI

`/ui/*` serves the embedded HTMX UI: pages, static assets, SSE streams,
and the `/ui/partials/*` HTML fragments the pages swap in. These return
HTML, not JSON, and are an implementation detail of the UI rather than a
client-facing API — see [usage.md](usage.md#web-ui) for what the pages
do, and `server/server.go` for the full route list. Three groups are
worth knowing about, because they mirror JSON endpoints above:

```
GET /ui/partials/schedule-preview?cron=<expr>
                                # the live human-readable rendering of a
                                # cron expression shown under a cron
                                # field — on the create form and on a
                                # series' "change the schedule" editor:
                                # its description plus the next three
                                # fire times, or the parser's message as
                                # an inline error. Always 200 — an
                                # invalid expression is content to
                                # display, and htmx only swaps 2xx
                                # responses. JSON equivalent:
                                # GET /schedule/describe.
GET /ui/partials/form-error?error=<message>
                                # renders one rejected-submit message
                                # into the new-task form. A 4xx response
                                # body is never swapped by htmx, so
                                # POST /ui/tasks returns the message on an
                                # HX-Trigger event and the form fetches
                                # this to display it.
```

`POST /ui/tasks` (the create form's submit) accepts the same scheduling
inputs as `POST /task/`, as form fields: `scheduleMode` (`once` /
`repeating`) selects which of `notBefore` / `cron` applies, `notBeforeISO`
(an RFC3339 instant resolved in the browser's timezone) takes precedence
over a bare `datetime-local` `notBefore` value, and an already-past start
time is rejected rather than queued immediately.

The series lifecycle actions used by the series detail page *mutate*:

```
PUT /ui/series/:id/pause        # -> PAUSED
PUT /ui/series/:id/resume       # -> RECURRING
PUT /ui/series/:id/cancel       # -> STOPPED (record kept)
PUT /ui/series/:id/schedule     # form field `cron=<expr>` (the shared
                                # schedule editor also submits
                                # `scheduleMode`; anything but
                                # `repeating` is rejected inline)
```

They are thin wrappers over the same functions
`PUT /task/:id/{pause,resume,cancel,schedule}` call, and exist only so the
browser gets an HTML response it can swap in: each returns the re-rendered
schedule block, carrying the parser's message inline when the action is
rejected (the JSON endpoints return `{}` or an error string, which htmx
can't place without a second round trip). Use the `/task/` endpoints
above from any non-browser client.

## MCP

```
ANY /mcp                        # MCP streamable-HTTP endpoint (JSON-RPC 2.0)
```

Mounted when `mcp.enabled` is true (default). See
[mcp.md](mcp.md) for the tool list, setup instructions, and the
security posture of the default (all-interfaces, `mcp.mode = "all"`)
configuration.
