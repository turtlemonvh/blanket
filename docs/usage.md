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
worker only claims tasks whose `tags` it satisfies.

```bash
# Run a worker that handles bash + unix + python tasks
blanket worker -t unix,bash,python

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
tags = ["bash", "unix"]
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
