# Usage

Worked examples for submitting, scheduling and managing tasks from the
REST API, the CLI and the web UI. See [installation](install.md) to get
set up, [api.md](api.md) for every endpoint, and
[task_flow.md](task_flow.md) for the state machines.

## Starting the server

After running the install script, start blanket with no arguments; it
reads the config from the install location:

```bash
blanket
```

The web UI is at [http://localhost:8773/](http://localhost:8773/).
Override the port with `-p 9000` or in the config file; `--logLevel debug`
gives verbose output. To run a custom config explicitly:

```bash
blanket --config /path/to/config.json
```

## Submitting tasks

### Via REST

```bash
# Minimal — uses the type's defaults
curl -s -X POST localhost:8773/task/ -d '{"type": "echo_task"}'

# With env vars
curl -s -X POST localhost:8773/task/ \
    -d '{"type": "bash_task", "environment": {"DEFAULT_COMMAND": "echo $(date)"}}'

# Run an arbitrary command
curl -s -X POST localhost:8773/task/ \
    -d '{"type": "bash_task", "environment": {"DEFAULT_COMMAND": "cd ~ && ls -lah"}}'

# python_hello — shells out to python3
curl -s -X POST localhost:8773/task/ \
    -d '{"type": "python_hello", "environment": {"NAME": "blanket"}}'
```

### Via CLI

```bash
# Submit, print the full record
$ blanket submit -t echo_task -e '{"GREETING": "hi"}'
echo_task 69ded2acce42aa8a11ac9ddc [1744748400]

# Submit, print only the task id
$ blanket submit -t echo_task -e '{"GREETING": "hi"}' -q
69ded2adce42aa8a11ac9de0
```

### Wait for the result: `POST /task/?wait`

Submitting normally returns as soon as the task is queued. `?wait` blocks
until the task reaches a terminal state and returns its state, exit code,
output, and parsed result artifact in one response.

```bash
# Block up to the default 30s
curl -s -X POST 'localhost:8773/task/?wait' -d '{"type": "echo_task"}'

# Block up to 60s, and let curl --fail notice a failed task (502)
curl -sf -X POST 'localhost:8773/task/?wait=60s&fail_on_error=true' \
    -d '{"type": "echo_task"}'

# Just the exit code
curl -s -X POST 'localhost:8773/task/?wait=60s' \
    -d '{"type": "echo_task"}' | jq -r '.task.exitCode'
```

If the wait runs out the response is a **504** carrying the task id and a
url to poll; the task keeps running. A wait longer than
`tasks.sync.maxWait` (default 300s) is a 400, not silently clamped. A task
type can declare a [`result_file`](task_type_definitions.md#result_file)
to have a JSON artifact parsed into the response's `result` field. The
full payload, response table and `tasks.sync.*` keys are in
[api.md](api.md#wait-for-a-task-to-finish-post-taskwait).

Two caveats: workers poll for work, so even a trivial task takes roughly
2-4 seconds of wall clock; and because the wait holds a connection open
on an unauthenticated endpoint, lower `tasks.sync.maxWait` if the server
is reachable beyond a trusted network.

### Run a task from the CLI: `blanket submit --wait` / `--follow`

`blanket submit --wait` turns submit into "run this and tell me how it
went": it blocks, prints the completion payload, and **exits with the
task's own exit code**, which makes it usable in a pipeline.

```bash
# Block, print the payload as JSON, exit with the task's exit code
$ blanket submit -t echo_task --wait
{
  "task": { "id": "68b4...", "state": "SUCCESS", "exitCode": 0, ... },
  "waitOutcome": "completed",
  "stdout": "hello world\n",
  ...
}

# The point of mirroring the exit code
$ blanket submit -t deploy --wait && echo "deployed"

# Watch it run: task stdout on stdout, task stderr on stderr
$ blanket submit -t long_build --follow
building...
warning: deprecated flag          # this one went to stderr
blanket: task 68b4... finished: state=SUCCESS exitCode=0

# Just the id, and the exit code
$ blanket submit -t echo_task --wait -q
68b4c1f2a3e4d5b6c7a8f9e0

# Give it longer than the server's default
$ blanket submit -t slow_task --wait --wait-timeout 120s
```

| Flag | Meaning |
| ---- | ------- |
| `--wait` | Block until the task finishes; print the completion payload as JSON. |
| `--follow` / `-f` | Implies `--wait`. Consumes the NDJSON event stream and writes the task's output to the matching local stream as it arrives; prints a one-line summary to **stderr** at the end instead of the payload. |
| `--wait-timeout` | How long to block — a duration (`"90s"`) or a number of seconds. Defaults to the server's `tasks.sync.defaultWait`; over `tasks.sync.maxWait` is rejected. |
| `-q` | With either: print only the task id, and no summary. The exit code is unaffected. |

Exit codes:

| Situation | Exit code |
| --------- | --------- |
| Task finished | The task's own `exitCode` (`0` for a clean run, `3` for `exit 3`, ...) |
| Task finished with no exit code of its own (killed by a signal, `TIMEDOUT`, never started) | `0` if `SUCCESS`, else `1` |
| The wait expired with the task still running | `124`, the code `timeout(1)` uses for the same thing. The task keeps running; its id is on stderr. |
| The submission itself failed (bad type, server unreachable) | `1` |

A failed API call (bad type, a malformed field, ...) prints
`error: <status> <message>` on stderr, using the server's own error text.
`blanket ps` and `blanket rm` follow the same convention.

`--follow` preserves the stdout/stderr split, so
`blanket submit -t x --follow 2>/dev/null` behaves the way it would for
the underlying command; ordering *between* the two streams is
best-effort, since blanket tails two separate files. `--wait`/`--follow`
can't be combined with `--not-before`/`--cron` — there'd be nothing to
wait for.

The same stream is available over HTTP as `POST /task/?wait&stream`
(NDJSON by default, SSE with `Accept: text/event-stream`), and
`GET /task/:id/log` serves it for an already-running task with
`Accept: application/x-ndjson`; see
[api.md](api.md#stream-a-task-as-it-runs-post-taskwaitstream) for the
event schema.

## Scheduling tasks

Delay a one-shot task, or make it recurring, with `notBefore` / `cron`
(REST) or `--not-before` / `--cron` (CLI) — mutually exclusive. See
[task_flow.md](task_flow.md#scheduling-notbefore-and-cron) for the state
machine and [api.md](api.md#time-formats) for the time formats
`notBefore` accepts.

```bash
# Run in 10 minutes
curl -s -X POST localhost:8773/task/ \
    -d '{"type": "echo_task", "notBefore": "10m"}'
$ blanket submit -t echo_task --not-before 10m

# Run at a specific time (RFC3339)
$ blanket submit -t echo_task --not-before 2026-09-05T08:00:00Z

# Recurring: fire every 5 minutes. Each fire spawns its own child task
# with its own log/result dir; the submitted task itself is a template
# and never runs.
curl -s -X POST localhost:8773/task/ \
    -d '{"type": "echo_task", "cron": "*/5 * * * *"}'
$ blanket submit -t echo_task --cron "*/5 * * * *"

# Stop a recurring task for good: cancel it like any other task. The
# record stays (STOPPED in `blanket ps` / GET /task/:id) and children
# already spawned run to completion.
curl -s -X PUT localhost:8773/task/<template-task-id>/cancel
# Or remove the record outright:
$ blanket rm <template-task-id>

# Pause a recurring task (it stops firing; the record shows PAUSED) and
# resume it later. REST only; there's no CLI flag for these:
curl -s -X PUT localhost:8773/task/<template-task-id>/pause
curl -s -X PUT localhost:8773/task/<template-task-id>/resume

# Change a live series' schedule without resubmitting it:
curl -s -X PUT localhost:8773/task/<template-task-id>/schedule \
    -d '{"cron": "0 * * * *"}'
curl -s -X PUT localhost:8773/task/<scheduled-task-id>/schedule \
    -d '{"notBefore": "2h"}'

# Preview a cron expression's friendly description and next fire times
# (what the create form's live preview calls):
curl -s "localhost:8773/schedule/describe?cron=*/5+*+*+*+*" | jq .

# List a series' past runs (its spawned children):
curl -s "localhost:8773/task/?parentId=<template-task-id>" | jq .
```

Every task response includes a `scheduleDescription` field, a friendly
rendering of its schedule (`"Every 5 minutes"`, `"Once, at
2026-09-05T08:00:00-04:00"`), shown in the `SCHEDULE` column of `blanket
ps`; see
[task_flow.md](task_flow.md#friendly-schedule-text-scheduledescription).
`POST /task/` returns 429 once the
[`scheduler.maxScheduled`](task_flow.md#scan-limit-schedulermaxscheduled)
cap is hit.

### Schedule a task from the web UI

The **New** button on the tasks page opens the create form. Tick
**Schedule task?** to reveal the scheduling section (collapsed by default,
so an unscheduled submit is unchanged):

- **One time** takes a start time in a date/time picker. The task waits in
  state `SCHEDULED` until then. The time is read in your browser's
  timezone, and a time already in the past is rejected — unlike the REST
  field, where a past `notBefore` means "run now".
- **Repeating** takes a 5-field cron expression and shows a live
  human-readable reading of it as you type ("At 02:00 PM, only on
  Tuesday") plus the next three fire times, or the parser's complaint. The
  submitted task becomes a `RECURRING` template.

On submit the flash message names the state and the friendly schedule
(e.g. `RECURRING - Every 5 minutes`), since a scheduled task doesn't start
running the way an ordinary submission does.

## Web UI

Everything on this page is also doable in the browser at
[http://localhost:8773/ui/](http://localhost:8773/ui/), a server-rendered
htmx UI baked into the binary.

| Page | What it's for |
| ---- | ------------- |
| **Tasks** (`/ui/`) | Every task record: filter by state/type/tags/date, submit a new one, cancel or delete. An **Exit** column shows each finished task's exit code. |
| **Upcoming** (`/ui/upcoming`) | What hasn't run yet — see below. |
| **Workers** (`/ui/workers`) | Launch, stop, restart workers; tail their logs. |
| **Task Types** (`/ui/task-types`) | The loaded TOML types and their settings. |
| **Docs** (`/ui/docs`) | These docs, as built into the binary, with search — readable without GitHub access. |
| **About** (`/ui/about`) | Version, config file, effective settings. |

The header shows which blanket you're looking at: the machine's hostname
by default, or whatever you set as `ui.instanceName`:

```
ui.instanceName  ""   # header + installed-app name; empty = hostname
```

### On a phone

Below 640px wide the lists turn into cards — each task leads with its
state and how long it has been queued, running, or ran for, then its type
and short id, then one line of context (the worker it's on, or its exit
code and finish time) — and the top links move to a tab bar at the bottom
(**Tasks**, **Upcoming**, **Workers**, **More**). Tap a card to open it;
the task page has a **Log ↓** button. Durations are computed by the server
and refresh whenever the list does (on a desktop the same duration is the
**Duration** column and a row on the task page). Cancel and Delete ask
first, on a phone or not.

To install it as an app, open blanket in the phone's browser and use
**Add to Home Screen** (Safari: the share menu; Chrome: the ⋮ menu). The
installed app opens full-screen and is named after the instance. Browsers
only offer this over HTTPS — see [Exposing blanket with
Tailscale](examples/tailscale.md) for the recommended way to get a real
certificate. There's no offline mode: everything blanket shows is live.

### Task detail

`/ui/tasks/<id>` is one task's page: its metadata, its environment, its
result artifact, and its log.

- **Exit code** sits next to the state, and also has its own column in the
  Tasks list. A dash means *no exit status*, not zero: a task that hasn't
  finished, one killed by a signal (`STOPPED` / `TIMEDOUT`), and one that
  never started all report none. Only a `0` badge means the process
  exited 0.
- **Result** appears only when the task's type declares a
  [`result_file`](task_type_definitions.md#result_file). It shows the
  parsed artifact as pretty-printed JSON (collapsed behind a disclosure if
  it's long) next to a link to the raw file, and says so instead if the
  file couldn't be parsed or the finished task never wrote it. It is read
  through the same code path as `POST /task/?wait`'s `result` /
  `resultError` fields, so the page and the API always agree.
- **Log** has a **stdout / stderr / both** toggle; `stdout` is the default.
  Each view shows the last 500 lines it covers, replayed before live
  lines follow, oldest first. `both` mixes the two streams with a
  per-line badge **in the order the task produced them**, read from the
  worker's [`blanket.combined.ndjson`](task_flow.md#task-output-files)
  (linked from the metadata table). A task without one — run before
  v0.3.0, or by a worker with `workers.combinedLog = false` — shows its
  history grouped by stream instead and says so above the pane. That
  record stops a fraction of a second after the task exits, so output
  from a process the task left running appears under `stdout` / `stderr`
  but not `both`. **Pin to bottom** keeps the newest line in view once
  the log outgrows its pane. The worker page's log behaves the same way.

### Upcoming

`/ui/upcoming` is the "what's queued on a schedule" view, split into the
two things that behave differently:

- **One-time** — tasks in state `SCHEDULED` (submitted with `notBefore`).
  Each shows the time it will run and a friendly description, and can be
  cancelled straight from the list.
- **Series** — live (`RECURRING`) and `PAUSED` templates. Each shows its
  friendly schedule, the raw cron expression, the next fire time (or when
  it was paused), a status badge, and a link to its detail page. Paused
  series stay listed; cancelled ones drop off, though their record and
  their past runs remain reachable at `/ui/tasks/<id>`.

### Series detail

A recurring template's own `/ui/tasks/<id>` page is the series view. From
it you can:

- read the schedule in plain English alongside the raw cron expression and
  the next fire time;
- **pause** it (the page then shows when it was paused) and **resume** it,
  which recomputes the next fire from now;
- **change the schedule** — the same cron editor the create form uses,
  pre-filled with the current expression, with a live preview of what a
  new one means and its next three fire times (a series always repeats,
  so switching it to "One time" is refused);
- **cancel** it for good, which stops it firing but keeps the record and
  its history (`DELETE`, via the Tasks page, removes the record instead);
- browse **past runs** — every task the series has spawned, in the same
  table as the main Tasks list.

A task spawned by a series carries a `parentId`, and the UI links back:
its detail page opens with a card naming the series, its schedule, and
whether that series is live, paused, or cancelled, and its row in the
Tasks list carries a compact "part of series …" link.

## File uploads

Attach files to a task; they're placed in the task's working directory
before the command runs.

```bash
# Send task spec as a JSON form field, attach files alongside
curl -X POST localhost:8773/task/ \
    -F data='{"type": "echo_task", "environment": {"GREETING": "hi"}}' \
    -F input.txt=@input.txt

# Or send the task spec as a file too
cat > data.json <<'EOF'
{
    "type": "echo_task",
    "environment": {
        "GREETING": "hi"
    }
}
EOF
curl -X POST localhost:8773/task/ \
    -F data=@data.json \
    -F input.txt=@input.txt
```

### Submitting many tasks

```bash
while true; do
    curl -X POST localhost:8773/task/ \
        -F data=@data.json \
        -F input.txt=@input.txt
    echo "$(date)"
    sleep 5
done
```

## Listing and managing tasks

```bash
# List
curl -s -X GET localhost:8773/task/ | jq .
blanket ps

# Just the ids
blanket ps -q

# Delete one
curl -s -X DELETE localhost:8773/task/<id> | jq .
blanket rm <id>

# Delete the most recent
blanket ps -q | tail -n1 | xargs blanket rm

# Delete everything
blanket ps -q | xargs -I {} blanket rm {}
```

Filters for `GET /task/` (state, type, tags, date range, `parentId`,
paging) are in [api.md](api.md#list-and-filter-tasks-get-task).

## Workers

Workers claim and execute tasks. Tags advertise capabilities: a worker
only claims tasks whose `tags` it satisfies. See
[tag_ontology.md](tag_ontology.md) for the namespaced tag convention;
workers should advertise generously (more tags than any one task type
needs), since a task's tags only narrow which workers may claim it.

```bash
# Run a worker that handles bash + unix + python3 tasks
blanket worker -t os:unix,exec:bash,runtime:python3

# Validate that all configured task types have working executors
blanket task-validate
```

You can also launch and manage workers from the web UI or via the
`/worker/` REST endpoints.

Worker config keys (defaults shown):

```
workers.logfileNameTemplate  "worker.{{.Id.Hex}}.log"   # worker's own log file
workers.combinedLog          true                       # record stream interleaving
```

`workers.combinedLog` is what lets a task's log views show its two streams
in the order they were produced: the worker tails `blanket.stdout.log` and
`blanket.stderr.log` and records each line into `blanket.combined.ndjson`.
Off means no third file and no tailer per task; the task's own two log
files are written either way. See [Task output
files](task_flow.md#task-output-files) for the format and the grace
window.

## Writing task types

Task types are TOML files under any directory listed in
`tasks.typesPaths` in your config. The filename stem becomes the type
name — `echo_task.toml` → `echo_task`. Drop your own TOML files into the
types directory and submit them the same way as the examples.

```toml
tags = ["exec:bash", "os:unix"]
executor = "bash"
command = "echo 'hello from blanket'"
timeout = 300

  [[environment.default]]
  name = "NAME"
  value = "world"

  [[environment.required]]
  name = "INPUT_FILE"
  description = "Path to the input data"
```

Supported executors: `bash` (default), `cmd` (Windows), `powershell`, or
any executable that accepts `-c <command>`. Executors run the command
non-interactively and without user shell config: `bash -c` ignores
`~/.bashrc` for a non-interactive shell, and `powershell` is invoked with
`-NoProfile -NonInteractive`. If a task needs something a profile would
have set, set it in the task type's `[[environment.default]]` or in the
command itself.

See [task_type_definitions.md](task_type_definitions.md) for the full
schema, [authoring_task_types.md](authoring_task_types.md) for how to
write a good one, and [`examples/types/`](../examples/types/) for working
copy-paste starters.

## Command reference

```
$ blanket -h
A fast and easy way to wrap applications and make them available via nice clean
REST interfaces with built in UI, command line tools, and queuing, all in a
single binary!

Usage:
  blanket [flags]
  blanket [command]

Available Commands:
  backup        Write a consistent copy of the database to the backups directory
  completion    Generate the autocompletion script for the specified shell
  help          Help about any command
  migrate       Inspect, apply, or roll back database schema migrations
  ps            List active and queued tasks
  rm            Remove tasks
  rollback      Put the previous blanket binary back, and optionally its database backup
  service       Manage blanket as a background service (start on login/boot)
  submit        Submit a task to be executed.
  task-validate Validate that task types are well-formed and runnable
  uninstall     Remove blanket's autostart service entry
  upgrade       Install a newer blanket and restart the server onto it
  version       Print the version number of blanket
  worker        Run a worker with capabilities defined by tags

Flags:
  -c, --config string            config file (default is config.json|yaml|toml in the blanket config dir)
      --drain-mode string        whether a restart stops its workers: auto|always|never (default "auto")
      --drain-timeout duration   how long a restart drain waits for workers to exit (0 uses restart.drainTimeout)
      --exec-mode string         how a requested restart replaces the process: auto|exec|exit (default "auto")
  -h, --help                     help for blanket
      --logLevel string          the logging level to use (default "info")
  -p, --port int32               Port the server will run on (default 8773)
```

`backup`, `migrate`, `upgrade` and `rollback` are covered in
[upgrade.md](upgrade.md); `service` and `uninstall` in
[autostart.md](autostart.md). For the full REST API see [api.md](api.md).
