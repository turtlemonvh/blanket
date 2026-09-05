# Autostart on login/boot

blanket can register itself as a background service so the server
starts automatically on login (Linux/macOS) or logon (Windows),
without having to run `blanket` by hand every time. This is **off by
default** — opt in during install, or any time afterward with `blanket
service install`.

The service always runs the plain server (`blanket`, with the
`--config`/`--port` you resolved when you registered it) — not a
worker. Workers are started separately with host-specific capability
tags (`blanket worker -t exec:bash,os:unix`), so there's no single
"the worker" to autostart; run one yourself, or wire it into your own
init system, if you want a worker running unattended too.

## Enabling at install time

Both installers ask interactively (when run from a real terminal) or
accept an environment variable to decide up front:

```bash
# Linux/macOS
INSTALL_AUTOSTART=1 curl -sSfL https://raw.githubusercontent.com/turtlemonvh/blanket/master/scripts/install.sh | bash
```

```powershell
# Windows
$env:INSTALL_AUTOSTART = "1"
irm https://raw.githubusercontent.com/turtlemonvh/blanket/master/scripts/install.ps1 | iex
```

Set `INSTALL_AUTOSTART=0` to skip without being asked (useful for
scripted/offline installs — see
[offline_install.md](offline_install.md)). Leaving it unset asks only
if there's a real terminal to ask on; a non-interactive run (e.g.
`curl ... | bash` in CI) skips and prints the manual command instead.

## Enabling or removing it later

```bash
blanket service install     # register + start now
blanket service status      # show whether it's registered and running
blanket service uninstall   # stop + remove the registration
```

`blanket uninstall` also removes the autostart registration (it's the
same code path as `blanket service uninstall`), and additionally
prints the binary/config/data paths it deliberately leaves behind —
see "What `blanket uninstall` does and does not remove" below.

`blanket service install` uses the `--config`/`--port` this command
itself resolves — the same config file search
[the server uses](../README.md#quick-start) (or whatever `-c`/`-p` you
pass explicitly) — so the service runs against the exact config you
had active, not whatever a bare `blanket` invocation would default to
later.

## Per-platform mechanism

### Linux — systemd user unit

Writes `~/.config/systemd/user/blanket.service` (respecting
`XDG_CONFIG_HOME`) and runs:

```bash
systemctl --user daemon-reload
systemctl --user enable --now blanket.service
```

Logs go to `blanket-service.log` under blanket's data directory (see
the [README](../README.md#installation) for the exact path), via the
unit's `StandardOutput`/`StandardError`. `Restart=on-failure` restarts
the server if it crashes.

**If `systemctl --user` isn't usable** — no systemd, or systemd
present but no user session/D-Bus bus wired up (common under WSL and
in containers) — `blanket service install` still writes the unit file
and prints the manual command to run once a real session is available,
rather than failing the install.

### macOS — launchd LaunchAgent

Writes `~/Library/LaunchAgents/com.turtlemonvh.blanket.plist` and runs:

```bash
launchctl bootstrap gui/<uid> ~/Library/LaunchAgents/com.turtlemonvh.blanket.plist
```

`RunAtLoad` starts it at login; `KeepAlive` (`SuccessfulExit: false`)
restarts it if it exits unexpectedly. Logs go to the same
`blanket-service.log` path as Linux, via `StandardOutPath`/
`StandardErrorPath`.

**If there's no GUI login session to bootstrap into** (e.g. an SSH-only
session) the plist is still written and the manual `launchctl
bootstrap` command is printed, rather than failing the install.

### Windows — Scheduled Task

Registers a Scheduled Task named `Blanket` via:

```powershell
schtasks /Create /TN Blanket /TR "<blanket.exe> --config <config> --port <port>" /SC ONLOGON /RL LIMITED /F
```

`/RL LIMITED` runs it with standard (non-elevated) rights, matching a
normal interactive logon. `/F` makes re-running `blanket.exe service
install` idempotent (e.g. after moving the binary or changing the
port).

## What `blanket uninstall` does and does not remove

`blanket uninstall` is intentionally conservative: it removes **only**
the autostart registration created by `blanket service install`. It
does **not** remove:

- the blanket binary
- the config file/directory
- the data directory (task types, results, the BoltDB database)

`blanket uninstall` prints the paths it left behind so you can remove
them by hand if you want a full uninstall. This is deliberate — someone
re-running the install script later expects their task history and
task type library to still be there, and the service registration is
the one piece that's cheap and safe to fully automate removing.
