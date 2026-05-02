# Blanket

Blanket is a RESTy wrapper for long running tasks. Define task types as
TOML files, submit them via REST API or CLI, and let workers execute
them — all from a single binary with a built-in web UI.

## Installation

Download a pre-built binary from [GitHub Releases](https://github.com/turtlemonvh/blanket/releases), or use one of the one-liner installers below.

**Linux / macOS:**

```bash
curl -sSfL https://raw.githubusercontent.com/turtlemonvh/blanket/master/scripts/install.sh | bash
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/turtlemonvh/blanket/master/scripts/install.ps1 | iex
```

The installers download the binary, create config/data directories
(XDG-compliant on Linux/macOS, `%LOCALAPPDATA%\blanket` on Windows),
write a default config file, and fetch the example task types. Set
`INSTALL_DIR` to override the binary location, or `VERSION=v0.1.0`
to pin a release.

| | Binary | Config | Data |
|---|---|---|---|
| Linux/macOS | `~/.local/bin/blanket` | `~/.config/blanket/` | `~/.local/share/blanket/` |
| Windows | `%LOCALAPPDATA%\blanket\bin\blanket.exe` | `%LOCALAPPDATA%\blanket\` | `%LOCALAPPDATA%\blanket\` |

## Quick Start

```bash
# Start the server (uses ~/.config/blanket/config.json by default)
blanket
```

Open the web UI at [http://localhost:8773/](http://localhost:8773/).

```bash
# List tasks
blanket ps

# Submit a task
curl -s -X POST localhost:8773/task/ -d '{"type": "echo_task"}'
# OR
blanket submit -t echo_task

# Run a worker with specific capabilities
blanket worker -t bash,unix
```

## Usage

### Submitting tasks

The `echo_task` example writes a fixed string to stdout:

```bash
curl -s -X POST localhost:8773/task/ \
    -d '{"type": "echo_task"}'
```

The `bash_task` example takes a `DEFAULT_COMMAND` env var and runs it:

```bash
curl -s -X POST localhost:8773/task/ \
    -d '{"type": "bash_task", "environment": {"DEFAULT_COMMAND": "echo $(date)"}}'
```

The `python_hello` example shells out to `python3`:

```bash
curl -s -X POST localhost:8773/task/ \
    -d '{"type": "python_hello", "environment": {"NAME": "blanket"}}'
```

Submit via CLI (useful for scripting):

```
$ blanket submit -t echo_task -e '{"GREETING": "hi"}'
echo_task 69ded2acce42aa8a11ac9ddc [1744748400]

$ blanket submit -t echo_task -e '{"GREETING": "hi"}' -q
69ded2adce42aa8a11ac9de0
```

### File uploads

Attach files to a task — they're placed in the task's working directory:

```bash
curl -X POST localhost:8773/task/ \
    -F data='{"type": "echo_task", "environment": {"GREETING": "hi"}}' \
    -F blanket.json=@blanket.json
```

### Managing tasks

```bash
# Delete a task
curl -s -X DELETE localhost:8773/task/<task-id> | jq .
blanket rm <task-id>

# Remove all tasks
blanket ps -q | xargs -I {} blanket rm {}
```

### Validating task types

Check that all configured task types are runnable (executor exists on
PATH, command is non-empty):

```bash
blanket task-validate
```

### Writing task types

Task types are TOML files. Drop them in any directory listed in
`tasks.typesPaths` in your config. The filename stem becomes the type
name.

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

See `examples/types/*.toml` for working examples.

## Command Reference

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
  -c, --config string     config file (default is blanket.yaml|json|toml)
  -h, --help              help for blanket
      --logLevel string   the logging level to use (default "info")
  -p, --port int32        Port the server will run on (default 8773)

Use "blanket [command] --help" for more information about a command.
```

## Contributing

See [CONTRIBUTORS.md](CONTRIBUTORS.md) for development setup, build
instructions, CI details, and code conventions.
