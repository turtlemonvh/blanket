# Blanket Docs

Reference documentation for blanket. The top-level
[README](../README.md) covers install and a 60-second start; the pages
here go deeper.

## For users

- [**Usage**](usage.md) — submitting tasks, file uploads, the CLI,
  managing tasks at scale, and the curl/HTTP examples.
- [**Task type definitions**](task_type_definitions.md) — the TOML
  schema, field by field, and the `blanket task-validate` checks.
- [**Authoring task types**](authoring_task_types.md) — how to write a
  good task type: sizing, `{{.VAR}}` vs `$VAR`, common patterns, the
  validate-and-iterate loop. Also the reference an AI agent is pointed
  at.
- [**Tag ontology**](tag_ontology.md) — how tags match tasks to
  workers, the namespaced tag convention, and how to extend the
  vocabulary.
- [**API**](api.md) — every REST endpoint.
- [**MCP interface**](mcp.md) — tool list, setup (including Claude
  Code), permissions, and the default security posture.
- [**Installation**](install.md) — what the install scripts do, the
  `INSTALL_*` environment variables, shell integration, where files
  land, and why no `sudo` is needed.
- [**Offline install**](offline_install.md) — installing and upgrading
  from the release bundle on machines with no internet access and only
  local-user permissions.
- [**Autostart on login/boot**](autostart.md) — registering blanket as a
  background service (systemd user unit / launchd LaunchAgent / Task
  Scheduler entry), opting in at install time, and `blanket uninstall`.
- [**Upgrading**](upgrade.md) — `blanket upgrade` / `blanket rollback`,
  checksum verification and offline bundles, rollback slots and the
  upgrade journal, database schema versions, backups and retention,
  `blanket backup` / `blanket migrate`, the restart state machine and
  the `curl` recipe that drives it, `--exec-mode` / `--drain-mode`, and
  what to do when a migration or a restart dies half-way.

## Examples

Integration recipes: running blanket alongside something else.

- [**Tailscale**](examples/tailscale.md) — putting blanket on a tailnet
  with HTTPS and a real name, via `tailscale serve` or a Tailscale
  Service, and why Funnel is the wrong tool for it.

## For maintainers

- [**Task flow**](task_flow.md) — task and worker state machines, the
  end-to-end claim/execute lifecycle.
- [**Design**](design.md) — origin, design goals, architecture.
- [**Authentication design brief**](design/auth.md) — proposed design
  for tokens, scopes, UI login, and worker auth. Not yet implemented.

Planned work is tracked as
[GitHub issues](https://github.com/turtlemonvh/blanket/issues), not in a
docs file.

## Where else to look

- [`../README.md`](../README.md) — install and quick start
- [`../CONTRIBUTORS.md`](../CONTRIBUTORS.md) — development setup, build,
  CI, release process
- [`../examples/types/`](../examples/types/) — copy-paste task type
  TOMLs (`echo_task`, `bash_task`, `python_hello`, `windows_echo`,
  `windows_powershell`, `docker_run`, `claude_code`, `codex`)
