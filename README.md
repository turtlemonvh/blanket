# Blanket

Blanket is a RESTy wrapper for long running tasks.

## Development setup

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
make docker-test-browser   # Playwright suite
make docker-test-smoke     # binary smoke test
make docker-shell          # interactive shell, source mounted at /src
```

See `Dockerfile` for what the image carries.

## Quick Start

```bash
# Build the binary. `make linux` → ./blanket-linux-amd64,
# `make darwin` → ./blanket-darwin-amd64, `make windows` → .exe.
make linux

# Copy the shipped example task types so there's something to exercise.
# Drop your own TOML files in this directory (or any directory listed in
# `tasks.typesPaths`) as you build up your own types.
cp -r examples/types ./types

# Create a config file
cat > blanket.json <<'EOF'
{
  "port": 8773,
  "tasks": {
    "typesPaths": ["./types"],
    "resultsPath": "results"
  },
  "logLevel": "debug"
}
EOF

# Run server
./blanket-linux-amd64
```

Once the server is running, you can view the web UI at [http://localhost:8773/](http://localhost:8773/).  You can also interact with blanket via curl and the command line.  For example, you can list tasks

```bash
curl -s -X GET localhost:8773/task/ | jq .
# OR
./blanket-linux-amd64 ps
```

Submit a task of the shipped `echo_task` type (the minimal one — just
writes a string to stdout):

```bash
curl -s -X POST localhost:8773/task/ \
    -d '{"type": "echo_task"}'
```

The `bash_task` example takes a `DEFAULT_COMMAND` env var and runs it
through bash — the generic escape hatch for ad-hoc commands:

```bash
curl -s -X POST localhost:8773/task/ \
    -d '{"type": "bash_task", "environment": {"DEFAULT_COMMAND": "echo $(date)"}}'
curl -X POST localhost:8773/task/ \
    -d '{"type": "bash_task", "environment": {"DEFAULT_COMMAND": "cd ~ && ls -lah"}}'
```

The `python_hello` example shells out to `python3` and has an optional
`NAME` env var with a default:

```bash
curl -s -X POST localhost:8773/task/ \
    -d '{"type": "python_hello", "environment": {"NAME": "blanket"}}'
