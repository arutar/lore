<#
.SYNOPSIS
Runs the Windows client's checks against a live Lore Platform installation: the Companion
scenario tests (every operation, with a second person working at the same time) and, with
-Explorer, the same operations driven through the Explorer context menu.

.DESCRIPTION
Run it on a Windows machine with the Lore client installed, in the desktop session of a user
whose saved Lore sign-in is an administrator of the installation (the tests create repositories
and a temporary second user through the WebUI's API). See README.md beside this script.

The administrator's password is read from -EnvFile (the installation's .env, LORE_ADMIN_PASSWORD)
or from LORE_LIVE_ADMIN_PASSWORD; it is never printed.

.EXAMPLE
pwsh -File platform/windows/tests/Run-ClientTests.ps1 -Web http://lore.example.org:8080 -EnvFile D:\lore\platform\deploy\.env -Explorer
#>
param(
    [Parameter(Mandatory)] [string] $Web,
    # The Lore server; by default the WebUI's host name with lore://.
    [string] $Server = '',
    [string] $EnvFile = '',
    [string] $Root = (Join-Path $env:TEMP 'lore-client-tests'),
    # A command that restarts the Lore server, e.g. "docker restart deploy-lore-1"; enables the
    # "locks survive a restart" checks.
    [string] $RestartCommand = '',
    [switch] $Explorer,
    [string] $Lore = 'C:\Program Files\Lore\lore.exe'
)
$ErrorActionPreference = 'Stop'
$here = Split-Path $MyInvocation.MyCommand.Path
$Web = $Web.TrimEnd('/')
if (-not $Server) { $Server = 'lore://' + ([Uri]$Web).Host }

$password = $env:LORE_LIVE_ADMIN_PASSWORD
if (-not $password -and $EnvFile) {
    $line = Get-Content $EnvFile | Where-Object { $_ -match '^LORE_ADMIN_PASSWORD=' } | Select-Object -First 1
    if ($line) { $password = ($line -split '=', 2)[1].Trim().Trim('"') }
}
if (-not $password) { throw 'Set LORE_LIVE_ADMIN_PASSWORD or pass -EnvFile with LORE_ADMIN_PASSWORD' }

$env:LORE_LIVE_WEB = $Web
$env:LORE_LIVE_SERVER = $Server
$env:LORE_LIVE_ROOT = Join-Path $Root 'scenarios'
$env:LORE_LIVE_ADMIN_PASSWORD = $password
$env:LORE_CLI = $Lore
$env:LORE_LIVE_RESTART_COMMAND = $RestartCommand
New-Item -ItemType Directory -Force $env:LORE_LIVE_ROOT | Out-Null

$failed = $false
"== Companion scenario tests ($Web, $Server)"
$project = Join-Path $here '..\LoreCompanion.Tests\LoreCompanion.Tests.csproj'
$results = Join-Path $Root 'scenarios.trx'
& dotnet test $project --filter 'TestCategory=Scenario' --logger "trx;LogFileName=$results" --logger 'console;verbosity=normal'
if ($LASTEXITCODE -ne 0) { $failed = $true }

if ($Explorer) {
    "== Explorer integration"
    # A fresh repository for the Explorer run, through the same API the scenario tests use.
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $page = Invoke-WebRequest "$Web/web/v1/login" -WebSession $session -UseBasicParsing
    $action = [System.Net.WebUtility]::HtmlDecode(([regex]::Match($page.Content, '<form[^>]*action="([^"]+)"')).Groups[1].Value)
    Invoke-WebRequest $action -Method Post -WebSession $session -UseBasicParsing -Body @{ username = 'admin'; password = $password; credentialId = '' } | Out-Null
    $csrf = (Invoke-RestMethod "$Web/web/v1/session" -WebSession $session).csrfToken
    $name = 'explorer-' + (Get-Date).ToUniversalTime().ToString('MMdd-HHmmss') + '/files'
    Invoke-RestMethod "$Web/web/v1/admin/repositories" -Method Post -WebSession $session -Headers @{ 'X-CSRF-Token' = $csrf } `
        -ContentType 'application/json' -Body (@{ name = $name; description = 'Explorer integration test'; public = $false } | ConvertTo-Json) | Out-Null
    $output = & (Join-Path $here 'explorer\Test-Explorer.ps1') -RepositoryUrl "$Server/$name" -Root (Join-Path $Root 'explorer') -RestartCommand $RestartCommand -Lore $Lore
    $output
    if ($output -match '\bFAIL\b') { $failed = $true }
}

if ($failed) { "Some checks failed."; exit 1 }
"All checks passed."
