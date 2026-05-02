# Task Type Definitions

Task types describe how to run a unit of work — what command to
execute, what environment variables it needs, what timeout to enforce,
and which workers are eligible to claim it. They live as TOML files
under any directory listed in the `tasks.typesPaths` config option.
The filename stem is the type name: `echo_task.toml` →
[`echo_task`](../examples/types/echo_task.toml).

The only hard requirements are:

* the filename must end in `.toml`
* the file must be in one of the locations listed in the
  `tasks.typesPaths` variable in the server config
* the `command` field is present (it is currently the only required
  field)

The command is rendered through Go's
[text/template](https://golang.org/pkg/text/template/) so it can
substitute environment variables at submit time.

## Field names

### tags

A list of strings. Defines the capabilities required of any worker
that wants to execute this task. A worker only claims a task whose
tags it satisfies.

### timeout

Max duration of the task in seconds. Default is `3600` (one hour).
Tasks that exceed this are killed and marked `TIMEDOUT`.

### command

The command to execute when the task runs. Supports
[Go template](https://golang.org/pkg/text/template/) substitution
against the task's environment map (e.g. `{{.NAME}}` is replaced with
the value of the `NAME` env var at submit time). Environment
variables are also available at exec time as `$NAME` — the difference
matters when the value is set by the caller versus inherited from the
shell.

### executor

The shell or interpreter that runs `command`. Supported values:

| executor | how it runs | typical platforms |
| -------- | ----------- | ----------------- |
| `bash` (default) | `bash -c <command>` | Linux, macOS, WSL |
| `cmd` | `cmd /c <command>` | Windows |
| `powershell` | `powershell -Command <command>` | Windows, macOS, Linux |
| any other binary | `<executor> -c <command>` | depends on the binary |

The executor binary must be on the worker's `$PATH`. Run
`blanket task-validate` to check that all configured task types have
their executor available on the current host.

### environment

A map of environment variables with three sections: `default`,
`required`, and `optional`.

* **default**: present by default, can be overridden by the caller
* **required**: must be sent when a new task instance is created
* **optional**: may be set but is not required; no default value
  (primarily for documentation and discoverability)

Each entry takes a `name` and `description`. `default` entries also
take a `value`. When submitting a task, you can always add additional
env variables that are not part of the type definition.

Environment variables are the main unit of configurability for tasks,
so this is where most of the complexity ends up.

## Examples

See [`examples/types/`](../examples/types/) for the full set of
copy-paste-ready starters: `echo_task` (minimal), `bash_task`
(arbitrary command via env var), `python_hello`, and `windows_echo`
(uses `cmd`, no bash needed).

### A simple bash task that runs a user-supplied command

```toml
tags = ["bash", "unix"]

# timeout in seconds
timeout = 200

# The command to execute
command='''
{{.DEFAULT_COMMAND}}
'''

executor="bash"

    # Environment variables are injected into the process environment

    [[environment.default]]
    name = "ANIMAL"
    value = "giraffe"

    [[environment.default]]
    name = "SECOND_ANIMAL"
    value = "hippo"

    # Remember, everything is interpreted as a string when passed as an env variable
    [[environment.default]]
    name = "NUM_FROGS"
    value = "3"

    [[environment.required]]
    name = "DEFAULT_COMMAND"
    description = "The bash command to run. E.g. `echo $(date)`"
```

### A Windows-native task using cmd

```toml
tags = ["windows"]
executor = "cmd"
command = "echo hello from blanket"
timeout = 10
```

No bash or WSL required — runs anywhere `cmd.exe` is available.
