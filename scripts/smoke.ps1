# End-to-end smoke test for the built blanket binary, Windows edition.
#
# Mirrors scripts/smoke.sh's spirit (spin up the server on a scratch port
# against a throwaway config, exercise a handful of endpoints, tear
# everything down) but is intentionally smaller: it focuses on the
# Windows-only path scripts/smoke.sh can't cover — submitting and running
# a task through native Windows executors (cmd.exe / powershell.exe), via
# a real `blanket worker` process. It is NOT a port of smoke.sh; the
# Docker/Playwright/MCP surfaces smoke.sh also checks are exercised on
# Linux only (see CONTRIBUTORS.md's CI section).
#
# Usage (from the repo root, or anywhere — the script resolves paths off
# its own location):
#   pwsh scripts/smoke.ps1 [-Binary path\to\blanket.exe]
#
# If -Binary is omitted, it defaults to .\blanket-windows-amd64.exe next
# to the repo root (what `go build -o blanket-windows-amd64.exe .`
# produces).

param(
    [string]$Binary
)

$ErrorActionPreference = "Stop"

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

if (-not $Binary) {
    $Binary = Join-Path $RepoRoot "blanket-windows-amd64.exe"
}
if (-not (Test-Path $Binary)) {
    Write-Error "smoke: no blanket binary found at '$Binary'; build one first (go build -o blanket-windows-amd64.exe .)"
    exit 1
}
$Binary = (Resolve-Path $Binary).Path

$Port = 18774
$Base = "http://localhost:$Port"
$WorkDir = Join-Path ([System.IO.Path]::GetTempPath()) ("blanket-smoke-" + [System.Guid]::NewGuid().ToString("N").Substring(0, 8))
New-Item -ItemType Directory -Path $WorkDir -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $WorkDir "types") -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $WorkDir "results") -Force | Out-Null

$ServerProcess = $null
$WorkerProcess = $null
$ExitCode = 0

function Stop-IfRunning {
    param($Proc)
    if ($null -ne $Proc -and -not $Proc.HasExited) {
        try { Stop-Process -Id $Proc.Id -Force -ErrorAction SilentlyContinue } catch {}
    }
}

function Fail {
    param([string]$Message)

    # Stop the server and worker BEFORE reading their logs. Both are
    # Start-Process redirections, so output sits in a buffer the parent
    # cannot see until the child exits -- dumping first is why a real
    # failure (turtlemonvh/blanket#169) printed one line of server.log and
    # an empty worker.log, which is the half of a flake investigation that
    # actually costs attention.
    Stop-IfRunning $script:WorkerProcess
    Stop-IfRunning $script:ServerProcess
    Start-Sleep -Milliseconds 250

    foreach ($name in @("server.log", "worker.log", "worker.err.log")) {
        Write-Host "--- $name ---"
        $path = Join-Path $WorkDir $name
        if (Test-Path $path) { Get-Content $path | Write-Host } else { Write-Host "(absent)" }
    }
    throw "smoke: FAIL - $Message"
}