```

See `examples/types/*.toml` for the schema; drop your own TOML files
into `./types/` and submit them the same way.

Add a task and upload some data files with it. Files will be placed at the root of the directory where the task runs.

```bash
# Send task as a form field named data with multiple files
curl -X POST localhost:8773/task/ \
    -F data='{"type": "echo_task", "environment": {"GREETING": "hi"}}' \
    -F blanket.json=@blanket.json

# Send task data as another file named "data"
cat > data.json <<'EOF'
{
    "type": "echo_task",
    "environment": {
        "GREETING": "hi"
    }
}
EOF
curl -X POST localhost:8773/task/ -F data=@data.json -F blanket.json=@blanket.json
```

Add lots of tasks

```bash
while true; do
    curl -X POST localhost:8773/task/ \
        -F data=@data.json \
        -F blanket.json=@blanket.json
    echo "$(date)"
    sleep 5
done
```

There is also limited functionality for sending tasks via the command line.

```
# Send in a single task
$ ./blanket-linux-amd64 submit -t echo_task -e '{"GREETING": "hi"}'
echo_task 69ded2acce42aa8a11ac9ddc [1744748400]

# Send in a single task, printing only the id of the task once it is submitted
$ ./blanket-linux-amd64 submit -t echo_task -e '{"GREETING": "hi"}' -q
69ded2adce42aa8a11ac9de0
```

Delete a task

```bash
curl -s -X DELETE localhost:8773/task/69ded2acce42aa8a11ac9ddc | jq .
# OR
./blanket-linux-amd64 ps -q | tail -n1 | xargs ./blanket-linux-amd64 rm

# Remove all tasks
./blanket-linux-amd64 ps -q | xargs -I {} ./blanket-linux-amd64 rm {}
```

Run worker with certain capabilities

```bash
# You can also launch from the web UI or via an api call
./blanket-linux-amd64 worker -t unix,bash,python
```

## Single-binary distribution

`go build` produces a single static binary with the web UI baked in.
Templates, CSS, and vendored htmx live under `server/ui_next/` and are
pulled into the binary via `//go:embed` (see `server/ui_next.go`). No
separate asset deploy, no runtime filesystem lookups — drop the binary
on a host and run it.

To refresh the vendored htmx bundle:

```bash
curl -sSfL https://unpkg.com/htmx.org@1.9.12/dist/htmx.min.js \
    -o server/ui_next/static/htmx.min.js
curl -sSfL https://unpkg.com/htmx.org@1.9.12/dist/ext/sse.js \
    -o server/ui_next/static/htmx-sse.js
```

## Command line API

```bash
$ ./blanket-linux-amd64 -h
A fast and easy way to wrap applications and make them available via nice clean REST interfaces with built in UI, command line tools, and queuing, all in a single binary!

Usage:
  blanket [flags]
  blanket [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  ps          List active and queued tasks
  rm          Remove tasks
  submit      Submit a task to be executed.
  version     Print the version number of blanket
  worker      Run a worker with capabilities defined by tags

Flags:
  -c, --config string     config file (default is blanket.yaml|json|toml)
  -h, --help              help for blanket
      --logLevel string   the logging level to use (default "info")
  -p, --port int32        Port the server will run on (default 8773)

Use "blanket [command] --help" for more information about a command.
```

## Specs

See [the docs directory](https://github.com/turtlemonvh/blanket/tree/master/docs) for more detailed information about the design of blanket.

### Origin

Blanket was designed because of problems I saw on several projects in [GTRI's ELSYS branch](https://www.gtri.gatech.edu/elsys) where we wanted to be able to integrate a piece of software with a less than awesome API into another tool.  Whether that software was a long running simulation, a CAD renderer, or some other strange beast, we kept seeing people try to wrap HTTP servers around these utilities.  This seemed unnecessary and wasteful.

The starting concept of blanket was simple: If we can wrap anything with a command line call (which is possible with tools like [sikuli](http://www.sikuli.org/)), and we could make it easy to expose any command line script as a web endpoint, then we can provide a nice consistent way to expose cool software with a possibly bad API to a larger class of users.

The first draft of blanket was written in python and used celery for queuing. It worked fine, but was a bit heavy weight, and was hard for some Windows users to install. Go was chosen for the rewrite since

* It compiles to a single binary, so deployment is easy
* It cross compiles to many platforms, so getting it to behave on windows shouldn't be too painful

This was my first major project in Go, and the code base is still recovering from some early "experiments".  It is still a bit rough around the edges, but I do use blanket almost every day at work to manage long running tasks that I'd like to keep a record of, like code deployments.  In this regard, it's a bit like a light-weight [jenkins](https://jenkins.io/).  I plan to continue working on blanket, and I expect it will become a major component of many of my future side projects.

If you are interested in using blanket for a project and want to ask whether blanket may be a good match, you can either [submit a github issue with your question](https://github.com/turtlemonvh/blanket/issues) or find me on twitter [@turtlemonvh](https://twitter.com/turtlemonvh).

### Design Goals

> This is how we want it to work, not necessarily how it works now.

* Speed is not a high priority at the moment. Instead, we favor 
    * simplicity: API is easy to work with, and tasks are hard to lose
    * pluggability: It is easy to change storage and queue backends while maintaining the same API.
    * traceable: It's easy to understand what's going on.
    * open: It's easy to get data in and out.
    * low resourse usage: Like [xinetd](https://en.wikipedia.org/wiki/Xinetd), it can be present and usable without you thinking about it.
* Blanket is designed for long running tasks, not high speed messaging. We assume
    * Tasks will be running for a long time (several seconds or more).
    * Contention between workers will be fairly low.
* TOML files drive all configuration for tasks
* The web UI is optional
    * Everything can be done without it, easily
    * The main feature is a json/rest interface


## Misc

* Go modules for dependency management
* `//go:embed` for static files (see `server/ui_next.go`)
* Server-rendered Go templates + [htmx](https://htmx.org/) for the web UI

