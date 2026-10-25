# Task Type Definitions

A task type is a TOML file that says how to run one kind of work: the
command, its environment variables, its timeout, and which workers may
claim it. The smallest useful type,
[`examples/types/echo_task.toml`](../examples/types/echo_task.toml):

```toml
description = "Write a fixed string to stdout and exit 0."
tags = ["exec:bash", "os:unix"]
timeout = 10
command = "echo 'hello from blanket'"
executor = "bash"
```

This page is the field reference. For how to write a good type —
sizing, `{{.VAR}}` vs `$VAR`, the validate-and-iterate loop — see
[authoring_task_types.md](authoring_task_types.md).

## File location: `tasks.typesPaths`

The server loads task types from every directory listed in the
`tasks.typesPaths` config key. A file is a task type when its name is
`<name>.toml`, with `name` made of letters, digits and underscores; the
stem is the type name (`echo_task.toml` → `echo_task`). `command` is the
only required field.

## Fields

### description

One line saying what the type does. Shown on the task types list and
the new-task form.

### documentation

Free text for setup, prerequisites and gotchas — what `description` is
too short to hold. Shown on the type's detail page
(`/ui/task-types/:name`). Optional; use a TOML triple-quoted string for
several lines.

```toml
description = "Run a dbt build against the warehouse"

documentation = '''
Requires `dbt` on $PATH and a populated ~/.dbt/profiles.yml on the
worker host. Writes artifacts to the task result dir.
'''
```

### tags

A list of strings naming what a worker must advertise to claim this
type's tasks: a worker claims a task only if its tags are a superset of
the task's. See [tag_ontology.md](tag_ontology.md) for the matching
rule and the `namespace:value` convention (`os:linux`, `exec:bash`,
`team:data-eng`, ...).

### timeout

Maximum run time in seconds. Default `3600` (one hour). A task that
exceeds it is killed and marked `TIMEDOUT`.

### command

