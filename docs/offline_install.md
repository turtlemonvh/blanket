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

**Linux / macOS:**

```bash
BINARY_PATH=./blanket-linux-amd64 TYPES_SRC=./types sh install.sh
```

**Windows (PowerShell):**

```powershell
$env:BINARY_PATH = ".\blanket-windows-amd64.exe"
$env:TYPES_SRC = ".\types"
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
