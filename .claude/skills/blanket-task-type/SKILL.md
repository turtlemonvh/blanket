---
name: blanket-task-type
description: Use when creating or editing a blanket task type (a TOML file under a tasks.typesPaths directory) — writing a new one from a description of what should run, or fixing/improving an existing one. Triggers on phrasings like "add a blanket task type for X", "write a task type that runs Y", "make this task type submittable with fewer inputs", or edits to any *.toml file under a directory blanket is configured to read task types from.
---

# blanket-task-type

**If the blanket MCP server is connected, use it — don't follow this
file.** Its `new_task_type` prompt (`/blanket:new_task_type` in Claude
Code; `/mcp__blanket__new_task_type` also works) carries the full
authoring workflow, and its tools do each
step: `blanket_docs(page="authoring")` for the guide,
`blanket_write_task_type` to validate and save, `blanket_workers` /
`blanket_launch_worker` for a matching worker, `blanket_run_task` to
smoke-test. For a quick edit, call those tools directly in that order.

This skill stays only for sessions without the server connected
(turtlemonvh/blanket#232; it goes away once the prompt has seen real use).

## Fallback: no MCP server

The same workflow, with the CLI:

1. **Read the guide** — `docs/authoring_task_types.md` (schema, sizing,
   `{{.VAR}}` vs `$VAR`, the loop in detail), `docs/tag_ontology.md`, and
   `docs/task_type_definitions.md` (the `task-validate` check table).
   Read them rather than relying on memory; they change independently
   of this file.
2. **Find context** — the configured `tasks.typesPaths` (server config,
   or ask), existing `*.toml` there to copy from, and
   `blanket task-validate --dump-known-tags` for the real tag vocabulary.
3. **Write the TOML** — `description`, `documentation`, namespaced
   `tags` reusing existing ones, a templated `command`, and 2-5 inputs.
   Don't turn something into an input just because it *could* vary.
4. **Validate** — `blanket task-validate --json <type-name>`. Fix every
   error; fix each warning or state a specific reason it's expected.
5. **Smoke-test** — check a worker with matching tags is running, then
   `blanket submit -t <type-name>` and confirm the task reaches
   `SUCCESS`. Validation catches structure, not runtime failures.
6. **Report** the type, the tags and why, and the final validate output.

## Repo notes

- `examples/types/` holds realistic task types to copy patterns from.
- `testdata/types/echo_task.toml` is the minimal smoke-test fixture —
  kept tiny on purpose; don't add examples there.
