# Install blanket — downloads the latest (or pinned) release binary for Windows,
# creates config/data directories under %LOCALAPPDATA%, and downloads example
# task types.
#
# Usage:
#   irm https://raw.githubusercontent.com/turtlemonvh/blanket/master/scripts/install.ps1 | iex
#
# Environment variables:
#   VERSION        — tag to install (default: latest release, e.g. v0.1.0)
#   INSTALL_DIR    — directory to place the binary (default: %LOCALAPPDATA%\blanket\bin)
#   BINARY_PATH    — path to an already-downloaded binary; skips the release
#                    download entirely (for offline installs, see
#                    docs/offline_install.md)
#   TYPES_SRC      — local directory of *.toml task types to copy instead
#                    of downloading examples/types/ from GitHub
#   INSTALL_SKILLS — 1 to install the blanket-task-type Claude Code skill
#                    without asking, 0 to skip without asking. Unset means
#                    "ask, but only in an interactive console" — see the
#                    "AI agent skill" section below.
#   SKILLS_SRC     — local directory containing a blanket-task-type\
#                    subdirectory with SKILL.md, copied instead of
#                    downloading it from GitHub (offline installs)
#   INSTALL_AUTOSTART — 1 to register blanket as a Scheduled Task that
#                    starts it at logon, without asking; 0 to skip without
#                    asking. Unset means "ask, but only in an interactive
#                    console" — same rule as INSTALL_SKILLS. Off by
#                    default. See docs/autostart.md; the registration
#                    itself is `blanket.exe service install`
#                    (`blanket.exe service uninstall` / `blanket.exe
#                    uninstall` removes it).
#   INSTALL_SHELL_INTEGRATION — 1 to add INSTALL_DIR to PATH and enable
#                    PowerShell completion via a marked block in $PROFILE,
#                    without asking; 0 to skip without asking (and remove
#                    any block a previous run added). Unset means "ask,
#                    but only in an interactive console" — see the "Shell
#                    integration" section below.

$ErrorActionPreference = "Stop"
$Repo = "turtlemonvh/blanket"
$RawBase = "https://raw.githubusercontent.com/$Repo/master"
$Binary = "blanket-windows-amd64.exe"
$ExampleTypes = @("echo_task.toml", "bash_task.toml", "python_hello.toml", "windows_echo.toml", "windows_powershell.toml")

# Agent harnesses this script knows how to install the skill for, and
# where each one looks for skills. Extend this as other harnesses' skill
# directory conventions are confirmed — codex and others weren't wired up
# here because their layout isn't verified yet.
function Get-SkillDest {
    if (Get-Command claude -ErrorAction SilentlyContinue) {
        return Join-Path $env:USERPROFILE ".claude\skills"
    }
    return $null
}

# Determine version
if ($env:BINARY_PATH) {
    if (-not (Test-Path $env:BINARY_PATH)) {
        Write-Error "BINARY_PATH '$($env:BINARY_PATH)' does not exist."
        exit 1
    }
    $Version = "local"
} elseif (-not $env:VERSION) {
    $release = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
    $Version = $release.tag_name
    if (-not $Version) {
        Write-Error "Could not determine latest release. Set `$env:VERSION explicitly."
        exit 1
    }
} else {
    $Version = $env:VERSION
}

# Resolve directories
$BlanketRoot = Join-Path $env:LOCALAPPDATA "blanket"
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { Join-Path $BlanketRoot "bin" }
$ConfigDir = $BlanketRoot
$TypesDir = Join-Path $BlanketRoot "types"
$ResultsDir = Join-Path $BlanketRoot "results"

$Url = "https://github.com/$Repo/releases/download/$Version/$Binary"
$OutFile = Join-Path $InstallDir "blanket.exe"

Write-Host "Installing blanket $Version (windows/amd64) ..."
Write-Host "  binary:  $OutFile"
Write-Host "  config:  $ConfigDir\"
Write-Host "  data:    $BlanketRoot\"
Write-Host ""

# Download binary
foreach ($dir in @($InstallDir, $TypesDir, $ResultsDir)) {
    if (-not (Test-Path $dir)) {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
    }
}

