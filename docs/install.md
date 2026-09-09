# Installation

How the install scripts work, what they change, and how to control or
undo each part. This is the reference behind the one-liner installers.

For machines with no internet access or no admin rights, see
[offline install](offline_install.md).

## What the installers do

`scripts/install.sh` (Linux/macOS) and `scripts/install.ps1` (Windows)
each:

1. Download the binary for your platform and **verify it against the
   release's `SHA256SUMS`**, then rename it into place — a failed or
   truncated download never leaves a half-written binary behind.
2. Create the config and data directories.
3. Write a default config file.
4. Fetch the example task types.
5. Offer three opt-in extras, described below: shell integration,
   autostart, and AI-agent skills.

No `sudo` or administrator rights are needed for a default install.
Everything lands under your home directory, which is deliberate:
blanket is meant to be easy to install inside organisations with a
semi-paranoid security culture.

## Where files land

| | Binary | Config | Data |
|---|---|---|---|
| Linux/macOS | `~/.local/bin/blanket` | `~/.config/blanket/` | `~/.local/share/blanket/` |
| Windows | `%LOCALAPPDATA%\blanket\bin\blanket.exe` | `%LOCALAPPDATA%\blanket\` | `%LOCALAPPDATA%\blanket\` |

## Environment variables

Each prompt can be answered ahead of time. This matters on a machine
with no controlling terminal — CI, a provisioning script, or an offline
box — where you would rather not be asked at all.

| Variable | Effect |
|---|---|
| `INSTALL_DIR` | Override where the binary is placed. |
| `VERSION` | Pin a release, e.g. `VERSION=v0.1.0`. Defaults to the latest. |
| `INSTALL_SHELL_INTEGRATION` | `1` adds `PATH` + tab completion; `0` skips, and also removes a block a previous run added. |
| `INSTALL_AUTOSTART` | `1` registers blanket to start on login/boot; `0` skips. Off by default. |
| `INSTALL_SKILLS` | `1` installs the `blanket-task-type` authoring skill; `0` skips. Only offered when a supported agent harness is found. |

## Shell integration

Opt-in. Adds the binary to your `PATH` and enables tab completion —
bash, zsh and fish on Linux/macOS, PowerShell on Windows — by appending
a clearly marked block to your shell's rc file (`~/.bashrc`,
`~/.zshrc`, `~/.config/fish/config.fish`, or `$PROFILE`). This is the
same pattern nvm and conda use.

Re-running the installer **updates that block in place** rather than
appending a second copy. Setting `INSTALL_SHELL_INTEGRATION=0` removes
a block an earlier run added.

## Autostart

Opt-in and off by default. Registers blanket as a background service
that starts on login or boot — a systemd user unit, a launchd
LaunchAgent, or a Task Scheduler entry depending on the platform.

You can also run `blanket service install` at any point afterwards.
See [autostart](autostart.md) for the details and for
`blanket uninstall`.

## AI-agent skills

If a supported agent harness (currently Claude Code) is found on your
`$PATH`, the installer offers to install the `blanket-task-type`
skill, which helps an agent draft and validate task type definitions.
Set `INSTALL_SKILLS=1` or `0` to decide without being prompted.

## Upgrading and removal

`blanket upgrade` handles upgrades in place, with checksum
verification, a database backup, and a rollback slot — see
[upgrading](upgrade.md). `blanket uninstall` reverses the autostart
registration; see [autostart](autostart.md).
