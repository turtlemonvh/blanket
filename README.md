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

The installers download the binary, create config/data directories,
write a default config file, and fetch the example task types. Set
`INSTALL_DIR` to override the binary location, or `VERSION=v0.1.0`
to pin a release.

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
blanket worker -t bash,unix
```

That's it. For curl examples, file uploads, scripting, custom task
types, and the full REST API, see the [docs](docs/README.md):

- [**Usage**](docs/usage.md) — submitting tasks, file uploads, the
  CLI, managing tasks at scale
- [**Task type definitions**](docs/task_type_definitions.md) — TOML
  schema for authoring your own task types
- [**API reference**](docs/api.md) — full list of REST endpoints
- [**Task flow**](docs/task_flow.md) — task and worker state machines

## Contributing

See [CONTRIBUTORS.md](CONTRIBUTORS.md) for development setup, build
instructions, CI details, and code conventions.
