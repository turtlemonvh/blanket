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
DELETE /task/:id                # delete a task; kills it if running
PUT    /task/:id/cancel         # cancel a task; transitions to STOPPED
GET    /task/:id/log            # stream stdout (SSE)
GET    /task/:id/log/tail       # last N lines of stdout
```

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
