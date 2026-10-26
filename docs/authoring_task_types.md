# Authoring Task Types

How to write a *good* task type, for a human or for an agent (the MCP
server's `new_task_type` prompt and `blanket_docs(page="authoring")`
point here). The field-by-field schema is
[task_type_definitions.md](task_type_definitions.md) and tags are
[tag_ontology.md](tag_ontology.md); this page is the workflow and the
judgment calls in between.

## What makes a task type good

Four things, each backed by a `blanket task-validate` check:

1. **Valid Go template syntax** in `command` — code 003.
2. **Good tags** — namespaced, drawn from the ontology or extending it
   deliberately — codes 010/011 (nudges) and 012-014 (opt-in
   strictness).
3. **The right number of inputs** — 2-5 is comfortable; past 10 the
   type is probably doing too much — code 008.
4. **Clear documentation** — `description` and `documentation` filled
   in — codes 006/007.

Running `blanket task-validate --json <name>` after every draft and
reading the findings is the fastest way to converge on all four.

## The `{{.VAR}}` vs `$VAR` distinction

`command` is rendered as a Go template *once, at submit time*, against
the task's merged environment map: `{{.NAME}}` becomes whatever `NAME`
resolved to when the task was created, a caller-supplied value or a
declared default.

The same variables are *also* exported as process environment variables
when the command runs, so `$NAME` works too. The difference:

- `{{.NAME}}` is baked into the command string before any shell sees
  it. Use it when the value must change the shape of the command, such
  as which subcommand runs.
- `$NAME` is resolved by the shell at run time from the worker's process
  environment, which includes anything the worker inherited, not only
  what blanket set. This is why `task-validate`'s code 004 (a template
  reference that is not a declared input) is a *warning*: the reference
  may be relying on something inherited via `$VAR` on purpose.

Prefer `$VAR` in the command body — it reads like normal shell — and
reach for `{{.VAR}}` only when the value changes the command's
structure rather than its arguments.

## How many inputs: 2-5, not 20

A task type is a function signature. Two to five inputs is comfortable
to read, submit and reason about. More than ten means the type is
trying to be too general: split it into two or three narrower types
instead of one with a dozen optional knobs.

Two channels count as inputs: declared environment variables
(`environment.default` / `required` / `optional`) and file uploads.
Files are not declared in the TOML — they are an ad-hoc part of the
multipart submit request ([usage.md#file-uploads](usage.md#file-uploads))
— so check 008 counts only the declared env vars. If a type also
expects one or two uploaded files, count them yourself.

When interviewing a user (or yourself) about what a type needs, push
back on anything that could be a fixed default instead of an input.
"Configurable" is not automatically better.

## Tags: request the minimum

Tags are worker-matching constraints, not descriptive labels: a worker
claims a task only if its tags are a superset of the task's, so every
tag you add *narrows* which workers can run it (see
[How tags match workers](tag_ontology.md#how-tags-match-workers)). For
each tag ask "does this task need a worker with this capability?", not
"would this tag be informative?".

Before inventing a tag, check what is already in use:

```bash
blanket task-validate --dump-known-tags
```

This prints the built-in seed vocabulary, anything declared in
`.blanket/known-tags.conf` (or `.blanket/known-tags.d/*.conf`), and
every tag used by a loaded task type, each labelled with its origin.
Prefer an existing tag over a near-duplicate: `task-validate` flags
likely typos (codes 010/011), but it will not stop you introducing
`runtime:py3` next to an existing `runtime:python3` if you do not look
first.

## Common patterns

### User-supplied command (escape hatch)

For ad-hoc one-offs where a dedicated type is not worth writing. See
[`examples/types/bash_task.toml`](../examples/types/bash_task.toml).

```toml
description = "Run an arbitrary bash command supplied at submit time."
tags = ["exec:bash", "os:unix"]
timeout = 300
command = '''
{{.DEFAULT_COMMAND}}
'''
executor = "bash"

  [[environment.required]]
  name = "DEFAULT_COMMAND"
  description = "The bash command to run."
```

### Script wrapper with a default

Shells out to a script or interpreter, with an optional input whose
default makes the type submittable with no environment at all. See
[`examples/types/python_hello.toml`](../examples/types/python_hello.toml).

```toml
description = "Print a greeting via python3."
tags = ["runtime:python3", "os:unix"]
timeout = 60
command = '''
python3 -c "print('hello, {{.NAME}}!')"
'''
executor = "bash"

  [[environment.default]]
  name = "NAME"
  value = "world"
```

### Windows-native

No bash or WSL dependency; runs anywhere `cmd.exe` (or `powershell`) is
available. See
[`examples/types/windows_echo.toml`](../examples/types/windows_echo.toml)
(`cmd`) and
[`examples/types/windows_powershell.toml`](../examples/types/windows_powershell.toml)
(`powershell`).

```toml
description = "Write a fixed string to stdout via cmd.exe."
tags = ["os:windows"]
executor = "cmd"
command = "echo hello from blanket"
timeout = 10
```

### Multi-step bash

For anything longer than a one-liner, a triple-quoted `command` reads
better than semicolon-chaining, and each step can reference its own
inputs:

```toml
description = "Clone a repo, run its test suite, and report the result."
documentation = '''
Requires git and the project's own toolchain on the worker's $PATH.
Clones into a fresh temp dir each run; nothing persists between tasks.
'''
tags = ["exec:bash", "os:unix", "runtime:git"]
timeout = 900
command = '''
set -euo pipefail
WORKDIR=$(mktemp -d)
git clone --depth 1 --branch {{.BRANCH}} {{.REPO_URL}} "$WORKDIR"
cd "$WORKDIR"
{{.TEST_COMMAND}}
'''
executor = "bash"

  [[environment.required]]
  name = "REPO_URL"
  description = "Git URL to clone, e.g. https://github.com/org/repo.git"

  [[environment.default]]
  name = "BRANCH"
  value = "main"

  [[environment.default]]
  name = "TEST_COMMAND"
  value = "make test"
```

## The authoring loop

The same five steps, by hand or driven by an agent:

1. **Understand the task.** What command runs, what it needs
   (interpreter, OS, network, secrets), and which parts vary between
   runs versus which are fixed defaults.
2. **Check existing context.** Look through the configured
   `tasks.typesPaths` for similar types worth copying, and run
   `blanket task-validate --dump-known-tags` for the tag vocabulary
   actually in use, not just the seed list.
3. **Write the TOML**: `description`, `documentation`, namespaced tags,
   a templated `command`, and 2-5 declared inputs.
4. **Validate.**
   ```bash
   blanket task-validate --json <type-name>
   ```
   Fix every error. For each warning, fix it or have a specific reason
   not to (a code-004 warning where the variable is intentionally
   inherited from the worker's environment, for example).
5. **Smoke-test it.** Submit a real task against the type, confirm it
   reaches `SUCCESS`, then check that the result directory and log
   output look right, not just that the process exited 0.

## Validating

```bash
blanket task-validate                    # every configured type, table output
blanket task-validate <type-name>        # one type
blanket task-validate --json             # structured findings — drive tooling off this
blanket task-validate --strict           # exit non-zero on warnings too, not just errors
```

The check table and every flag, including the opt-in tag-lint flags
(`--warn-new-tag`, `--warn-undeclared-tag`, `--check-workers`), are in
[task_type_definitions.md](task_type_definitions.md#validation-blanket-task-validate).
