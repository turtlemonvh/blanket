# API

The blanket server exposes a JSON REST API on the configured port
(default `8773`). The same set of endpoints is used by the embedded
HTMX UI, the CLI, and any external client. All bodies are JSON unless
noted otherwise.

## Errors

Every `:id` route (task or worker) applies one rule consistently: an id
that fails to parse as an `objectid.ObjectId` (malformed -- not valid hex,
wrong length) is a **400**; a well-formed id naming no record is a
**404**. The two are distinguishable client errors -- a malformed id is a
bug in the caller, a missing one might just mean "already deleted" -- and
neither is ever a 500 (turtlemonvh/blanket#115). `DELETE /task/:id` and
`DELETE /worker/:id` are the one deliberate exception: both are
idempotent-delete endpoints and answer 200 even for a well-formed id
naming nothing, same as if it had just been deleted.

Most JSON handlers report an error as `{"error": "<message>"}`; a few
older ones return a plain-text message instead (streaming/tail
endpoints and the web UI in particular) -- check the specific handler if
you need the exact shape.

## Tasks

User-facing endpoints — submit, list, inspect, cancel.

```
GET    /task/                   # list tasks (filterable via query string)
GET    /task/:id                # fetch a single task
POST   /task/                   # submit a new task (JSON or multipart form)
                                 # ?wait[=30s] blocks until the task finishes
                                 # and returns its outcome — see
                                 # "Synchronous submission" below
                                 # &stream returns it as a live event stream
                                 # instead — see "Streaming submission"
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
GET    /task/:id/log            # stream stdout (SSE), or the structured
                                 # event stream with Accept:
                                 # application/x-ndjson / ?format=ndjson
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
* A worker that dies mid-task leaves its task in `CLAIMED`/`RUNNING` until
  the [reaper](task_flow.md#the-reaper) reconciles it, which takes minutes
  by design and may legitimately conclude that the task should be left
  alone. A synchronous caller waiting on such a task burns its whole wait
  budget and gets a 504 well before then.

### Streaming submission: `POST /task/?wait&stream`

Adding `stream` keeps the connection open and emits one event per update
while the task runs, ending with a terminal `result` event that carries
the completion payload above **verbatim**. The stream is
self-terminating: a client that reads to the `result` event never needs a
second fetch.

```
POST /task/?wait&stream          # events, then the completion payload
POST /task/?wait=60s&stream      # ...with a 60s budget
POST /task/?stream               # a stream implies a wait; same as ?wait&stream
```

```bash
curl -sN -X POST 'localhost:8773/task/?wait=60s&stream' \
    -d '{"type": "echo_task"}'
```

A stream is always **200**. The status line is sent before the task's
outcome is known, so `fail_on_error` has no effect here and there is no
504: the `result` event's `waitOutcome` (`"completed"` /
`"wait_timeout"`) carries the distinction, and `task.state` /
`task.exitCode` carry the task's own outcome. A `wait_timeout` result
event means the budget expired with the task still running — it is
untouched and pollable by id, exactly as with the blocking 504.

Disconnecting mid-stream is fine and changes nothing about the task.

#### The event schema

Every event is a JSON object with `ts` (unix seconds), `taskId`, and
`type`. Beyond that:

| `type` | Extra fields |
| ------ | ------------ |
| `state` | `state`, and `previousState` — `null` on the first event, which reports the state the task was already in rather than a transition. |
| `log` | `stream` (`"stdout"` / `"stderr"`), `seq`, `line` (no trailing newline). |
| `result` | The completion payload's fields, flattened: `waitOutcome`, `task`, `stdout`, `stderr`, `stdoutTruncated`, `stderrTruncated`, `result`, `resultError`. |

```
{"ts":1756900000,"taskId":"68b4...","type":"state","state":"WAITING","previousState":null}
{"ts":1756900002,"taskId":"68b4...","type":"state","state":"CLAIMED","previousState":"WAITING"}
{"ts":1756900002,"taskId":"68b4...","type":"state","state":"RUNNING","previousState":"CLAIMED"}
{"ts":1756900002,"taskId":"68b4...","type":"log","stream":"stdout","seq":1,"line":"hello world"}
{"ts":1756900003,"taskId":"68b4...","type":"state","state":"SUCCESS","previousState":"RUNNING"}
{"ts":1756900003,"taskId":"68b4...","type":"result","waitOutcome":"completed","task":{...},"stdout":"hello world\n","stderr":"","result":null,"resultError":null}
```

Notes that matter in practice:

* **`state` events are the debugging tool.** Without them a caller that
  waits 30s and times out can't tell "no worker was free to claim this"
  (still `WAITING`) from "the task is genuinely slow" (reached
  `RUNNING`). Since blanket's workers are hand-launched, the first case
  is common.
* **`log` delivery is best-effort and lags.** The worker flushes
  `blanket.stdout.log` on its own poll interval, and the stream can't
  attach to the log files until the task reaches `CLAIMED` (they don't
  exist before the worker sets the execution directory up). The
  `result` event repeats both tails, so a client that reads to the end
  always has the authoritative output even if it missed live lines.
* **`seq` is per stream, per connection.** `stdout` and `stderr` each
  count from 1. It is not a position in the file, and a reconnecting
  client sees it restart.
* **Ordering between `stdout` and `stderr` is not the task's.** They are
  two files tailed separately; blanket cannot reconstruct the
  interleaving the task produced.
* Unknown event types should be skipped, not treated as errors — that's
  what lets a later blanket add one without breaking your client.

#### Framing: NDJSON or SSE

The JSON object on the wire is identical either way; only the wrapping
differs, and one encoder produces both.

| `Accept` | Framing | `Content-Type` |
| -------- | ------- | -------------- |
| anything else (the default) | One JSON object per line | `application/x-ndjson` |
| `text/event-stream` | SSE frames, `event:` set to the event's `type`, `data:` to the same JSON | `text/event-stream` |

NDJSON is the default because the callers this exists for — `curl` in a
pipeline, the Go CLI, an agent's HTTP client — all parse line-delimited
JSON trivially. SSE is there for browser consumers and matches how
`GET /task/:id/log` already behaves.

### Structured log stream: `GET /task/:id/log`

The default is unchanged: SSE frames of raw stdout lines with
`event: message`, which is what the embedded UI consumes. Asking for the
structured stream instead gets the same events, from the same encoder,
so the two surfaces can't drift:

```bash
curl -sN -H 'Accept: application/x-ndjson' localhost:8773/task/<id>/log
curl -sN 'localhost:8773/task/<id>/log?format=ndjson'
```

| Request | Response |
| ------- | -------- |
| default | `text/event-stream`, raw stdout lines as `event: message` frames (unchanged) |
| `Accept: application/x-ndjson`, or `?format=ndjson` (`?format=events` is a synonym) | `application/x-ndjson`, the `state` / `log` / `result` events above |

The structured variant has no wait budget: it stays open until the task
reaches a terminal state (ending with a `completed` result event) or the
client goes away. Both variants now stay open while the task is live —
before this they closed after the first five idle seconds regardless of
the task's state.

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
                                 # ?runId= &pid= &timeout= &typeDigest=
PUT    /task/:id/progress       # update percent-complete (0-100)
                                 # ?progress= [&runId=]
PUT    /task/:id/finish         # mark RUNNING → SUCCESS / ERROR / TIMEDOUT
                                 # ?state=<terminal state> (required)
                                 # ?runId= (optional) fencing token
                                 # ?exitCode=<int> (optional) records the
                                 # process exit status
```

`PUT /task/:id/finish` takes its terminal `state` as a query parameter,
plus an optional `runId` and `exitCode` — the same way `run` passes
`timeout`, `pid`, and `typeDigest`.

`exitCode` lands on the task record's `exitCode` field, which every task
response carries:

* an integer once a process has run to completion (`0` for a clean exit,
  `3` for `exit 3`, ...)
* `null` when there is no exit status of its own to report — the task
  hasn't finished, its process was killed by a signal (`STOPPED`,
  `TIMEDOUT`), or it never started. The field is nullable precisely so
  "unknown" stays distinguishable from a genuine exit 0.