if ($env:BINARY_PATH) {
    Copy-Item -Path $env:BINARY_PATH -Destination $OutFile -Force
} else {
    try {
        Invoke-WebRequest -Uri $Url -OutFile $OutFile -UseBasicParsing
    } catch {
        Remove-Item -Path $OutFile -ErrorAction SilentlyContinue
        Write-Error "Download failed. Check that release $Version exists: https://github.com/$Repo/releases"
        exit 1
    }
}

# Write default config if not present
$ConfigFile = Join-Path $ConfigDir "config.json"
if (-not (Test-Path $ConfigFile)) {
    $TypesAbs = (Resolve-Path $TypesDir).Path
    $ResultsAbs = (Resolve-Path $ResultsDir).Path
    $DataAbs = (Resolve-Path $BlanketRoot).Path
    $config = @{
        port = 8773
        database = (Join-Path $DataAbs "blanket.db")
        tasks = @{
            typesPaths = @($TypesAbs)
            resultsPath = $ResultsAbs
        }
        logLevel = "info"
    } | ConvertTo-Json -Depth 3
    Set-Content -Path $ConfigFile -Value $config -Encoding UTF8
    Write-Host "Created default config: $ConfigFile"
} else {
    Write-Host "Config already exists, skipping: $ConfigFile"
}

# Install example task types (skip existing files)
Write-Host ""
if ($env:TYPES_SRC) {
    if (-not (Test-Path $env:TYPES_SRC -PathType Container)) {
        Write-Error "TYPES_SRC '$($env:TYPES_SRC)' is not a directory."
        exit 1
    }
    $typeFiles = (Get-ChildItem -Path $env:TYPES_SRC -Filter "*.toml").Name
} else {
    $typeFiles = $ExampleTypes
}

foreach ($typeFile in $typeFiles) {
    $dest = Join-Path $TypesDir $typeFile
    if (Test-Path $dest) {
        Write-Host "  skip (exists): $typeFile"
        continue
    }

    if ($env:TYPES_SRC) {
        Copy-Item -Path (Join-Path $env:TYPES_SRC $typeFile) -Destination $dest
    } else {
        $typeUrl = "$RawBase/examples/types/$typeFile"
        try {
            Invoke-WebRequest -Uri $typeUrl -OutFile $dest -UseBasicParsing
        } catch {
            Write-Host "  warn: could not download $typeFile"
            continue
        }
    }

    # Check if executor is available
    $executorLine = Select-String -Path $dest -Pattern '^executor' | Select-Object -First 1
    $executor = "bash"
    if ($executorLine) {
        if ($executorLine.Line -match '"([^"]+)"') {
            $executor = $Matches[1]
        }
    }
    $found = Get-Command $executor -ErrorAction SilentlyContinue
    if ($found) {
        Write-Host "  installed: $typeFile (executor: $executor)"
    } else {
        Write-Host "  installed: $typeFile (warning: executor '$executor' not found on PATH)"
    }
}

# AI agent skill — only offered if a supported agent harness (currently
# just Claude Code) is on PATH. `irm ... | iex` runs non-interactively, so
# INSTALL_SKILLS lets a scripted install opt in or out explicitly;
# otherwise we only prompt in a real interactive console, and default to
# skipping if it isn't one.
$SkillDestRoot = Get-SkillDest
if ($SkillDestRoot) {
    Write-Host ""
    $doInstallSkill = $false
    if ($env:INSTALL_SKILLS -eq "1") {
        $doInstallSkill = $true
    } elseif ($env:INSTALL_SKILLS -eq "0") {
        $doInstallSkill = $false
    } elseif (-not [Console]::IsInputRedirected) {
        $reply = Read-Host "Install the blanket-task-type Claude Code skill (helps author task types)? [y/N]"
        $doInstallSkill = ($reply -match '^(y|yes)$')
    }

    if ($doInstallSkill) {
        $skillDest = Join-Path $SkillDestRoot "blanket-task-type"
        if (Test-Path $skillDest) {
            Write-Host "  skip (exists): blanket-task-type skill already at $skillDest"
        } else {
            New-Item -ItemType Directory -Path $skillDest -Force | Out-Null
            $skillFile = Join-Path $skillDest "SKILL.md"
            if ($env:SKILLS_SRC) {
                Copy-Item -Path (Join-Path $env:SKILLS_SRC "blanket-task-type\SKILL.md") -Destination $skillFile
                Write-Host "  installed: blanket-task-type skill -> $skillFile"
            } else {
                $skillUrl = "$RawBase/.claude/skills/blanket-task-type/SKILL.md"
                try {
                    Invoke-WebRequest -Uri $skillUrl -OutFile $skillFile -UseBasicParsing
                    Write-Host "  installed: blanket-task-type skill -> $skillFile"
                } catch {
                    Remove-Item -Path $skillDest -Recurse -ErrorAction SilentlyContinue
                    Write-Host "  warn: could not download blanket-task-type skill"
                }
            }
        }
    }
}

