# Blanket

Blanket is a RESTy wrapper for long-running tasks. Define task types as
TOML files, submit them via REST API or CLI, and let workers execute
them — all from a single binary with a built-in web UI.

## Installation

Download a pre-built binary from
[GitHub Releases](https://github.com/turtlemonvh/blanket/releases),
or use one of the one-liner installers below.

**Linux / macOS:**

```bash
curl -sSfL https://raw.githubusercontent.com/turtlemonvh/blanket/master/scripts/install.sh | bash
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/turtlemonvh/blanket/master/scripts/install.ps1 | iex
```

The installers download the binary (verified against the release's
`SHA256SUMS`, then renamed into place so a failed download never leaves a
half-written binary behind), create config/data directories, write a
default config file, and fetch the example task types. Set `INSTALL_DIR`
to override the binary location, or `VERSION=v0.1.0` to pin a release. If Claude Code is on your `$PATH`, they'll also
offer to install the `blanket-task-type` authoring skill — set
`INSTALL_SKILLS=1` (or `0`) to decide without being prompted.

They'll also offer to register blanket as a background service that
starts on login/boot (off by default) — set `INSTALL_AUTOSTART=1` (or
`0`) to decide without being prompted, or run `blanket service install`
any time afterward. See [autostart](docs/autostart.md) for details and
`blanket uninstall`.

They'll also offer to add the binary to `PATH` and enable tab
completion — bash, zsh, and fish on Linux/macOS, PowerShell on
Windows — by appending a clearly marked block to your shell's rc file
(`~/.bashrc`, `~/.zshrc`, `~/.config/fish/config.fish`, or
`$PROFILE`), the same pattern nvm and conda use. Re-running the
installer updates that block in place instead of duplicating it. Set
`INSTALL_SHELL_INTEGRATION=1` (or `0`) to decide without being
prompted; `0` also removes a block a previous run added.

No internet access, or only local-user permissions? Each release
attaches `blanket-bundle-<version>.tar.gz` — binaries, checksums,
example task types and the install scripts in one file. See
[**offline install**](docs/offline_install.md).

### Upgrading

```bash
blanket upgrade --check      # is a newer release available?
blanket upgrade --yes        # install it and restart the server onto it
blanket rollback --yes       # change your mind
```

`blanket upgrade` verifies the download against the release's
`SHA256SUMS`, keeps the binary it replaces so `blanket rollback` can put
it back, takes a database backup first, and restarts the running server
onto the new binary without losing the work in flight. `--print-plan`
shows the exact steps, `--bundle` installs from an offline bundle, and
`--stage-only` / `--no-restart` split it up. See
[**upgrading**](docs/upgrade.md).

| | Binary | Config | Data |
|---|---|---|---|
| Linux/macOS | `~/.local/bin/blanket` | `~/.config/blanket/` | `~/.local/share/blanket/` |
| Windows | `%LOCALAPPDATA%\blanket\bin\blanket.exe` | `%LOCALAPPDATA%\blanket\` | `%LOCALAPPDATA%\blanket\` |

## Quick start

```bash
# Start the server (uses the install-script-generated config by default)
blanket
```

Open the web UI at [http://localhost:8773/](http://localhost:8773/).

```bash
# Submit a task — over REST or via the CLI
curl -s -X POST localhost:8773/task/ -d '{"type": "echo_task"}'
blanket submit -t echo_task

# Run a worker that accepts bash/unix tasks
blanket worker -t exec:bash,os:unix
```

That's it. For curl examples, file uploads, scripting, custom task
types, and the full REST API, see the [docs](docs/README.md):

- [**Usage**](docs/usage.md) — submitting tasks, file uploads, the
  CLI, managing tasks at scale
- [**Task type definitions**](docs/task_type_definitions.md) — TOML
  schema for authoring your own task types
- [**API reference**](docs/api.md) — full list of REST endpoints
- [**Autostart**](docs/autostart.md) — running blanket as a background
  service that starts on login/boot, and `blanket uninstall`
- [**Task flow**](docs/task_flow.md) — task and worker state machines
- [**Upgrading**](docs/upgrade.md) — `blanket upgrade` / `blanket
  rollback`, checksums and offline bundles, schema versions, backups,
  `blanket backup` / `blanket migrate`
- [**MCP interface**](docs/mcp.md) — expose blanket to MCP clients (agents),
  security considerations, and setup

## Contributing

See [CONTRIBUTORS.md](CONTRIBUTORS.md) for development setup, build
instructions, CI details, and code conventions.
