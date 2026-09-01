# Blanket Docs

Reference documentation for blanket. The top-level
[README](../README.md) covers install + a 60-second start; the pages
here go deeper.

## For users

- [**Usage**](usage.md) — submitting tasks, file uploads, the CLI,
  managing tasks at scale, and the full set of curl/HTTP examples.
- [**Task type definitions**](task_type_definitions.md) — TOML
  schema for authoring your own task types.
- [**Authoring task types**](authoring_task_types.md) — practical
  guide (and AI-agent reference) for writing a good task type: sizing,
  the `{{.VAR}}` vs `$VAR` distinction, common patterns, the
  validate-and-iterate loop.
- [**Tag ontology**](tag_ontology.md) — the namespaced tag convention,
  why tags are worker constraints rather than labels, and how to
  extend the vocabulary.
- [**API**](api.md) — full list of REST endpoints.
- [**Offline install**](offline_install.md) — installing on machines
  with no internet access and only local-user permissions.

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