# ---------------------------------------------------------------------------
# Autostart on login/boot (issue #59) — optional Scheduled Task
# registration so blanket starts automatically instead of needing to be
# run by hand each time. Off by default. The registration logic itself
# lives in Go ("blanket.exe service install" — a Task Scheduler entry via
# schtasks; see command/service_windows.go and docs/autostart.md) so it's
# testable independent of this script; this block only decides whether to
# invoke it, following the same 1/0/unset convention as INSTALL_SKILLS
# above. Kept as one contiguous section near the end of the script to
# minimize merge conflicts with other in-flight edits to this file.
Write-Host ""
$doInstallAutostart = $false
if ($env:INSTALL_AUTOSTART -eq "1") {
    $doInstallAutostart = $true
} elseif ($env:INSTALL_AUTOSTART -eq "0") {
    $doInstallAutostart = $false
} elseif (-not [Console]::IsInputRedirected) {
    $reply = Read-Host "Start blanket automatically at logon (Task Scheduler)? [y/N]"
    $doInstallAutostart = ($reply -match '^(y|yes)$')
}

if ($doInstallAutostart) {
    & $OutFile --config $ConfigFile service install
} else {
    Write-Host "Skipping autostart registration. Enable it later with:"
    Write-Host "  $OutFile --config $ConfigFile service install"
}

# Shell integration (issue #22): opt-in PATH + completion setup for
# PowerShell, mirroring the install.sh block below for bash/zsh/fish.
#
# Appends a clearly delimited, idempotent block to $PROFILE (the current
# user's PowerShell profile) that (a) adds InstallDir to PATH for future
# sessions if it isn't already there, and (b) loads blanket's PowerShell
# completion. Re-running install replaces the block in place (matched by
# the marker lines below) rather than duplicating it.
#
# Consent follows the same pattern as INSTALL_SKILLS above:
#   INSTALL_SHELL_INTEGRATION=1 — do it without asking
#   INSTALL_SHELL_INTEGRATION=0 — skip without asking, and remove any block
#                                  a previous run added (uninstall)
#   unset                       — prompt only in an interactive console,
#                                  otherwise skip and print manual steps
# ---------------------------------------------------------------------------
$BlockStart = "# >>> blanket >>>"
$BlockEnd = "# <<< blanket <<<"