The command to run. It is rendered once, at submit time, as a Go
[text/template](https://golang.org/pkg/text/template/) against the
task's environment map, so `{{.NAME}}` becomes the value of `NAME`. The
same variables are also exported to the process, so `$NAME` works at
run time. When each is the right choice is covered in
[`{{.VAR}}` vs `$VAR`](authoring_task_types.md#the-var-vs-var-distinction).

### executor

The shell or interpreter that runs `command`:

| executor | how it runs | typical platforms |
| -------- | ----------- | ----------------- |
| `bash` (default) | `bash -c <command>` | Linux, macOS, WSL |
| `cmd` | `cmd /c <command>` | Windows |
| `powershell` | `powershell -NoProfile -NonInteractive -Command <command>` | Windows, macOS, Linux |
| any other binary | `<executor> -c <command>` | depends on the binary |

The executor must be on the worker's `$PATH`; `blanket task-validate`
checks this (code 002).

Executors run without the user's shell profile: `bash -c` does not
source `~/.bashrc` for a non-interactive shell, and `powershell` runs
with `-NoProfile`, so a task that needs something a profile would set
must set it in its own `environment` or `command`. `-NonInteractive`
turns a PowerShell prompt into an error rather than a task that hangs
until its timeout. Budget a `powershell` type's `timeout` for
interpreter startup as well as the work: `powershell.exe` starts far
slower than `cmd.exe`, which is why
[`windows_powershell.toml`](../examples/types/windows_powershell.toml)
allows 60s where its `cmd` sibling allows 10.

### result_file

The name of a file the task writes, whose contents callers get back as
structured data. Optional; a type that declares none reports
`result: null`.

```toml
command = "./lookup.sh {{.USER_ID}} > result.json"
result_file = "result.json"
```

The path is **relative to the task's result directory**, which is also
the task's working directory, so a task that writes `result.json` where
it runs needs no path at all. The file is read once the task reaches a
terminal state, parsed as JSON, and returned as the `result` field of a
[synchronous submission's](api.md#wait-for-a-task-to-finish-post-taskwait)
completion payload.

* A missing file yields `result: null` and is **not** an error — a task
  that failed before writing its result is a normal outcome.
* A file that exists but is unparseable, unreadable, or larger than
  `tasks.sync.maxResultBytes` (default 1 MiB) yields `result: null` plus
  a `resultError` message, so a malformed result never looks like an
  absent one.
* The path must stay inside the result directory. Absolute paths
  (`/etc/passwd`, `C:\...`), UNC paths, and anything that escapes via
  `..` are **rejected when the type is loaded**: the type does not load
  at all, and `blanket task-validate` reports it as check 009. The same
  rule is applied again when the file is read.
* Subdirectories are fine: `result_file = "out/result.json"`.

### environment

Three lists of environment variables:

* **default** — set unless the caller overrides it; carries a `value`
* **required** — the caller must supply it when creating the task
* **optional** — the caller may supply it; no default value. Mainly
  documents what the command will read.

Each entry takes a `name` and a `description`; `default` entries also a
`value`. Every value reaches the process as a string. A task submission
can always add env vars the type does not declare.

Env vars are a task type's inputs, so this is where the complexity
collects. Keep them few: see
[How many inputs](authoring_task_types.md#how-many-inputs-2-5-not-20)
and check 008 below.

## Validation: `blanket task-validate`

`blanket task-validate [type-name]` checks every configured type, or
one, against coded rules and prints a per-type status plus the findings.
Codes are stable once assigned. The exit status is non-zero on any
error, or with `--strict` on any warning too.

### Checks 001-009

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
| 009 | `result_file` is a relative path contained in the result dir | error |

004 is a warning, not an error, because a `{{.VAR}}` reference can
legitimately resolve to a variable inherited from the worker's own
environment rather than one declared in the type. 009 is an error
because the loader rejects a type that trips it; it is not servable
until fixed.

### Tag lint: codes 010-014

These check tags against the known-tag set described in
[tag_ontology.md](tag_ontology.md#extending-the-vocabulary-known-tagsconf).
010 and 011 are on by default and fire only on a tag that looks like a
typo of an existing one, so a new, well-formed namespaced tag such as
`team:platform` passes silently. 012-014 are opt-in.

| Code | Check | Default |
| --- | --- | --- |
| 010 | tag is a near-miss (edit distance ≤2) of a known tag | warn |
| 011 | unnamespaced tag has a namespaced value-match (`bash` → `exec:bash`) | warn |
| 012 | tag is new — not declared anywhere, not used by any other type | off (`--warn-new-tag`) |
| 013 | tag isn't declared in the known-tags vocabulary, even if used elsewhere | off (`--warn-undeclared-tag`) |
| 014 | no registered worker advertises a superset of this type's tags | off (`--check-workers`) |

The `tasks.warnNewTag` and `tasks.warnUndeclaredTag` config keys turn
012 and 013 on without the flag. 014 asks the local server
(`GET /worker/` on the configured `port`) which non-stopped workers are
registered; if it cannot be reached, `--check-workers` yields a single
"skipped" finding instead of failing the run.

### Flags

* `--json` — print findings as a JSON array (`{type, code, level,
  message, suggestion}`) instead of the table. Drive tooling off this.
* `--strict` — exit non-zero on warnings too, not just errors. Without
  it only errors fail the run, so it works as a pre-flight check that
  ignores style nits.
* `--dump-known-tags` — print the resolved tag vocabulary instead of
  validating. See [tag_ontology.md](tag_ontology.md).
* `--no-builtin-tags` — exclude the built-in seed vocabulary when
  resolving known tags.
* `--warn-new-tag` — enable code 012.
* `--warn-undeclared-tag` — enable code 013.
* `--check-workers` — enable code 014.

## Examples

[`examples/types/`](../examples/types/) holds copy-paste starters:
`echo_task` (minimal), `bash_task` (arbitrary command via env var),
`python_hello`, `windows_echo` (`cmd`, no bash needed),
`windows_powershell`, `docker_run`, `claude_code` and `codex`.
[Common patterns](authoring_task_types.md#common-patterns) walks through
four of them.
