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

The install script adds blanket to your `PATH`, sets up configuration,
loads default task types, and offers to set blanket up to run on boot
and to install AI-agent skills for drafting task types. See
[**installation**](docs/install.md) for the environment variables that
control each of those, and where files land.

**No `sudo` or administrator rights are needed** for a default install
— everything goes under your home directory. Blanket is designed to be
easy to set up inside organisations with a semi-paranoid security
culture.

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

Upgrades are checksum-verified, take a database backup first, keep the
binary they replace so `blanket rollback` can put it back, and restart
the running server without losing the work in flight. See
[**upgrading**](docs/upgrade.md).

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

## License

[MIT](LICENSE.md).
