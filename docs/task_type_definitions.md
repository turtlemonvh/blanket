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

### description

A one-line summary of what the task type does. Shown on the task types
list and on the new-task form.

### documentation

Multi-line free text for setup, prerequisites, and gotchas — the things
a `description` is too short to hold. Rendered on the task type's detail
page (`/ui/task-types/:name`). Optional; use a TOML triple-quoted string
for multi-line content.

```toml
description = "Run a dbt build against the warehouse"

documentation = '''
Requires `dbt` on $PATH and a populated ~/.dbt/profiles.yml on the
worker host. Writes artifacts to the task result dir.
'''
```

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
so this is where most of the complexity ends up. As a rule of thumb, 2-5
inputs is comfortable to use; more than 10 is a sign the type should
probably be split (see check 008 below).

## Validation

`blanket task-validate [type-name]` checks every configured task type
against a set of coded rules and prints a per-type status plus the
individual findings. Codes are stable once assigned.

| Code | Check | Level |
| --- | --- | --- |
| 001 | `command` is present and non-empty | error |
| 002 | executor resolves on `$PATH` | error |
| 003 | `command` parses as a Go template | error |
| 004 | every `{{.VAR}}` reference is a declared input | warn |
| 005 | a `required` input is never referenced by `command` | warn |
| 006 | `description` is present and non-empty | warn |
| 007 | `documentation` is present and non-empty | warn |
| 008 | declared input count is in the healthy range (2-5) | warn |

004 is deliberately a warning, not an error — a `{{.VAR}}` reference can
legitimately resolve to a variable inherited from the worker's own
environment rather than one declared in this type's `environment` table.

Codes 010+ are reserved for the tag lint (near-miss detection, strictness
flags, worker-existence checks) — see
[tag_ontology.md](tag_ontology.md).

Flags:

* `--json` — print findings as a JSON array (`{type, code, level,
  message, suggestion}`) instead of the table. This is what an authoring
  tool should drive against.
* `--strict` — exit non-zero on warnings too, not just errors. Default
  behavior exits non-zero only on errors, so it stays usable as a
  pre-flight check without failing on style nits.
* `--dump-known-tags` — print the resolved tag vocabulary instead of
  validating. See [tag_ontology.md](tag_ontology.md).
* `--no-builtin-tags` — exclude the built-in seed vocabulary when
  resolving known tags (only affects `--dump-known-tags` today).

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
