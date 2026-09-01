# Authoring Task Types

A practical guide to writing a *good* blanket task type — for a human, or
for an AI agent pointed at this file (via `CLAUDE.md`, a Cursor rule, an
MCP resource, or the `blanket-task-type` skill under
`.claude/skills/`). For the formal field-by-field schema, see
[task_type_definitions.md](task_type_definitions.md); for tags
specifically, see [tag_ontology.md](tag_ontology.md). This doc doesn't
repeat either — it's the workflow and the judgment calls in between.

## What makes a task type good

Four things, each backed by a `blanket task-validate` check:

1. **Valid Go template syntax** in `command` — checked by code 003.
2. **Good tags** — namespaced, drawn from the ontology or extending it
   deliberately — checked by codes 010/011 (nudges) and 012-014 (opt-in
   strictness).
3. **The right number of inputs** — 2-5 is comfortable; past 10 the type
   is probably doing too much — checked by code 008.
4. **Clear documentation** — `description` and `documentation` filled in
   — checked by codes 006/007.

If you're writing a type by hand or generating one as an agent, running
`blanket task-validate --json <name>` after every draft and reading the
findings is the fastest way to converge on all four.

## The `{{.VAR}}` vs `$VAR` distinction

`command` is rendered as a Go template *once, at submit time*, against
the task's merged environment map. `{{.NAME}}` becomes whatever `NAME`
resolved to when the task was created — a caller-supplied value, or a
declared default.

The same variables are *also* exported as real process environment
variables when the command actually runs, so `$NAME` works too. The
difference matters:

- `{{.NAME}}` is baked into the command string before it's ever handed to
  a shell — useful when you need the value to affect the shape of the
  command itself (e.g. picking which subcommand to run).
- `$NAME` is resolved by the shell at execution time, from whatever the
  worker's process environment looks like — including anything inherited
  from the worker's own environment, not just what blanket declared. This
  is why `task-validate`'s code 004 (a template ref that isn't a declared
  input) is a *warning*, not an error: the ref might be intentionally
  relying on something inherited via `$VAR` instead.

Prefer `$VAR` inside the command body when you can — it reads like normal
shell — and reach for `{{.VAR}}` only when the value needs to change the
command's structure, not just its arguments.

## Sizing: 2-5 inputs, not 20

A task type is a function signature. Two to five inputs is comfortable to
read, submit, and reason about. More than ten is a sign the type is
trying to be too general — split it into two or three narrower types
instead of one type with a dozen optional knobs.

Two channels count as "inputs": declared environment variables
(`environment.default` / `required` / `optional`), and file uploads.
Files aren't declared in the TOML schema — they're an ad-hoc part of the
multipart submit request (see
[usage.md#file-uploads](usage.md#file-uploads)) — so `task-validate`'s
input-count check (008) can only count the declared env vars. If a type
also expects one or two uploaded files as part of its contract, factor
that into whether it's staying in the comfortable range, even though the
validator can't see it.

When you're interviewing a user (or yourself) about what a type needs,
push back on anything that could be a fixed default instead of a
caller-supplied input. "Configurable" isn't automatically better.

## Tags: request the minimum

Tags are worker-matching constraints, not descriptive labels — see
[tag_ontology.md](tag_ontology.md) for the full rationale. The one-line
version: every tag you add *narrows* which workers can claim the task.
When authoring, ask "does this task genuinely need a worker with this
capability?" for each tag, not "would this tag be informative?"

Before inventing a tag, check what's already in use:

```bash
blanket task-validate --dump-known-tags
```

This shows the built-in seed vocabulary, anything declared in
`.blanket/known-tags.conf` (or `.blanket/known-tags.d/*.conf`), and every
tag actually used by a loaded task type — each labeled with its origin.
Prefer an existing tag over inventing a near-duplicate; `task-validate`
will flag likely typos and near-misses (codes 010/011), but it won't stop
you from introducing `runtime:py3` next to an existing `runtime:python3`
if you don't check first.

## Common patterns

### User-supplied command (escape hatch)

For ad-hoc one-offs where a dedicated type isn't worth writing. See
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

Shells out to a real script or interpreter, with an optional input that
has a sensible default so the type is submittable with no environment at
all. See
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

No bash/WSL dependency — runs anywhere `cmd.exe` (or `powershell`) is
available. See
[`examples/types/windows_echo.toml`](../examples/types/windows_echo.toml).

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

Whether you're a human iterating by hand or an agent driving this
programmatically, the loop is the same:

1. **Understand the task.** What command needs to run, what does it
   require (interpreter, OS, network, secrets), and which parts of that
   genuinely vary between runs versus which are fixed defaults?
2. **Check existing context.** Glob the configured `tasks.typesPaths` for
   similar types worth copying from, and run
   `blanket task-validate --dump-known-tags` to see the real tag
   vocabulary in use, not just the seed list.
3. **Write the TOML** — `description`, `documentation`, namespaced tags,
   a template'd `command`, and 2-5 declared inputs.
4. **Validate.**
   ```bash
   blanket task-validate --json <type-name>
   ```
   Fix every error. For each warning, either fix it or have a specific
   reason not to (e.g. a code-004 warning where the referenced variable
   is intentionally inherited from the worker's environment).
5. **Smoke-test it** — submit a real task against the type and confirm it
   runs to `SUCCESS`, then check the result directory and log output
   actually look right, not just that the process exited 0.

## Validating

```bash
blanket task-validate                    # every configured type, table output
blanket task-validate <type-name>        # one type
blanket task-validate --json             # structured findings — drive tooling off this
blanket task-validate --strict           # exit non-zero on warnings too, not just errors
```

See [task_type_definitions.md#validation](task_type_definitions.md#validation)
for the full check table and every flag, including the opt-in tag-lint
strictness flags (`--warn-new-tag`, `--warn-undeclared-tag`,
`--check-workers`).
