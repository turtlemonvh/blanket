# Offline install

For machines with no internet access and only local-user permissions
(no package manager, no `git clone`, no outbound network). Everything
blanket does at install time — writing config, creating data
directories — is already local-only; the only network calls the
installers make are the binary download and (optionally) the example
task types. Both can be pointed at local files instead.

## 1. On a machine with internet access

Pick the release tag you want (e.g. `v0.2.0`) and grab three things,
all from that same tag so they match:

1. **The binary** — from the
   [Releases page](https://github.com/turtlemonvh/blanket/releases),
   download the asset for your OS: `blanket-linux-amd64`,
   `blanket-darwin-amd64`, or `blanket-windows-amd64.exe`.
2. **The install script** — browse the repo at that tag
   (`https://github.com/turtlemonvh/blanket/tree/v0.2.0/scripts`) and
   save the raw file for your platform: `install.sh` (Linux/macOS) or
   `install.ps1` (Windows). Don't use the `master` version — it may
   not match the release you downloaded.
3. **Example task types (optional)** — same tag, save the `*.toml`
   files under
   [`examples/types/`](../examples/types/) if you want them
   preinstalled.
4. **The `blanket-task-type` skill (optional)** — if you use Claude
   Code and want the task-type authoring skill, save
   [`.claude/skills/blanket-task-type/SKILL.md`](../.claude/skills/blanket-task-type/SKILL.md)
   at that same tag, keeping it under a `blanket-task-type/`
   subdirectory locally (the installer copies the whole subdirectory,
   matching where Claude Code expects to find it).

## 2. Move everything to the offline machine

Copy the binary, the install script, and (optionally) the `.toml`
files over — USB drive, internal file share, whatever's available.
No `git clone` required.

## 3. Run the installer with local sources

Both installers accept environment variables that skip their network
calls in favor of local paths:

- `BINARY_PATH` — path to the binary you downloaded; skips OS/arch
  detection, the GitHub releases API lookup, and the download.
- `TYPES_SRC` — a local directory of `*.toml` files; copied in place
  of downloading `examples/types/` from GitHub.
- `SKILLS_SRC` — a local directory containing a `blanket-task-type/`
  subdirectory with `SKILL.md`; copied in place of downloading it from
  GitHub. Only consulted if the installer detects a supported agent
  harness (currently Claude Code) on `$PATH` and you opt into
  installing the skill — see below.

An offline machine also has no controlling terminal to prompt on in
the usual case (or does, but you'd rather not be asked), so set
`INSTALL_SKILLS=1` or `INSTALL_SKILLS=0` to decide up front instead of
relying on the interactive prompt. Same goes for
`INSTALL_SHELL_INTEGRATION` — see [README](../README.md#installation)
for what it does (PATH + shell completion via a marked block in your
shell's rc file); set it to `1` or `0` rather than leaving it to the
prompt.

**Linux / macOS:**

```bash
BINARY_PATH=./blanket-linux-amd64 TYPES_SRC=./types \
  INSTALL_SKILLS=1 SKILLS_SRC=./skills \
  INSTALL_SHELL_INTEGRATION=1 \
  sh install.sh
```

**Windows (PowerShell):**

```powershell
$env:BINARY_PATH = ".\blanket-windows-amd64.exe"
$env:TYPES_SRC = ".\types"
$env:INSTALL_SKILLS = "1"
$env:SKILLS_SRC = ".\skills"
$env:INSTALL_SHELL_INTEGRATION = "1"
.\install.ps1
```

Everything else — config generation, `~/.local/bin` (or
`%LOCALAPPDATA%\blanket\bin`) placement, data directory layout — is
unchanged and already offline-safe. See the main
[README](../README.md#installation) for the default paths.

If you skip `TYPES_SRC`, the installer will still try to download
example types and print `warn: could not download ...` for each one;
that's expected on an offline machine and safe to ignore — blanket
runs fine with zero task types installed.
