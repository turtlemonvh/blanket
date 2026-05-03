# Blanket Docs

Reference documentation for blanket. The top-level
[README](../README.md) covers install + a 60-second start; the pages
here go deeper.

## For users

- [**Usage**](usage.md) — submitting tasks, file uploads, the CLI,
  managing tasks at scale, and the full set of curl/HTTP examples.
- [**Task type definitions**](task_type_definitions.md) — TOML
  schema for authoring your own task types.
- [**API**](api.md) — full list of REST endpoints.

## For maintainers

- [**Task flow**](task_flow.md) — task and worker state machines,
  end-to-end claim/execute lifecycle.
- [**Design**](design.md) — origin, design goals, architecture.
- [**Next up**](next_up.md) — running backlog of planned work.

## Where else to look

- [`../README.md`](../README.md) — install + quick start
- [`../CONTRIBUTORS.md`](../CONTRIBUTORS.md) — development setup,
  build, CI, release process
- [`../examples/types/`](../examples/types/) — copy-paste task type
  TOMLs (`echo_task`, `bash_task`, `python_hello`, `windows_echo`)
