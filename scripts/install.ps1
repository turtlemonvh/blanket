# Install blanket — downloads the latest (or pinned) release binary for Windows,
# creates config/data directories under %LOCALAPPDATA%, and downloads example
# task types.
#
# Usage:
#   irm https://raw.githubusercontent.com/turtlemonvh/blanket/master/scripts/install.ps1 | iex
#
# Environment variables:
#   VERSION      — tag to install (default: latest release, e.g. v0.1.0)
#   INSTALL_DIR  — directory to place the binary (default: %LOCALAPPDATA%\blanket\bin)

$ErrorActionPreference = "Stop"
$Repo = "turtlemonvh/blanket"
$RawBase = "https://raw.githubusercontent.com/$Repo/master"
$Binary = "blanket-windows-amd64.exe"
$ExampleTypes = @("echo_task.toml", "bash_task.toml", "python_hello.toml", "windows_echo.toml")

# Determine version
if (-not $env:VERSION) {
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

try {
    Invoke-WebRequest -Uri $Url -OutFile $OutFile -UseBasicParsing
} catch {
    Remove-Item -Path $OutFile -ErrorAction SilentlyContinue
    Write-Error "Download failed. Check that release $Version exists: https://github.com/$Repo/releases"
    exit 1
}

# Write default config if not present
$ConfigFile = Join-Path $ConfigDir "config.json"
if (-not (Test-Path $ConfigFile)) {
    $TypesAbs = (Resolve-Path $TypesDir).Path
    $ResultsAbs = (Resolve-Path $ResultsDir).Path
    $config = @{
        port = 8773
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

# Download example task types (skip existing files)
Write-Host ""
foreach ($typeFile in $ExampleTypes) {
    $dest = Join-Path $TypesDir $typeFile
    if (Test-Path $dest) {
        Write-Host "  skip (exists): $typeFile"
        continue
    }

    $typeUrl = "$RawBase/examples/types/$typeFile"
    try {
        Invoke-WebRequest -Uri $typeUrl -OutFile $dest -UseBasicParsing
    } catch {
        Write-Host "  warn: could not download $typeFile"
        continue
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

# PATH hint
Write-Host ""
$pathDirs = $env:PATH -split ";"
if ($pathDirs -notcontains $InstallDir) {
    Write-Host "Note: $InstallDir is not on your PATH. Add it with:"
    Write-Host "  `$env:PATH = `"$InstallDir;`$env:PATH`""
    Write-Host "  # Or permanently via System Properties > Environment Variables"
    Write-Host ""
}

Write-Host "Done! Run 'blanket.exe --help' to get started."
Write-Host "The server will use config from: $ConfigFile"
