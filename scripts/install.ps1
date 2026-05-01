# Install blanket — downloads the latest (or pinned) release binary for Windows.
#
# Usage:
#   irm https://raw.githubusercontent.com/turtlemonvh/blanket/master/scripts/install.ps1 | iex
#
# Environment variables:
#   VERSION      — tag to install (default: latest release, e.g. v0.1.0)
#   INSTALL_DIR  — directory to place the binary (default: current directory)

$ErrorActionPreference = "Stop"
$Repo = "turtlemonvh/blanket"
$Binary = "blanket-windows-amd64.exe"

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

$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { "." }
$Url = "https://github.com/$Repo/releases/download/$Version/$Binary"
$OutFile = Join-Path $InstallDir "blanket.exe"

Write-Host "Installing blanket $Version (windows/amd64) to $OutFile ..."

if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}

try {
    Invoke-WebRequest -Uri $Url -OutFile $OutFile -UseBasicParsing
} catch {
    Remove-Item -Path $OutFile -ErrorAction SilentlyContinue
    Write-Error "Download failed. Check that release $Version exists: https://github.com/$Repo/releases"
    exit 1
}

Write-Host "Done. Run '$OutFile --help' to get started."
