# Usage

Detailed examples for working with blanket. The
[top-level README](../README.md) covers install and a 60-second
start; this page goes deeper.

## Starting the server

After running the install script, start blanket with no arguments —
it reads the config from the install location:

```bash
blanket
```

The web UI is at [http://localhost:8773/](http://localhost:8773/).
Override the port with `-p 9000` or via the config file. Set
`--logLevel debug` for verbose output while you're getting started.

To run a custom config explicitly:

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

## Scheduling tasks

Delay a one-shot task, or make it recurring, with `notBefore` / `cron`
(REST) or `--not-before` / `--cron` (CLI) — mutually exclusive. See
[task_flow.md](task_flow.md#scheduling-scheduledts--recurring-tasks-turtlemonvhblanket61)
for the full state machine.

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

# Stop a recurring task for good, keeping the record around (it'll show
# STOPPED in `blanket ps` / GET /task/:id): cancel it, same as any other
# task. Its already-spawned children are unaffected and run to completion
# normally.
curl -s -X PUT localhost:8773/task/<template-task-id>/cancel
# Or remove the record outright instead:
$ blanket rm <template-task-id>

# Pause a recurring task (it stops firing but the record stays RECURRING-
# adjacent, as PAUSED) / resume it later. There's no CLI flag for these
# yet -- REST only:
curl -s -X PUT localhost:8773/task/<template-task-id>/pause
curl -s -X PUT localhost:8773/task/<template-task-id>/resume

# Change a live series' schedule without resubmitting it:
curl -s -X PUT localhost:8773/task/<template-task-id>/schedule \
    -d '{"cron": "0 * * * *"}'
curl -s -X PUT localhost:8773/task/<scheduled-task-id>/schedule \
    -d '{"notBefore": "2h"}'

# Preview a cron expression's friendly description and next fire times
# before submitting (used by the create form's live preview):
curl -s "localhost:8773/schedule/describe?cron=*/5+*+*+*+*" | jq .

# List a series' past runs (its spawned children):
curl -s "localhost:8773/task/?parentId=<template-task-id>" | jq .
```

### Via the web UI

The **New** button on the tasks page opens the create form. Tick
**Schedule task?** to reveal the scheduling section; it's collapsed by
default, so an unscheduled submit is unchanged.

- **One time** takes a start time in a date/time picker. The task waits
  in state `SCHEDULED` until then. The time is read in your browser's
  timezone, and a time already in the past is rejected — unlike the REST
  field, where a past `notBefore` just means "run now".
- **Repeating** takes a 5-field cron expression and shows a live
  human-readable reading of it as you type ("At 02:00 PM, only on
  Tuesday") plus the next three fire times, or the parser's complaint if
  the expression doesn't parse. The submitted task becomes a `RECURRING`
  template.

On submit the flash message names the state and the friendly schedule
(e.g. `RECURRING - Every 5 minutes`), since a scheduled task doesn't
start running the way an ordinary submission does.

Every task response includes a `scheduleDescription` field — a friendly
rendering of its schedule (e.g. `"Every 5 minutes"`, `"Once, at
2026-09-05T08:00:00-04:00"`), shown in the `SCHEDULE` column of
`blanket ps`'s default output. See
[task_flow.md](task_flow.md#friendly-schedule-text) for details, and its
["Scan limit"](task_flow.md#scan-limit-schedulermaxscheduled) section for
the `scheduler.maxScheduled` cap (`POST /task/` returns 429 once hit).

## Web UI

Everything below is also doable in the browser at
[http://localhost:8773/ui/](http://localhost:8773/ui/) — a
server-rendered htmx UI baked into the binary, no separate deploy.

| Page | What it's for |
| ---- | ------------- |
| **Tasks** (`/ui/`) | Every task record: filter by state/type/tags/date, submit a new one, cancel or delete. |
| **Upcoming** (`/ui/upcoming`) | What hasn't run yet — see below. |
| **Workers** (`/ui/workers`) | Launch, stop, restart workers; tail their logs. |
| **Task Types** (`/ui/task-types`) | The loaded TOML types and their settings. |
| **About** (`/ui/about`) | Version, config file, effective settings. |

### Upcoming

`/ui/upcoming` is the "what's queued on a schedule" view, split into the
two things that behave differently:

- **One-time** — tasks in state `SCHEDULED` (submitted with `notBefore`).
  Each shows the time it will run and a friendly description, and can be
  cancelled straight from the list.
- **Series** — live (`RECURRING`) and `PAUSED` templates. Each shows its
  friendly schedule, the raw cron expression, the next fire time (or when
  it was paused), a status badge, and a link to its detail page. Paused
  series stay listed; cancelled ones drop off, though their record — and
  their past runs — remain reachable at `/ui/tasks/<id>`.

### Series detail

A recurring template's own `/ui/tasks/<id>` page is the series view. From
it you can:

- read the schedule in plain English alongside the raw cron expression and
  the next fire time;
- **pause** it (the page then shows when it was paused) and **resume** it,
  which recomputes the next fire from now;
- **change the schedule** — type a new cron expression and a live preview
  shows what it means and its next three fire times before you save;
- **cancel** it for good, which stops it firing but keeps the record and
  its history (`DELETE`, via the Tasks page, removes the record instead);
- browse **past runs** — every task the series has spawned, in the same
  table as the main Tasks list.

### Series membership

A task spawned by a series carries a `parentId`, and the UI links back to
where it came from: its detail page opens with a card naming the series,
its schedule, and whether that series is currently live, paused, or
cancelled, and its row in the Tasks list carries a compact
"part of series …" link.

## File uploads

Attach files to a task — they're placed in the task's working
directory before the command runs.

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

## Workers

Workers claim and execute tasks. Tags advertise capabilities — a
worker only claims tasks whose `tags` it satisfies. See
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

## Writing task types

Task types are TOML files under any directory listed in
`tasks.typesPaths` in your config. The filename stem becomes the
type name — `echo_task.toml` → `echo_task`.

Drop your own TOML files into the types directory and submit them
the same way as the examples.

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

Supported executors: `bash` (default), `cmd` (Windows), `powershell`,
or any executable that accepts `-c <command>`.

See [task_type_definitions.md](task_type_definitions.md) for the full
schema, and [`examples/types/`](../examples/types/) for working
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
  completion    Generate the autocompletion script for the specified shell
  help          Help about any command
  ps            List active and queued tasks
  rm            Remove tasks
  submit        Submit a task to be executed.
  task-validate Validate that task types are runnable
  version       Print the version number of blanket
  worker        Run a worker with capabilities defined by tags

Flags:
  -c, --config string     config file (default is config.json|yaml|toml in the blanket config dir)
  -h, --help              help for blanket
      --logLevel string   the logging level to use (default "info")
  -p, --port int32        Port the server will run on (default 8773)
```

For the full REST API see [api.md](api.md).