try {
    # Copy the fixture + Windows-native example task types into the
    # scratch types dir.
    $typesDir = Join-Path $WorkDir "types"
    $exampleTypesDir = Join-Path $RepoRoot "examples\types"
    Copy-Item (Join-Path $RepoRoot "testdata\types\echo_task.toml") (Join-Path $typesDir "echo_task.toml") -ErrorAction SilentlyContinue
    Copy-Item (Join-Path $exampleTypesDir "windows_echo.toml") (Join-Path $typesDir "windows_echo.toml")
    $powershellType = Join-Path $exampleTypesDir "windows_powershell.toml"
    $havePowershellType = Test-Path $powershellType
    if ($havePowershellType) {
        Copy-Item $powershellType (Join-Path $typesDir "windows_powershell.toml")
    }

    # JSON-escape backslashes in the Windows paths going into config.json
    # (String.Replace, not -replace, so this is a literal substitution —
    # no regex metacharacter surprises).
    $typesDirJson = $typesDir.Replace('\', '\\')
    $resultsDirJson = (Join-Path $WorkDir "results").Replace('\', '\\')
    $dbJson = (Join-Path $WorkDir "blanket.db").Replace('\', '\\')
    $config = @"
{
  "port": $Port,
  "database": "$dbJson",
  "tasks": {
    "typesPaths": ["$typesDirJson"],
    "resultsPath": "$resultsDirJson"
  },
  "logLevel": "warn"
}
"@
    $configPath = Join-Path $WorkDir "config.json"
    Set-Content -Path $configPath -Value $config -Encoding UTF8

    # Start the server.
    $serverLog = Join-Path $WorkDir "server.log"
    $serverErrLog = Join-Path $WorkDir "server.err.log"
    $ServerProcess = Start-Process -FilePath $Binary `
        -ArgumentList @("--config", $configPath) `
        -WorkingDirectory $WorkDir `
        -RedirectStandardOutput $serverLog `
        -RedirectStandardError $serverErrLog `
        -NoNewWindow -PassThru

    # Poll /version until the server is listening, or give up after ~10s.
    $ready = $false
    for ($i = 0; $i -lt 100; $i++) {
        if ($ServerProcess.HasExited) {
            Fail "server exited before becoming ready (exit code $($ServerProcess.ExitCode))"
        }
        try {
            $resp = Invoke-WebRequest -Uri "$Base/version" -UseBasicParsing -TimeoutSec 2
            if ($resp.StatusCode -eq 200) {
                $ready = $true
                break
            }
        } catch {
            # Not up yet.
        }
        Start-Sleep -Milliseconds 100
    }
    if (-not $ready) {
        Fail "server did not respond on $Base within 10s"
    }

    # /version returns JSON with a name field.
    $versionBody = (Invoke-WebRequest -Uri "$Base/version" -UseBasicParsing).Content
    if ($versionBody -notmatch '"name"') {
        Fail "/version missing name field: $versionBody"
    }

    # /ui/ serves the HTMX shell.
    $uiBody = (Invoke-WebRequest -Uri "$Base/ui/" -UseBasicParsing).Content
    if ($uiBody -notmatch '<title>Blanket') {
        Fail "/ui/ missing <title>Blanket"
    }

    # /task/ starts empty.
    $tasksBody = (Invoke-WebRequest -Uri "$Base/task/" -UseBasicParsing).Content
    if ($tasksBody.Trim() -ne "[]") {
        Fail "/task/ should start empty, got '$tasksBody'"
    }

    # Types to exercise: cmd always, powershell if the example exists.
    $typesToRun = @("windows_echo")
    if ($havePowershellType) {
        $typesToRun += "windows_powershell"
    }

    $taskIds = @{}
    foreach ($type in $typesToRun) {
        $body = @{ type = $type } | ConvertTo-Json -Compress
        $createResp = Invoke-RestMethod -Uri "$Base/task/" -Method Post -ContentType "application/json" -Body $body
        if ($createResp.state -ne "WAITING") {
            Fail "new $type task not WAITING: $($createResp | ConvertTo-Json -Compress)"
        }
        $taskIds[$type] = $createResp.id
    }

    # Start a worker able to claim os:windows-tagged tasks, and let it
    # drain the queue.
    $workerLog = Join-Path $WorkDir "worker.log"
    $workerErrLog = Join-Path $WorkDir "worker.err.log"
    $WorkerProcess = Start-Process -FilePath $Binary `
        -ArgumentList @("--config", $configPath, "worker", "--tags", "os:windows", "--checkinterval", "0.5") `
        -WorkingDirectory $WorkDir `
        -RedirectStandardOutput $workerLog `
        -RedirectStandardError $workerErrLog `
        -NoNewWindow -PassThru

    foreach ($type in $typesToRun) {
        $taskId = $taskIds[$type]
        $finalState = $null
        $task = $null
        $waitStart = Get-Date
        for ($i = 0; $i -lt 200; $i++) {
            if ($WorkerProcess.HasExited) {
                Fail "worker exited unexpectedly (exit code $($WorkerProcess.ExitCode)) while waiting on $type"
            }
            $task = Invoke-RestMethod -Uri "$Base/task/$taskId" -UseBasicParsing
            if ($task.state -in @("SUCCESS", "ERROR", "STOPPED", "TIMEDOUT")) {
                $finalState = $task.state
                break
            }
            Start-Sleep -Milliseconds 100
        }
        $waited = [math]::Round(((Get-Date) - $waitStart).TotalSeconds, 1)

        # How long the *task* ran, which is not the same as how long this
        # loop waited for it. Every task is submitted up front and the
        # worker drains the queue, so by the time the loop reaches the
        # second type it has often already finished -- the wait reads 0s
        # while the task itself took seconds. The budget is enforced
        # against this number (worker.go: StartedTs + Timeout), so this is
        # the one to compare against it.
        #
        # Unix seconds, so sub-second tasks read 0s. That is fine for what
        # this is watching: whether a task is creeping up on its timeout.
        $ran = "?"
        if ($task.startedTs -gt 0 -and $task.lastUpdatedTs -ge $task.startedTs) {
            $ran = "$($task.lastUpdatedTs - $task.startedTs)s"
        }

        if ($finalState -ne "SUCCESS") {
            Fail "task $taskId (type $type) did not reach SUCCESS (got '$finalState', ran $ran of a $($task.timeout)s budget, script waited ${waited}s, exitCode $($task.exitCode))"
        }

        # Printed on success, not just on failure, because the margin is
        # the thing worth watching: #169's Windows flake was a task
        # quietly running close to its timeout on a loaded runner until one
        # day it crossed. A number in every green log turns "it broke"
        # into "it had been drifting for weeks".
        Write-Host "smoke: $type -> SUCCESS (ran $ran of a $($task.timeout)s budget; script waited ${waited}s)"
    }

    Write-Host "smoke: OK"
} catch {
    Write-Error $_.Exception.Message
    $ExitCode = 1
} finally {
    Stop-IfRunning $WorkerProcess
    Stop-IfRunning $ServerProcess
    Remove-Item -Path $WorkDir -Recurse -Force -ErrorAction SilentlyContinue
}

exit $ExitCode