function Get-ShellBlock {
    param([string]$InstallDir)
    @"
$BlockStart
# Added by blanket's install.ps1: PATH entry + shell completion.
# To remove: delete the lines between the markers above and below, or
# re-run the installer with `$env:INSTALL_SHELL_INTEGRATION = "0".
if (`$env:PATH -split ";" -notcontains "$InstallDir") {
    `$env:PATH = "$InstallDir;" + `$env:PATH
}
if (Get-Command blanket.exe -ErrorAction SilentlyContinue) {
    blanket.exe completion powershell | Out-String | Invoke-Expression
}
$BlockEnd
"@
}

function Remove-ShellBlock {
    param([string]$ProfilePath)
    if (-not (Test-Path $ProfilePath)) { return }
    $lines = @(Get-Content -Path $ProfilePath)
    if (-not ($lines -contains $BlockStart)) { return }
    $result = New-Object System.Collections.Generic.List[string]
    $skip = $false
    foreach ($line in $lines) {
        if (-not $skip -and $line -eq $BlockStart) { $skip = $true; continue }
        if ($skip -and $line -eq $BlockEnd) { $skip = $false; continue }
        if ($skip) { continue }
        $result.Add($line)
    }
    # Trim trailing blank lines (the block is always appended at EOF, so
    # any left at EOF now were the separator we added before it).
    while ($result.Count -gt 0 -and $result[$result.Count - 1] -eq "") {
        $result.RemoveAt($result.Count - 1)
    }
    Set-Content -Path $ProfilePath -Value $result -Encoding UTF8
}

function Write-ShellBlock {
    param([string]$ProfilePath, [string]$InstallDir)
    $profileDir = Split-Path -Path $ProfilePath -Parent
    if ($profileDir -and -not (Test-Path $profileDir)) {
        New-Item -ItemType Directory -Path $profileDir -Force | Out-Null
    }
    if (-not (Test-Path $ProfilePath)) {
        New-Item -ItemType File -Path $ProfilePath -Force | Out-Null
    }
    Remove-ShellBlock -ProfilePath $ProfilePath
    $existing = Get-Content -Path $ProfilePath -Raw -ErrorAction SilentlyContinue
    if ($existing -and $existing.Trim().Length -gt 0) {
        Add-Content -Path $ProfilePath -Value ""
    }
    Add-Content -Path $ProfilePath -Value (Get-ShellBlock -InstallDir $InstallDir)
}

Write-Host ""
$doShellIntegration = $false
if ($env:INSTALL_SHELL_INTEGRATION -eq "1") {
    $doShellIntegration = $true
} elseif ($env:INSTALL_SHELL_INTEGRATION -eq "0") {
    $doShellIntegration = $false
} elseif (-not [Console]::IsInputRedirected) {
    $reply = Read-Host "Add $InstallDir to PATH and enable PowerShell completion in `$PROFILE ($PROFILE)? [y/N]"
    $doShellIntegration = ($reply -match '^(y|yes)$')
}

if ($doShellIntegration) {
    Write-ShellBlock -ProfilePath $PROFILE -InstallDir $InstallDir
    Write-Host "Updated $PROFILE : added PATH entry + PowerShell completion for blanket"
    Write-Host "  (marked block between '$BlockStart' and '$BlockEnd')."
    Write-Host "  Start a new PowerShell session, or run: . `$PROFILE"
    Write-Host "  To undo: remove that block, or re-run with `$env:INSTALL_SHELL_INTEGRATION = `"0`""
} elseif ($env:INSTALL_SHELL_INTEGRATION -eq "0") {
    Remove-ShellBlock -ProfilePath $PROFILE
    Write-Host "Skipped shell integration (`$env:INSTALL_SHELL_INTEGRATION = `"0`"); any earlier block in $PROFILE was removed."
} else {
    Write-Host "Skipped shell integration. To add PATH + completion yourself, append this to `$PROFILE ($PROFILE):"
    (Get-ShellBlock -InstallDir $InstallDir) -split "`n" | ForEach-Object { Write-Host "  $_" }
    Write-Host "Or re-run this installer with `$env:INSTALL_SHELL_INTEGRATION = `"1`"."
}

# Fallback PATH note for the current session, independent of whether the
# $PROFILE integration above ran (that only takes effect in new sessions).
Write-Host ""
$pathDirs = $env:PATH -split ";"
if ($pathDirs -notcontains $InstallDir) {
    Write-Host "Note: $InstallDir is not on your current session's PATH yet. For this session:"
    Write-Host "  `$env:PATH = `"$InstallDir;`$env:PATH`""
    Write-Host "  # Or permanently via System Properties > Environment Variables"
    Write-Host ""
}

Write-Host "Done! Run 'blanket.exe --help' to get started."
Write-Host "The server will use config from: $ConfigFile"