A present-but-unparseable `exitCode` is a **400**. Task records written
before this field existed decode with `exitCode: null`; no migration is
needed. An omitted `exitCode` leaves whatever is stored alone rather than
clearing it, so a cancel — or a retry of a finish that already landed —
can't erase a code the first report recorded.

`runId` is the worker's **fencing token** for one execution attempt
(turtlemonvh/blanket#23). The worker generates it immediately before it
starts the child process and sends the same value on every transition for
that run. Both endpoints are idempotent with respect to it, because the
worker retries them:

| Request | Task state | Stored `runId` | Sent `runId` | Response |
| --- | --- | --- | --- | --- |
| `run` | `CLAIMED` | — | `R1` | **200**, token stored |
| `run` | `RUNNING` | `R1` | `R1` | **200**, no-op (a retry) |
| `run` | `RUNNING` | `R1` | `R2` | **409** — two runners |
| `run` | `RUNNING` | `R1` | *(empty)* | **200**, no-op (legacy) |
| `run` | terminal | any | any | **409** — e.g. a user already stopped it |
| `finish` | `RUNNING` | `R1` | `R1` | **200**, task becomes terminal |
| `finish` | terminal | `R1` | `R1` | **200**, no-op — first terminal state wins |
| `finish` | `RUNNING` | `R1` | `R2` | **409** — two runners |
| `finish` | `RUNNING` | `R1` | *(empty)* | **200** (legacy) |
| `finish` | `CLAIMED` | any | any | **400** — never started |

Two rules are worth calling out:

- **An empty `runId` is legacy-permissive.** A worker built before the
  token existed sends none, and its task records carry none, so a
  mixed-version install keeps working. Only two tokens that are both
  present and different are a conflict. This leniency goes away with the
  schema bump in phase 4 of #23.
- **The first terminal state wins**, and a repeat is a 200 rather than an
  error. In particular a user's `STOPPED` (via `PUT /task/:id/cancel`)
  beats the `ERROR` the worker reports moments later when the killed child
  exits. Rejecting that late report is what used to strand tasks: the
  worker retried into the same rejection until its deadline and then gave
  up having reported nothing.

`PUT /task/:id/progress` takes the token too, and returns 409 on a
mismatch so a stale runner can't paint over the live one's numbers — but
it's optional there. A task script that wants to send it reads it from
`BLANKET_APP_TASK_RUN_ID`, which the worker exports into the child's
environment alongside the other `BLANKET_APP_*` variables.

A 409 is never retried by the worker; 5xx and transport failures are,
with full-jitter backoff (see `lib/httpx`).

See [task_flow.md](task_flow.md) for the full state machine and
which endpoint drives each transition, and
[task_flow.md](task_flow.md#outcome-journal) for the on-disk journal the
worker keeps alongside these calls.

Two transitions have no worker behind them. The
[reaper](task_flow.md#the-reaper) applies a recovered outcome through this
same `finish` endpoint (carrying the journal's `runId`, so it is
indistinguishable from a very late report by the worker itself), and it
requeues a task whose worker died before the command ever started. A
requeued task goes back to `WAITING` with its `workerId` and `runId`
cleared and its `requeueCount` field — present on every task response —
incremented; once that count reaches `reaper.maxRequeues` the task is
failed to `ERROR` instead of being requeued again.

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
                                 # field-level merge: the worker owns pid,
                                 # pidStartTs, logfile, startedTs, tags,
                                 # checkInterval; the server owns stopped,
                                 # stoppedReason, lastHeardTs and lost
PUT    /worker/:id/heartbeat    # liveness ping; no body. See below.
PUT    /worker/:id/stop         # stop after current task; sets Stopped=true
                                 # ?force=true also sends an immediate kill
                                 # signal to the worker process
PUT    /worker/:id/restart      # re-start an existing stopped worker
                                 # (clears Stopped server-side, then relaunches)
DELETE /worker/:id              # remove from DB; only valid if stopped
```

### Heartbeat (turtlemonvh/blanket#23)

`PUT /worker/:id/heartbeat` takes **no body** and answers:

```json
{
  "stopped": false,
  "lastHeardTs": 1757088000,
  "serverInstanceId": "6a9c504d0372dcda2a889778",
  "serverStartedTs": 1757087000
}
```

| Field | Meaning |
| ----- | ------- |
| `stopped` | The worker's server-side `stopped` flag right now. A worker that reads `true` shuts down after its current task, so a drain lands within one check interval. |
| `lastHeardTs` | The timestamp the server just recorded, **from its own clock**. There is deliberately no way to supply one: every reaper decision is arithmetic on this number, and a worker-supplied value would measure clock skew rather than liveness. |
| `serverInstanceId` | Identifies the server *process*. A change means the server restarted since the last heartbeat. |
| `serverStartedTs` | When that process started serving. |

404 for a worker that is not in the database — an ordinary outcome when an
operator deletes a worker whose process is still winding down, not a
server error. The call also clears the worker's `lost` flag: a worker that
checks in is by definition not lost.

Worker records carry these fields beyond the obvious ones: `pidStartTs`
(the worker process's start time, which is what makes its pid usable as a
liveness signal despite pid reuse), `lastHeardTs`, `lost` (the reaper
believes this worker is gone; a badge in the UI and nothing else),
`stoppedReason`, and — while a restart is in flight —`respawnIntent`,
`respawnAttempts` and `lastRespawnTs`. See
[task_flow.md](task_flow.md#the-reaper) for the decision table the first
few feed, the thresholds, and the `reaper.*` config keys, and
[task_flow.md](task_flow.md#respawn-intent-and-stop-reasons) for the
respawn bookkeeping.

### `PUT /worker/:id/stop`

```
PUT /worker/:id/stop?force=true&reason=<who and why>
```

`force=true` sends the process an immediate kill signal instead of waiting
for its poll loop to notice.

`reason` attributes the stop, and decides the fate of any pending respawn
intent (turtlemonvh/blanket#23 phase 5). A worker's own shutdown handler
sends `reason=worker-shutdown`, which **preserves** the intent — a drained
worker exiting is the drain working. Every other stop, including the
unattributed one an operator or the UI sends, **clears** it: a deliberate
decision to take a worker down mid-restart outranks the restart's plan to
bring it back. Without the reason on the wire the two calls would be
identical, and a drained worker would cancel its own respawn on the way
out.

## Server

```
GET  /                          # redirects to the web UI
GET  /version                   # build info as JSON
GET  /config/                   # processed server config
GET  /ops/status/               # runtime metrics (goroutines, memory, etc.)
POST /ops/backup                # take a database backup — see "Ops endpoints"
GET  /ops/restart/status        # the restart state machine — see "Ops endpoints"
POST /ops/restart/begin
POST /ops/restart/pause
POST /ops/restart/swapped
POST /ops/restart/drain
POST /ops/restart/exec
POST /ops/restart/abort
```

`GET /config/` returns every resolved config key, plus three values that
are properties of the running process rather than of the configuration:
`basepath` (the directory the binary was resolved from), and — as of
turtlemonvh/blanket#23 — `instanceId` and `serverStartedTs`, the same pair
a worker's [heartbeat](#heartbeat-turtlemonvhblanket23) response carries.
A changed `instanceId` means the server process was replaced, which is how
a client tells a restart from a slow request.

## Ops endpoints

A small group of **privileged, local-only** endpoints for operating the
install rather than for using it (turtlemonvh/blanket#23): taking a backup,
and driving a restart.

```
POST /ops/backup                # write a consistent database backup now
                                 # ?dir=<path> overrides the destination

GET  /ops/restart/status        # what the restart machine is doing
POST /ops/restart/begin         # IDLE      -> STAGED
POST /ops/restart/pause         #           -> PAUSED   (worker spawn -> 409)
POST /ops/restart/swapped       #           -> SWAPPED
POST /ops/restart/drain         #           -> DRAINING (stops workers)
POST /ops/restart/exec          #           -> EXECING  (server goes away)
POST /ops/restart/abort         # anything  -> IDLE
```

Every mutating `/ops/` endpoint requires **both**:

- a **loopback** client address, decided from the connection's own
  `RemoteAddr` and never from `X-Forwarded-For` (which the caller writes,
  and could simply set to `127.0.0.1`); and
- an **`X-Blanket-Restart`** header. Its *value* is not checked and is not
  a secret. Its job is to force a browser into a CORS preflight, because a
  cross-origin POST carrying only "simple" headers is sent without one —
  the browser blocks the attacker from reading the response, but the
  request still lands.

`/ops/` is also **carved out of the wildcard CORS handler** that covers the
rest of the API, so that preflight is refused rather than approved. Without
that carve-out, the permissive policy would approve the very preflight the
header exists to provoke.

Anything missing either requirement gets **403** with
`{"error": "..."}`. `curl`, a script, and the blanket CLI are unaffected:
they set the header and connect from loopback.

There is deliberately no auth token yet — loopback-only covers the
single-machine install, which is the only deployment shape blanket has
today.

### `POST /ops/backup`

Takes a point-in-time copy of the database **without pausing anything**.
BoltDB's MVCC makes a read transaction a consistent image of the whole
file, so this is safe on a live, busy server — and it is the only way to
back one up, since the CLI cannot open the database while the server holds
its exclusive lock.

```
$ curl -X POST -H 'X-Blanket-Restart: 1' localhost:8773/ops/backup
{"path":"/var/lib/blanket/backups/blanket-1-2026-09-06T21-02-11.418Z.db","schemaVersion":1}
```

| Status | Meaning |
| ------ | ------- |
| 200 | Backup written; `path` is the file, `schemaVersion` the schema it holds. |
| 403 | Not from loopback, or the `X-Blanket-Restart` header is missing. |
| 500 | The backup failed — most often the free-space precheck refusing (see [upgrade.md](upgrade.md#the-free-space-precheck)). |

Backups land in `<database dir>/backups/` unless `?dir=` or the
`storage.backupDir` config key says otherwise, and the newest 3 are kept.
See [**upgrade.md**](upgrade.md) for naming, retention, the precheck
thresholds, and the `blanket backup` / `blanket migrate` CLI.

While a restart is in flight, this call also advances its record from
`STAGED` to `BACKED_UP` and stores the path it wrote. There is deliberately
no `POST /ops/restart/backed-up`: the server took the backup, so the record
can state a fact it observed instead of repeating a claim the caller made.

### The restart endpoints (turtlemonvh/blanket#23 phase 5)

One route per transition. The states, what each one means, and what happens
when a process dies in each of them are in
[**upgrade.md**](upgrade.md#the-restart-state-machine), along with the
manual `curl` recipe for a full restart; this section is the request and
response shapes.

Transitions are **forward-only**. Skipping ahead is normal — a routine
restart takes no backup, swaps no binary and drains nothing — but going
back, or repeating a state, is **409** with the current state in the body.
So is `begin` while a restart is already in flight, and so is any
transition when none is.

#### `GET /ops/restart/status`

```
$ curl -H 'X-Blanket-Restart: 1' localhost:8773/ops/restart/status
{
  "restart": {"state": "IDLE"},
  "active": false,
  "spawnPaused": false,
  "execMode": "auto",
  "resolvedExecMode": "exec",
  "drainMode": "auto",
  "drainTimeoutMs": 60000,
  "supervised": false,
  "instanceId": "6a9e2bab860f1bb515a3a6fe",
  "pid": 4213,
  "version": "blanket v0.3.0 (built …)"
}
```

`resolvedExecMode` is what `exec` will actually do, with `auto` already
decided — the question you cannot answer from the record alone on a server
whose flags you cannot see. It is behind the ops guard like the mutating
routes, unlike `GET /ops/status/`: the pid, the exec mode and the
supervision guess together are a map of how to interfere with this server.

#### `POST /ops/restart/begin`

```
$ curl -X POST -H 'X-Blanket-Restart: 1' -H 'Content-Type: application/json' \
    -d '{"reason": "upgrade to 0.4.0"}' localhost:8773/ops/restart/begin
{"restart":{"state":"STAGED","id":"6a9e…","reason":"upgrade to 0.4.0", …}}
```

The body is optional; `?reason=` and `?execMode=` do the same job for a
caller that would rather not send one. Fields:

| Field | Meaning |
| ----- | ------- |
| `reason` | Free text, echoed in the status and logged by the process that finds the record on boot. |
| `execMode` | `auto` \| `exec` \| `exit`, overriding the server's `--exec-mode` for this restart. Resolved and pinned now, so one attempt cannot answer the question two ways. |
| `deadlineSeconds` | How long the watchdog gives you between calls before aborting. Default `restart.deadline` (5m). Refreshed by every transition. |

Staging pauses nothing and stops nothing. It exists so that a crash from
here on is attributable, and so the reaper stands down (every worker is
about to look stale at once).

#### `POST /ops/restart/pause`

```
{"restart":{"state":"PAUSED", …},"spawnPaused":true}
```

From here on `POST /worker/` and `PUT /worker/:id/restart` answer **409**:

```
{"error":"a server restart is in flight; worker spawn is paused until it completes or is aborted",
 "restart":{"id":"6a9e…","state":"PAUSED"}}
```

409 rather than 503 because the request is not wrong and the server is not
broken — the resource is in a state that does not accept it, and that state
clears on its own. This is the window the pause exists for: a worker is
forked from `os.Executable()`, so between the binary swap and the server's
own replacement, spawning one would pair a new worker binary with an old
server.

#### `POST /ops/restart/swapped`

Records that the binary on disk is now the new one. The only state that is
purely the caller's word — the server has no way to tell that the file
behind `os.Executable()` is a different build, and guessing wrong in the
comfortable direction is exactly how a stale binary would get declared
swapped.

#### `POST /ops/restart/drain`

```
$ curl -X POST -H 'X-Blanket-Restart: 1' 'localhost:8773/ops/restart/drain'
{"restart":{"state":"DRAINING","drainedWorkers":2, …},
 "stopped":["6a9e…","6a9f…"],
 "stillRunning":[],
 "drained":true,
 "waitedMs":1043}
```

Stops every running worker and records a **respawn intent** on each, in one
database transaction with the state change. Workers that were already
stopped are left alone: one stopped before the restart began was stopped by
somebody else, for a reason this restart knows nothing about.

`?wait=false` returns as soon as that transaction commits. The default
waits up to `restart.drainTimeout` for the stopped processes to actually
exit, and either way answers **200** — a drain that timed out with
stragglers is information, not a failure. `drained` says whether everything
went, `stillRunning` names what did not.

Refused with **409** when `--drain-mode=never`.

#### `POST /ops/restart/exec`

```
{"restart":{"state":"EXECING", …},"execMode":"exec","exitCode":0,"pid":4213}
```

**202**, written and flushed *before* the shutdown starts, so the caller
can tell "the restart began" from "the connection broke". Then the server
runs the ordered teardown ([design.md](design.md#shutdown-sequence)) and
either re-execs in place (`exec` — same pid, starts answering again) or
exits with **75** (`exit` — `EX_TEMPFAIL`, so a `Restart=on-failure`
supervisor brings it back; exiting 0 is exactly what would leave it down).

**503** if this server has no running listener to restart, which only
happens to a router built without one.

#### `POST /ops/restart/abort`

```
{"aborted":{"state":"DRAINING", …},"respawned":true}
```

Clears the record from any state and lifts the pause. If the restart had
already drained, the workers it stopped are **brought back** — otherwise
aborting would leave the box worse off than the restart it is rescuing.
This is the same code path the deadline watchdog runs, which is the point:
the recovery a human triggers is the one every watchdog timeout has
exercised.

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
