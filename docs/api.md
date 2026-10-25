# API

The blanket server exposes a JSON REST API on the configured port
(default `8773`). The embedded HTMX UI, the CLI, and external clients all
use the same endpoints. Bodies are JSON unless noted. Nothing is
authenticated: every route is open to anything that can reach the port,
and the [ops endpoints](#ops-endpoints) are only guarded by loopback.

## Errors

Every `:id` route (task or worker) follows one rule: a malformed id, one
that fails to parse as an `objectid.ObjectId` (not valid hex, wrong
length), is a **400**; a well-formed id naming no record is a **404**.
A malformed id is a bug in the caller and a missing one may just mean
"already deleted"; neither is ever a 500. The DELETE routes follow the
rule too, so a script that doesn't care whether the record was already
gone can treat 404 as success.

Most JSON handlers report errors as `{"error": "<message>"}`; a few
(streaming/tail endpoints, the web UI) return plain text.

## Time formats

Every timestamp in a response is **unix seconds** as a JSON integer:
task fields (`createdTs`, `scheduledTs`, `nextFireTs`, `pausedTs`, ...),
worker fields (`lastHeardTs`, `pidStartTs`, ...), and the `ts` of a
[streamed event](#event-schema-state-log-result). Two exceptions, noted
where they occur: `blanket.combined.ndjson` records `ts` in
**milliseconds**, and the human-readable fire times (`GET
/schedule/describe`'s `next`, the `scheduleDescription` text) are
**RFC3339 in the server's local zone**.

Inputs take these forms; each field's section says which apply to it:

| Form | Example | Meaning |
| ---- | ------- | ------- |
| Go duration | `10m`, `90s`, `2h` | Relative to now. Units are `ns`, `us`, `ms`, `s`, `m`, `h`. |
| RFC3339 | `2026-09-05T08:00:00Z`, `2026-09-05T08:00:00-04:00` | Absolute; the offset is part of the value. |
| Unix seconds | `1780000000` | Absolute. |
| Date layout | `2006-01-02T15:04`, `2006-01-02`, `2006/01/02 15:04` | Absolute, read as UTC. List filters only. |

* `notBefore` (on submit and `PUT /task/:id/schedule`): a duration, an
  RFC3339 timestamp, or unix seconds, tried in that order; anything else
  is **400** with the parser's message.
* `createdAfter` / `createdBefore`: unix seconds, RFC3339, or a date
  layout. A value in none of those forms is ignored, not rejected.
* `?wait`: a duration, or a bare number of seconds (`30`, `2.5`).

## Tasks

User-facing endpoints: submit, list, inspect, cancel, follow logs. Each
has its own section below.

```
GET    /task/                   # list tasks; filterable via query string
GET    /task/:id                # fetch one task
POST   /task/                   # submit; ?wait blocks, &stream streams
DELETE /task/:id                # delete; kills a RUNNING task; 404 if missing
PUT    /task/:id/cancel         # -> STOPPED; RUNNING needs ?force=true
PUT    /task/:id/pause          # RECURRING -> PAUSED
PUT    /task/:id/resume         # PAUSED -> RECURRING
PUT    /task/:id/schedule       # change a series' cron or a one-shot's notBefore
GET    /task/:id/log            # follow a log file (SSE), or the event stream (NDJSON)
GET    /task/:id/log/tail       # last N lines of a log file
GET    /schedule/describe       # explain a cron expression
```

Every task response carries, besides the fields the caller set:
`exitCode` (see [Exit codes](#exit-codes-exitcode)), `requeueCount` (see
[Reaper-driven transitions](#reaper-driven-transitions)), and
`scheduleDescription` (see [Describe a schedule](#describe-a-schedule-get-scheduledescribe)).

### Submit a task: `POST /task/`

Accepts a JSON body, or a multipart form with a `data` field holding the
same JSON plus any files to place in the task's result dir. Returns
**201** with the task record as soon as it is queued.

| Field         | Required | Description |
| ------------- | -------- | ----------- |
| `type`        | yes      | Name of a loaded task type. |
| `environment` | if the type has required vars | Map of string → string, merged over the type's default env. |
| `notBefore`   | no       | Delays a one-shot task. Any absolute [time form](#time-formats), or a duration relative to now (`"10m"`). A future time starts the task in `SCHEDULED` instead of `WAITING`; the scheduler promotes it once due. Mutually exclusive with `cron`. |
| `cron`        | no       | A 5-field cron expression (minute hour dom month dow). Makes the task a `RECURRING` template that never runs itself: the scheduler spawns a child task (own id, log, and result dir; `parentId` set to the template) at each fire time. Mutually exclusive with `notBefore`. |

A bad `type` or `environment` is **400**. A future `notBefore` or a `cron`
submission is **429** when accepting it would bring the live
`SCHEDULED`+`RECURRING`+`PAUSED` count to `scheduler.maxScheduled`
(default `10000`) — see
[task_flow.md](task_flow.md#scan-limit-schedulermaxscheduled). The
scheduling state machine is in
[task_flow.md](task_flow.md#scheduling-notbefore-and-cron).

### Wait for a task to finish: `POST /task/?wait`

With `?wait` the same call blocks until the task reaches a terminal state
and answers with its outcome, output, and result artifact in one
response. Nothing else changes: same body, validation, uploads, queue and
worker. It is a query parameter rather than a second route so there is
nothing to keep in sync.

```
POST /task/?wait                 # block up to tasks.sync.defaultWait (30s)
POST /task/?wait=30s             # block up to 30 seconds
POST /task/?wait=30&fail_on_error=true
```

| Parameter | Value | Meaning |
| --------- | ----- | ------- |
| `wait` | absent | 201, return immediately. |
| `wait` | present, no value | Block for `tasks.sync.defaultWait`. |
| `wait` | a [duration](#time-formats) (`30s`, `2m`) or bare seconds (`30`) | Block for that long. Over `tasks.sync.maxWait` is **400**, never a silent clamp. |
| `fail_on_error` | absent / `false` | A failed task still answers **200**; its outcome is in `state` and `exitCode`. |
| `fail_on_error` | no value, or `true` | A non-`SUCCESS` terminal state answers **502** with the same body, so `curl --fail` notices. |

| Situation | Status | Body |
| --------- | ------ | ---- |
| Terminal state within the wait | **200** | The [completion payload](#the-completion-payload) |
| ...and `fail_on_error=true` with a non-`SUCCESS` state | **502** | The same payload |
| Wait expired, task still live | **504** | `{"id", "state", "waitOutcome": "wait_timeout", "pollUrl", "error"}`; the task keeps running, poll `pollUrl` |
| Bad `type`/`environment`, unparseable `wait`, `wait` over the cap | **400** | `{"error": "..."}` |
| Client disconnected | — | Nothing; the task keeps running and stays fetchable by id |

An HTTP status describes whether the API call worked; the task's own
outcome is in the body. `fail_on_error` is the opt-in for callers that
want the status to carry both.

Config keys (defaults shown):

```
tasks.sync.defaultWait     30s       # applied to a bare ?wait
tasks.sync.maxWait         300s      # hard cap; a larger ?wait is a 400
tasks.sync.maxLogLines     200       # lines of stdout and of stderr in the payload
tasks.sync.maxResultBytes  1048576   # cap on the result_file size that gets parsed
```

Caveats:

* A waiting call holds a connection and a goroutine open for an
  unauthenticated caller. `tasks.sync.maxWait` is the only control; lower
  it if the server is reachable beyond a trusted network.
* Workers poll, so even a 100ms task takes 2-4 seconds of wall clock.
* A worker that dies mid-task leaves it `CLAIMED`/`RUNNING` until the
  [reaper](task_flow.md#the-reaper) reconciles it, which takes minutes by
  design. A synchronous caller burns its wait budget and gets a 504 first.

### The completion payload

Returned by `POST /task/?wait`, and carried verbatim by the `result`
event of the streaming form.

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
| `task` | The task JSON, exactly as `GET /task/:id` returns it. |
| `waitOutcome` | `"completed"`, or `"wait_timeout"` in a stream (which cannot change its HTTP status once started). |
| `stdout` / `stderr` | The last `tasks.sync.maxLogLines` lines of `blanket.stdout.log` / `blanket.stderr.log` under the result dir; empty if the task wrote nothing. Two separate tails; their interleaving is in `blanket.combined.ndjson` ([Task output files](task_flow.md#task-output-files)), which this payload does not read. |
| `stdoutTruncated` / `stderrTruncated` | True when earlier lines were dropped from that tail. |
| `result` | The parsed contents of the type's [`result_file`](task_type_definitions.md#result_file), or `null` — no declared file, or a task that failed before writing it, both yield `null` without an error. |
| `resultError` | Why `result` is `null` despite a declared `result_file` (unparseable, oversized, unreadable); `null` when nothing was wrong. |

### Stream a task as it runs: `POST /task/?wait&stream`

Adding `stream` keeps the connection open and emits one event per update
while the task runs, ending with a terminal `result` event carrying the
completion payload. A client that reads to `result` never needs a second
fetch.

```
POST /task/?wait&stream          # events, then the completion payload
POST /task/?wait=60s&stream      # ...with a 60s budget
POST /task/?stream               # a stream implies a wait

curl -sN -X POST 'localhost:8773/task/?wait=60s&stream' -d '{"type": "echo_task"}'
```

A stream is always **200**: the status is sent before the outcome is
known, so `fail_on_error` has no effect and there is no 504. The `result`
event's `waitOutcome` (`completed` / `wait_timeout`) carries that
distinction; `task.state` / `task.exitCode` carry the task's outcome. A
`wait_timeout` means the budget expired with the task still running and
pollable by id. Disconnecting mid-stream changes nothing about the task.

### Event schema: `state`, `log`, `result`

Every event is a JSON object with `ts` ([unix seconds](#time-formats)),
`taskId`, and `type`:

| `type` | Extra fields |
| ------ | ------------ |
| `state` | `state`, and `previousState` — `null` on the first event, which reports the state the task was already in. |
| `log` | `stream` (`"stdout"` / `"stderr"`), `seq`, `line` (no trailing newline). |
| `result` | The completion payload's fields, flattened. |

```
{"ts":1756900000,"taskId":"68b4...","type":"state","state":"WAITING","previousState":null}
{"ts":1756900002,"taskId":"68b4...","type":"state","state":"CLAIMED","previousState":"WAITING"}
{"ts":1756900002,"taskId":"68b4...","type":"state","state":"RUNNING","previousState":"CLAIMED"}
{"ts":1756900002,"taskId":"68b4...","type":"log","stream":"stdout","seq":1,"line":"hello world"}
{"ts":1756900003,"taskId":"68b4...","type":"state","state":"SUCCESS","previousState":"RUNNING"}
{"ts":1756900003,"taskId":"68b4...","type":"result","waitOutcome":"completed","task":{...},"stdout":"hello world\n","stderr":"","result":null,"resultError":null}
```

* **`state` events are the debugging tool.** They tell "no worker was
  free" (still `WAITING` at timeout) from "the task is slow" (reached
  `RUNNING`). Since workers are hand-launched, the first case is common.
* **`log` delivery is best-effort and lags.** The worker flushes the log
  files on its own poll interval, and the stream cannot attach until the
  task reaches `CLAIMED` and the files exist. On attach, up to 500 lines
  already on disk replay as `log` events before any live one (the same
  cap as `GET /task/:id/log`). The `result` event repeats both tails, so
  a client that reads to the end always has the authoritative output.
* **`seq` is per stream, per connection**, counting from 1 straight
  through the seam between replayed history and live lines. It is not a
  file position; a reconnecting client sees it restart.
* **Ordering between `stdout` and `stderr` is not the task's.** The two
  files are tailed separately: history replays as all buffered `stdout`
  then all buffered `stderr`, and live lines interleave as they arrive.
  The real interleaving is recorded in `blanket.combined.ndjson`
  ([Task output files](task_flow.md#task-output-files)), which these
  events do not read.
* Skip unknown event types rather than erroring, so a later blanket can
  add one.

### Event framing: NDJSON or SSE

The JSON object is identical either way; one encoder produces both.

| `Accept` | Framing | `Content-Type` |
| -------- | ------- | -------------- |
| anything else (default) | One JSON object per line | `application/x-ndjson` |
| `text/event-stream` | SSE frames, `event:` set to the event's `type`, `data:` to the same JSON | `text/event-stream` |

NDJSON is the default because `curl`, the Go CLI and an agent's HTTP
client all parse it trivially; SSE is for browsers and matches
`GET /task/:id/log`.

### List and filter tasks: `GET /task/`

Returns a JSON array ordered by id, which embeds creation time: oldest
first unless `reverseSort=true`. Comma-separated values and repeated
parameters are both accepted for the list filters.

| Parameter | Meaning |
| --------- | ------- |
| `states` | Only these states, e.g. `states=RUNNING,ERROR`. |
| `types` | Only these task types. |
| `parentId` | Only the child runs of this `RECURRING`/`PAUSED`/cancelled template (ignored unless a valid id). |
| `requiredTags` / `maxTags` | Tasks whose tags include all of / are within this set. |
| `createdAfter` / `createdBefore` | Only tasks created after / before this [time](#time-formats) (absolute forms only); applied via the id's embedded timestamp. |
| `limit` / `offset` | Page size (default `500`) and offset. |
| `reverseSort=true` | Newest first. |
| `count=true` | Return only the matching count, as plain text. |

### Cancel a task: `PUT /task/:id/cancel`

Moves a `WAITING`, `SCHEDULED`, `RECURRING`, or `PAUSED` task to
`STOPPED` and keeps the record. A `RUNNING` task requires `?force=true`
and is otherwise **400**: the server flips the record and the worker
kills the process on its next poll (see
[task_flow.md](task_flow.md#cancel-a-running-task-forcetrue)). Any other
state (`CLAIMED`, or already terminal) is **501**. A cancelled template
never fires again; children it already spawned run to completion.

### Delete a task: `DELETE /task/:id`

Removes the record and its result dir, killing the task if it is running.
For a `RECURRING`/`PAUSED` template this removes the series outright;
`cancel` is the keep-the-record alternative. Returns **200**
`{"id": "..."}` once deleted, **404** if no task has that id (including
one already deleted), and **400** for a malformed id.

### Pause, resume, or reschedule a series

```
PUT /task/:id/pause             # RECURRING -> PAUSED; sets pausedTs
PUT /task/:id/resume            # PAUSED -> RECURRING; clears pausedTs,
                                 # recomputes nextFireTs from now
PUT /task/:id/schedule          # {"cron": "..."} on a RECURRING/PAUSED template,
                                 # {"notBefore": "..."} on a SCHEDULED one-shot
```

`pause` is **400** unless the task is `RECURRING`; `resume` is **400**
unless `PAUSED`; `schedule` is **400** on a state/body mismatch or an
invalid value, with the parser's message. Semantics are in
[task_flow.md](task_flow.md#pause-resume-cancel-or-reschedule-a-series).

### Describe a schedule: `GET /schedule/describe`

`GET /schedule/describe?cron=<expr>` validates a cron expression and
returns `{"cron": "<expr>", "description": "<friendly text>", "next":
[...]}` — the next three fire times, [RFC3339 in local
time](#time-formats) — or **400** with the parser's message if `cron` is
missing or invalid. The UI's
create form uses the HTML sibling, `/ui/partials/schedule-preview`.

Every task response also carries a computed `scheduleDescription`
(`"Every 5 minutes"`, `"Once, at 2026-09-05T08:00:00-04:00"`, or `""` for
an unscheduled task) — see
[task_flow.md](task_flow.md#friendly-schedule-text-scheduledescription).

### Follow or stream a task's log: `GET /task/:id/log`

Two shapes from one route, chosen by content negotiation:

| Request | Response |
| ------- | -------- |
| default | `text/event-stream`: raw lines of one log file as `event: message` frames. What the embedded UI consumes. |
| `Accept: application/x-ndjson`, or `?format=ndjson` (`?format=events` is a synonym) | `application/x-ndjson`: the `state` / `log` / `result` [events above](#event-schema-state-log-result), from the same encoder as `?wait&stream`. |

```bash
curl -sN 'localhost:8773/task/<id>/log?stream=stderr'
curl -sN -H 'Accept: application/x-ndjson' localhost:8773/task/<id>/log
```

Both shapes stay open while the task is live and replay history on
connect: up to 500 lines already on disk (`DEFAULT_LOG_TAIL_LINES`, the
same cap as `/log/tail`) — one file for the raw form, both files as `log`
events (stdout's block, then stderr's) for the structured form. Each
connection takes its own read and follows from the exact byte offset it
stopped at, so the seam neither drops nor repeats a line, and a second
connection to an already-open file sees the same window as the first.
The structured form has no wait budget: it ends with a `completed`
result event or when the client leaves.

### Tail a task's log: `GET /task/:id/log/tail`

Returns the last `n` lines (`?n=`, default 500) of one log file as
`text/plain`; an empty body if the file does not exist yet.

```bash
curl -s 'localhost:8773/task/<id>/log/tail?stream=stderr&n=50'
```

### Choose stdout or stderr: `?stream=`

The raw form of `GET /task/:id/log` and `GET /task/:id/log/tail` take
`?stream=stdout|stderr` naming which file under the result dir
(`blanket.stdout.log` / `blanket.stderr.log`) to read; omitted means
`stdout`. `?stream=both` is **400** on both: these emit the bytes the
task wrote and nothing else, so an interleaving would be ambiguous. For
both at once use the structured stream, whose `log` events carry a
`stream` field, or the UI-only
[combined log stream](#combined-log-stream-get-uissetasksidlog).

A third result-dir file, `blanket.combined.ndjson`, records how the two
streams interleaved. It is served like any result file
(`GET /results/:taskId/blanket.combined.ndjson`); its shape, its grace
window, and `workers.combinedLog` are in
[Task output files](task_flow.md#task-output-files).

## Worker-facing task endpoints

Used by `blanket worker` to advance task state. Unauthenticated like
everything else, so `exitCode` and the rest are spoofable by anything
that can reach the port.

```
POST   /task/claim/:workerid    # claim a task matching the worker's tags
PUT    /task/:id/run            # CLAIMED -> RUNNING
                                 # ?runId= &pid= &timeout= &typeDigest=
PUT    /task/:id/progress       # ?progress=0..100 [&runId=]
PUT    /task/:id/finish         # RUNNING -> SUCCESS / ERROR / TIMEDOUT
                                 # ?state= (required) &runId= &exitCode=
```

Which endpoint drives which transition is drawn in
[task_flow.md](task_flow.md#task-state-machine); the on-disk journal the
worker keeps alongside these calls is its
[Outcome journal](task_flow.md#outcome-journal).

### Claim a task: `POST /task/claim/:workerid`

Answers **200** with the claimed task JSON (now `CLAIMED`, `workerId`
set), **204** when nothing in the queue matches the worker's tags, or
**404** when the queue entry's task was stopped or deleted meanwhile (the
entry is acked away; the worker simply tries again).

### Run, progress, and finish

`run` records `pid`, `timeout`, `typeDigest` and `runId`. `progress` is
**400** unless the value is an integer 0-100 and the task is `RUNNING`.
`finish` takes its terminal `state` as a query parameter (anything but
`SUCCESS`/`ERROR`/`TIMEDOUT`/`STOPPED` is **400**), plus the optional
`runId` and `exitCode` below. All three are 404 for a missing task.

### Exit codes: `?exitCode=`

`PUT /task/:id/finish?exitCode=<int>` records the process exit status on
the task's `exitCode` field, which every task response carries:

* an integer once a process ran to completion (`0`, `3` for `exit 3`, ...);
* `null` when there is no exit status of the task's own — not finished
  yet, killed by a signal (`STOPPED`, `TIMEDOUT`), or never started.
  Nullable so "unknown" stays distinguishable from a genuine exit 0.

A present-but-unparseable `exitCode` is **400**. An omitted one leaves
the stored value alone, so a cancel, or a retry of a finish that already
landed, cannot erase a recorded code. Tasks recorded before v0.3.0 have
no stored code and read as `null`.

### `runId`: the fencing token for one run

`runId` identifies one execution attempt. The
worker generates it just before starting the child process and sends it
on every transition for that run; a task script can read it from
`BLANKET_APP_TASK_RUN_ID`. `run` and `finish` are idempotent with respect
to it, because the worker retries them:

| Request | Task state | Stored `runId` | Sent `runId` | Response |
| --- | --- | --- | --- | --- |
| `run` | `CLAIMED` | — | `R1` | **200**, token stored |
| `run` | `RUNNING` | `R1` | `R1` | **200**, no-op (a retry) |
| `run` | `RUNNING` | `R1` | `R2` | **409** — two runners |
| `run` | `RUNNING` | `R1` | *(empty)* | **200**, no-op (no token sent) |
| `run` | terminal | any | any | **409** — e.g. a user already stopped it |
| `finish` | `RUNNING` | `R1` | `R1` | **200**, task becomes terminal |
| `finish` | terminal | `R1` | `R1` | **200**, no-op — first terminal state wins |
| `finish` | `RUNNING` | `R1` | `R2` | **409** — two runners |
| `finish` | `RUNNING` | `R1` | *(empty)* | **200** (no token sent) |
| `finish` | `CLAIMED` | any | any | **400** — never started |

* **An empty `runId` is accepted.** Only two tokens that are both
  present and different conflict, so a worker from before v0.3.0, which
  sends none, keeps working in a mixed-version install.
* **The first terminal state wins**, and a repeat is 200. A user's
  `STOPPED` (via `cancel`) beats the `ERROR` the worker reports when the
  killed child exits, and the 200 is what tells the worker to stop
  retrying; a rejection would have it retry until it gave up.
* `progress` takes the token too and is **409** on a mismatch, so a stale
  runner cannot paint over the live one's numbers; empty is accepted.

A 409 is never retried by the worker; 5xx and transport failures are,
with full-jitter backoff (`lib/httpx`).

### Reaper-driven transitions

Two transitions have no worker behind them. The
[reaper](task_flow.md#the-reaper) applies a recovered outcome through
the same `finish` endpoint, carrying the journal's `runId`, so it is
indistinguishable from a late report by the worker itself. And it
requeues a `CLAIMED` task whose worker died before the command started:
back to `WAITING` with `workerId` and `runId` cleared and `requeueCount`
incremented; once that count reaches `reaper.maxRequeues` the task is
failed to `ERROR` instead.

## Task types

Read-only; loaded from TOML files at startup. Schema in
[task_type_definitions.md](task_type_definitions.md). `description` and
`documentation` are included like any other key.

```
GET /task_type/                 # list all loaded task types
GET /task_type/:name            # fetch one by name
```

## Workers

```
GET    /worker/                 # list workers
GET    /worker/:id              # fetch one
GET    /worker/:id/log          # SSE stream of the worker's logfile
GET    /worker/:id/log/tail     # last N lines of it (?n=, default 500)
GET    /worker/:id/logs         # full logfile download
POST   /worker/                 # launch a new worker (used by the UI)
PUT    /worker/:id              # registration + status updates from the worker
PUT    /worker/:id/heartbeat    # liveness ping; no body
PUT    /worker/:id/stop         # stop after the current task
PUT    /worker/:id/restart      # relaunch a stopped worker
DELETE /worker/:id              # remove from the DB; 400 unless stopped, 404 if missing
```

The worker state machine is in
[task_flow.md](task_flow.md#worker-state-machine).

### Launch, update, restart, delete a worker

`POST /worker/` takes a worker config JSON (`tags`, `checkInterval`, ...)
and forks a daemon from the server's own binary; **200** with the
registered record once it has reported a pid, **400** for a
`checkInterval` under the minimum (0.5s), **408** if it never registers.
`PUT /worker/:id/restart` clears the server-side `stopped` flag, then
relaunches the same way. Both answer **409** while a server restart holds
the spawn pause (see [`POST /ops/restart/pause`](#post-opsrestartpause)).

`PUT /worker/:id` merges at the field level: the worker owns `pid`,
`pidStartTs`, `logfile`, `startedTs`, `tags`, `checkInterval`; the server
owns `stopped`, `stoppedReason`, `lastHeardTs`, `lost`, and the respawn
bookkeeping (`respawnIntent`, `respawnAttempts`, `lastRespawnTs`). A
re-registration clears `lost`, the one server-owned field it may touch.

`DELETE /worker/:id` takes no body. It is **400** unless the stored
record is stopped, **404** if the worker is missing, and **200**
`{"id": ...}` once deleted.

Worker records carry `pidStartTs` (makes the pid usable as a liveness
signal despite pid reuse), `lastHeardTs`, `lost` (the reaper believes it
gone; a UI badge and nothing more), `stoppedReason`, and the respawn
fields. The [reaper](task_flow.md#the-reaper) explains the first three;
[respawn intent](task_flow.md#respawn-intent-and-stop-reasons) the rest.

### Heartbeat: `PUT /worker/:id/heartbeat`

Takes **no body** and answers:

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
| `stopped` | The worker's server-side flag right now. A worker that reads `true` exits after its current task, so a drain lands within one check interval. |
| `lastHeardTs` | The timestamp the server just recorded, **from its own clock**. There is no way to supply one: every reaper decision is arithmetic on this number, and a worker-supplied value would measure clock skew. |
| `serverInstanceId` | Identifies the server *process*; a change means it restarted since the last heartbeat. |
| `serverStartedTs` | When that process started serving. |

**404** for a worker not in the database — ordinary when an operator
deletes a worker whose process is still winding down. The call also
clears the worker's `lost` flag.

### Stop a worker: `PUT /worker/:id/stop`

```
PUT /worker/:id/stop?force=true&reason=<who and why>
```

Sets `stopped`; the worker exits after its current task. `force=true`
also sends the process an immediate kill signal. `reason` attributes the
stop and decides the fate of a pending respawn intent: a worker's own
shutdown handler sends
`reason=worker-shutdown`, which **preserves** the intent (a drained worker
exiting is the drain working); every other stop, including an
unattributed one from an operator or the UI, **clears** it. See
[task_flow.md](task_flow.md#respawn-intent-and-stop-reasons).

## Server

```
GET  /                          # redirects to the web UI
GET  /version                   # build info as JSON
GET  /config/                   # processed server config
GET  /ops/status/               # runtime metrics (goroutines, memory, etc.)
GET  /results/<taskId>/<file>   # browse a task's result dir
```

`GET /config/` returns every resolved config key plus three process
properties: `basepath` (the directory the binary was resolved from),
`instanceId` and `serverStartedTs` — the same pair the
[heartbeat](#heartbeat-put-workeridheartbeat) carries. A changed
`instanceId` is how a client tells a restart from a slow request.

## Ops endpoints

**Privileged, local-only** endpoints for operating the install: a
backup, and the restart state machine.

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

### The ops guard: loopback and `X-Blanket-Restart`

Every mutating `/ops/` route, and `GET /ops/restart/status`, requires
**both**:

- a **loopback** client address, decided from the connection's
  `RemoteAddr` and never from `X-Forwarded-For`; and
- an **`X-Blanket-Restart`** header. Its value is not checked and is not
  a secret: it forces a browser into a CORS preflight, which a
  cross-origin POST with only "simple" headers would skip.

`/ops/` is carved out of the wildcard CORS handler that covers the rest
of the API, so that preflight is refused rather than approved. Missing
either requirement is **403** with `{"error": "..."}`. `curl`, scripts
and the CLI set the header and connect from loopback, so they are
unaffected. There is no auth token yet: loopback-only covers the
single-machine install, the only deployment shape blanket has.

### Back up the database: `POST /ops/backup`

A point-in-time copy of the database **without pausing anything**:
BoltDB's MVCC makes a read transaction a consistent image of the file. It
is the only way to back up a live server, since the CLI cannot open the
database while the server holds its lock.

```
$ curl -X POST -H 'X-Blanket-Restart: 1' localhost:8773/ops/backup
{"path":"/var/lib/blanket/backups/blanket-1-2026-09-06T21-02-11.418Z.db","schemaVersion":1}
```

| Status | Meaning |
| ------ | ------- |
| 200 | Backup written; `path` is the file, `schemaVersion` the schema it holds. |
| 403 | Not from loopback, or the header is missing. |
| 500 | The backup failed — most often the [free-space precheck](upgrade.md#the-free-space-precheck). |

Backups land in `<database dir>/backups/` unless `?dir=` or
`storage.backupDir` says otherwise; the newest 3 are kept. Naming,
retention, thresholds and the `blanket backup` / `blanket migrate` CLI
are in [upgrade.md](upgrade.md). While a restart is in flight this call
also advances its record from `STAGED` to `BACKED_UP` and stores the path;
there is deliberately no `POST /ops/restart/backed-up`, since the server
took the backup and can record a fact it observed.

### Restart endpoints

One route per transition. The states,
what a crash in each means, and the manual `curl` recipe are in
[upgrade.md](upgrade.md#the-restart-state-machine); this is the request
and response shapes. Transitions are **forward-only**: skipping ahead is
normal (a routine restart takes no backup, swaps nothing, drains nothing),
but going back or repeating a state is **409** with the current state in
the body — as is `begin` while a restart is in flight, and any other
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
decided. It is behind the ops guard, unlike `GET /ops/status/`: the pid,
the exec mode and the supervision guess are a map of how to interfere
with this server.

#### `POST /ops/restart/begin`

```
$ curl -X POST -H 'X-Blanket-Restart: 1' -H 'Content-Type: application/json' \
    -d '{"reason": "upgrade to 0.4.0"}' localhost:8773/ops/restart/begin
{"restart":{"state":"STAGED","id":"6a9e…","reason":"upgrade to 0.4.0", …}}
```

The body is optional; `?reason=` and `?execMode=` do the same job.

| Field | Meaning |
| ----- | ------- |
| `reason` | Free text, echoed in the status and logged by the process that finds the record on boot. |
| `execMode` | `auto` \| `exec` \| `exit`, overriding the server's `--exec-mode` for this restart. Resolved and pinned now. |
| `deadlineSeconds` | How long the watchdog allows between calls before aborting. Default `restart.deadline` (5m); refreshed by every transition. |

Staging pauses and stops nothing. It makes a crash from here on
attributable, and stands the reaper down.

#### `POST /ops/restart/pause`

```
{"restart":{"state":"PAUSED", …},"spawnPaused":true}
```

From here on `POST /worker/` and `PUT /worker/:id/restart` answer **409**
with `{"error": "a server restart is in flight; worker spawn is paused
until it completes or is aborted", "restart": {"id": "…", "state":
"PAUSED"}}` — 409 rather than 503 because the request is not wrong and
the state clears on its own. A worker is forked from `os.Executable()`,
so between the binary swap and the server's own replacement, spawning
one would pair a new worker binary with an old server.

#### `POST /ops/restart/swapped`

Records that the binary on disk is now the new one. The only state that
is purely the caller's word: the server cannot tell that the file behind
`os.Executable()` is a different build.

#### `POST /ops/restart/drain`

```
$ curl -X POST -H 'X-Blanket-Restart: 1' 'localhost:8773/ops/restart/drain'
{"restart":{"state":"DRAINING","drainedWorkers":2, …},
 "stopped":["6a9e…","6a9f…"],
 "stillRunning":[],
 "drained":true,
 "waitedMs":1043}
```

Stops every running worker and records a **respawn intent** on each, in
one database transaction with the state change. Workers already stopped
are left alone: somebody else stopped them, for a reason this restart
knows nothing about. The default waits up to `restart.drainTimeout` for
the processes to exit; `?wait=false` returns as soon as the transaction
commits. Either way **200** — a drain that timed out with stragglers is
information, not a failure: `drained` says whether everything went,
`stillRunning` names what did not. **409** when `--drain-mode=never`.

#### `POST /ops/restart/exec`

```
{"restart":{"state":"EXECING", …},"execMode":"exec","exitCode":0,"pid":4213}
```

**202**, written and flushed *before* the shutdown starts, so the caller
can tell "the restart began" from "the connection broke". The server then
runs the ordered teardown ([design.md](design.md#shutdown-sequence)) and
either re-execs in place (`exec`: same pid, starts answering again) or
exits with **75** (`exit`: `EX_TEMPFAIL`, so a `Restart=on-failure`
supervisor brings it back). **503** if this server has no running
listener to restart, which only happens to a router built without one.

#### `POST /ops/restart/abort`

```
{"aborted":{"state":"DRAINING", …},"respawned":true}
```

Clears the record from any state and lifts the pause. If the restart had
already drained, the workers it stopped are **brought back**. This is the
same code path the deadline watchdog runs, so the recovery a human
triggers is the one every watchdog timeout has exercised.

## Streaming endpoints (SSE)

Five routes hold a `text/event-stream` connection open:

```
GET /task/:id/log               # one log file of a task, line by line
                                 # (`message` events); ?stream=stderr
GET /worker/:id/log             # a worker's logfile, same shape
GET /ui/sse/tasks               # `tasks-changed`: a nudge to re-fetch, no payload
GET /ui/sse/workers             # `workers-changed`, likewise
GET /ui/sse/tasks/:id/log       # UI-only: both log files, pre-rendered HTML
```

### The `server-restarting` event

All five emit a **`server-restarting`** event, and only that event, as
their final frame when the server is shutting down or restarting. It is
preceded by a bare `retry:` field (1000 ms), and the stream closes:

```
retry: 1000

event:server-restarting
data:the server is shutting down; reconnecting
```

Treat it as "reconnect shortly", not an error. The embedded UI raises a
banner and clears it when a stream reconnects
(`server/ui/static/sse-restart-banner.js`); an `EventSource` reconnects
on its own. It exists because `net/http`'s `Shutdown` waits indefinitely
on an active connection, so a stream that never returned would hang
every restart — see [design.md](design.md#shutdown-sequence).

## Web UI

`/ui/*` serves the embedded HTMX UI: pages, static assets, SSE streams,
and the `/ui/partials/*` fragments the pages swap in. These return HTML
and are an implementation detail of the UI, not a client API; see
[usage.md](usage.md#web-ui) for the pages and `server/server.go` for the
full route list. The ones below are worth knowing because browsers fetch
them on their own or because they mirror JSON endpoints.

### UI partials that mirror JSON endpoints

```
GET /ui/manifest.webmanifest    # PWA manifest (application/manifest+json):
                                # start_url and scope /ui/, display standalone,
                                # 192/512/maskable icons under /ui/static/;
                                # `name` is ui.instanceName, else the hostname.
                                # No service worker is served.
GET /ui/partials/schedule-preview?cron=<expr>
                                # live rendering of a cron field: description
                                # plus the next three fire times, or the
                                # parser's message inline. Always 200 — htmx
                                # only swaps 2xx. JSON: GET /schedule/describe.
GET /ui/partials/form-error?error=<message>
                                # renders one rejected-submit message into the
                                # new-task form. htmx never swaps a 4xx body, so
                                # POST /ui/tasks returns the message on an
                                # HX-Trigger event and the form fetches this.
GET /ui/partials/task-log?id=<task id>&stream=stdout|stderr|both
                                # the task detail page's log pane and toggle: an
                                # SSE-connected <pre> for a running task, the
                                # stored tail otherwise. An unrecognized `stream`
                                # falls back to stdout rather than erroring.
```

`POST /ui/tasks` (the create form) accepts the same scheduling inputs as
`POST /task/`, as form fields: `scheduleMode` (`once` / `repeating`)
selects which of `notBefore` / `cron` applies, `notBeforeISO` (an
[RFC3339](#time-formats) instant resolved in the browser's timezone)
takes precedence over a bare `datetime-local` `notBefore` (read in the
server's zone), and an already-past start time is rejected rather than
queued immediately.

### Combined log stream: `GET /ui/sse/tasks/:id/log`

The log pane's "both" view: stdout and stderr of one task on one
connection, each `message` event carrying one HTML-escaped line in a span
with its stream badge. On connect it replays the last 500 lines and then
follows from the byte offset the replay stopped at. It reads
`blanket.combined.ndjson`, so replayed history is in the same time order
as live lines; a task with no such record (recorded with
`workers.combinedLog = false`, or run before v0.3.0), and output from
a process the task orphaned, fall back to the two per-stream files
grouped — all of stdout, then all of stderr. UI-only on purpose:
`GET /task/:id/log` emits the bytes the task wrote and must not start
emitting markup; clients wanting both streams as data use the structured
NDJSON stream.

### Series actions: `PUT /ui/series/:id/*`

```
PUT /ui/series/:id/pause        # -> PAUSED
PUT /ui/series/:id/resume       # -> RECURRING
PUT /ui/series/:id/cancel       # -> STOPPED (record kept)
PUT /ui/series/:id/schedule     # form field cron=<expr>; the shared editor also
                                # submits scheduleMode, and anything but
                                # `repeating` is rejected inline
```

Thin wrappers over the same functions
`PUT /task/:id/{pause,resume,cancel,schedule}` call, returning the
re-rendered schedule block with the parser's message inline on rejection
(the JSON endpoints return `{}` or an error string, which htmx cannot
place without a second round trip). Non-browser clients use `/task/`.

## MCP

```
ANY /mcp                        # MCP streamable-HTTP endpoint (JSON-RPC 2.0)
```

Mounted when `mcp.enabled` is true (default). See [mcp.md](mcp.md) for
the tool list, setup, and the security posture of the default
(all-interfaces, `mcp.mode = "all"`) configuration.
