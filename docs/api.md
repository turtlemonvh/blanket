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
```

See [task_flow.md](task_flow.md) for the full state machine and
which endpoint drives each transition.

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

## Streaming endpoints (SSE)

Four routes hold a `text/event-stream` connection open:

```
GET /task/:id/log               # stdout of a running task, line by line
                                 # (`message` events)
GET /worker/:id/log             # a worker's logfile, same shape
GET /ui/sse/tasks               # `tasks-changed` — a nudge to re-fetch;
                                 # carries no payload
GET /ui/sse/workers             # `workers-changed`, likewise
```

All four also emit a **`server-restarting`** event, and only that event,
as their final frame when the server is shutting down or restarting
itself. It is preceded by a bare `retry:` field telling the browser how
soon to reconnect (1000 ms), and the stream then closes:

```
retry: 1000

event:server-restarting
data:the server is shutting down; reconnecting
```

A client should treat it as "reconnect shortly", not as an error — the
stream ended on purpose, not because anything went wrong. The embedded UI
raises a banner on it and takes the banner down when a stream reconnects
(`server/ui/static/sse-restart-banner.js`); an `EventSource` reconnects on
its own, so a non-UI client that ignores the event still recovers, just
without the explanation. See
[design.md](design.md#shutdown-sequence) for why the event exists at all:
`net/http`'s `Shutdown` waits indefinitely on an active connection, so a
streaming handler that doesn't return of its own accord would hang every
restart forever.

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
