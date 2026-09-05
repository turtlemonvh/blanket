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
- [**MCP interface**](mcp.md) — tool list, setup (incl. Claude Code),
  permissions, and the default security posture.
- [**Offline install**](offline_install.md) — installing on machines
  with no internet access and only local-user permissions.
- [**Autostart on login/boot**](autostart.md) — registering blanket as
  a background service (systemd user unit / launchd LaunchAgent /
  Task Scheduler entry), opting in at install time, and
  `blanket uninstall`.

## For maintainers

- [**Task flow**](task_flow.md) — task and worker state machines,
  end-to-end claim/execute lifecycle.
- [**Design**](design.md) — origin, design goals, architecture.

Planned work is tracked as [GitHub issues](https://github.com/turtlemonvh/blanket/issues), not in a docs file — see [issue #43](https://github.com/turtlemonvh/blanket/issues/43) for why.

## Where else to look

- [`../README.md`](../README.md) — install + quick start
- [`../CONTRIBUTORS.md`](../CONTRIBUTORS.md) — development setup,
  build, CI, release process
- [`../examples/types/`](../examples/types/) — copy-paste task type
  TOMLs (`echo_task`, `bash_task`, `python_hello`, `windows_echo`,
  `windows_powershell`)
