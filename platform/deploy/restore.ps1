[CmdletBinding(SupportsShouldProcess = $true)]
param([Parameter(Mandatory = $true)][string]$Backup)
# Restores a backup made by backup.ps1 (or backup.sh) over the current installation.
# Everything that can fail without touching data runs first; once the databases are being
# replaced, any failure puts the previous databases and storage back.
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
$backup = [IO.Path]::GetFullPath($Backup)
foreach ($file in @("lore.dump", "keycloak.dump", "storage.tar", "manifest.json", "SHA256SUMS")) {
    if (-not (Test-Path "$backup/$file" -PathType Leaf)) { throw "Backup is incomplete: $file" }
}
foreach ($line in Get-Content "$backup/SHA256SUMS") {
    if ($line -notmatch "^([0-9a-fA-F]{64})\s+\*?(.+)$") { continue }
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $backup $matches[2])).Hash
    if ($actual -ne $matches[1].ToUpperInvariant()) { throw "Checksum mismatch: $($matches[2])" }
}
if (-not (Test-Path ".env")) { throw ".env is missing; run setup on this host first" }
if (-not $PSCmdlet.ShouldProcess($backup, "Restore Lore databases and storage")) { return }

# The data root comes from the environment or, as setup stores it, from .env.
$dataRootSetting = $env:LORE_DATA_ROOT
if (-not $dataRootSetting) {
    $dataRootSetting = Get-Content ".env" | Where-Object { $_ -like "LORE_DATA_ROOT=*" } | ForEach-Object { $_.Substring("LORE_DATA_ROOT=".Length) } | Select-Object -Last 1
}
if (-not $dataRootSetting) { $dataRootSetting = "./data" }
$dataRoot = [IO.Path]::GetFullPath($dataRootSetting)
$staging = Join-Path $dataRoot (".restore-new-" + [Guid]::NewGuid())
$previous = Join-Path $dataRoot (".restore-old-" + [Guid]::NewGuid())
$tar = if ($env:SystemRoot -and (Test-Path "$env:SystemRoot\System32\tar.exe")) { "$env:SystemRoot\System32\tar.exe" } else { "tar" }
$services = @("caddy", "lore", "control-plane", "keycloak")
$stopped = $false
$databasesTouched = $false
$envSaved = $false
$completed = $false

function Invoke-Checked([string]$Description, [scriptblock]$Command) {
    & $Command
    if ($LASTEXITCODE -ne 0) { throw $Description }
}

# pg_restore reads the dump from a file inside the container: piping binary data through
# Windows PowerShell 5 would corrupt it.
function Restore-Database([string]$Database, [string]$Dump, [switch]$StopOnError) {
    $remote = "/tmp/lore-restore-$Database.dump"
    Invoke-Checked "Couldn't copy the $Database dump" { docker compose cp $Dump "postgres`:$remote" }
    $options = @("--clean", "--if-exists", "--single-transaction")
    if ($StopOnError) { $options += "--exit-on-error" }
    try {
        Invoke-Checked "Couldn't restore $Database" { docker compose exec -T postgres pg_restore -U lore @options "--dbname=$Database" $remote }
    } finally {
        docker compose exec -T postgres rm -f $remote | Out-Null
    }
}

try {
    # Step 1: unpack and check the storage before anything is stopped or replaced.
    New-Item -ItemType Directory -Force -Path $staging, $previous | Out-Null
    Invoke-Checked "Couldn't extract storage" { & $tar -C $staging -xf "$backup/storage.tar" }
    foreach ($directory in @("lore", "control-plane")) {
        if (-not (Test-Path "$staging/$directory" -PathType Container)) { throw "Backup storage is missing $directory" }
    }

    # Step 2: stop the services and keep a dump of the current databases for a rollback.
    $stopped = $true
    Invoke-Checked "Couldn't stop Lore services" { docker compose stop $services }
    foreach ($database in @("lore", "keycloak")) {
        $remote = "/tmp/lore-before-restore-$database.dump"
        Invoke-Checked "Couldn't dump the current $database database" { docker compose exec -T postgres pg_dump -U lore --format=custom --file=$remote $database }
        Invoke-Checked "Couldn't copy the current $database dump" { docker compose cp "postgres`:$remote" "$previous/$database.dump" }
        docker compose exec -T postgres rm -f $remote | Out-Null
    }

    # Step 3: databases, each in one transaction that stops at the first error.
    $databasesTouched = $true
    foreach ($database in @("lore", "keycloak")) { Restore-Database $database "$backup/$database.dump" -StopOnError }

    # Step 4: storage.
    foreach ($directory in @("lore", "control-plane")) {
        if (Test-Path "$dataRoot/$directory") { Move-Item "$dataRoot/$directory" "$previous/$directory" }
        Move-Item "$staging/$directory" "$dataRoot/$directory"
    }

    # Step 5: secrets that live in the restored Keycloak database. Host settings (URLs, data
    # root, the PostgreSQL password of this cluster) stay as they are.
    if (Test-Path "$backup/env" -PathType Leaf) {
        Copy-Item ".env" "$previous/env"
        $envSaved = $true
        $restored = @{}
        foreach ($line in Get-Content "$backup/env") {
            $key, $value = $line -split "=", 2
            if ($key -in @("KEYCLOAK_ADMIN_PASSWORD", "LORE_SERVICE_CLIENT_SECRET") -and $value) { $restored[$key] = $value }
        }
        $lines = @(Get-Content ".env" | Where-Object { ($_ -split "=", 2)[0] -notin $restored.Keys })
        foreach ($entry in $restored.GetEnumerator()) { $lines += "$($entry.Key)=$($entry.Value)" }
        [IO.File]::WriteAllLines((Join-Path (Get-Location).Path ".env"), [string[]]$lines, [Text.UTF8Encoding]::new($false))
    } else {
        Write-Warning "This backup has no settings file (made before it was added). If Keycloak sign-in fails after the restore, copy KEYCLOAK_ADMIN_PASSWORD and LORE_SERVICE_CLIENT_SECRET from the original host's .env."
    }
    $completed = $true
    Write-Output "Restore completed: $backup"
} finally {
    if (-not $completed) {
        Write-Warning "Restore failed; putting the previous installation back"
        if ($databasesTouched) {
            foreach ($database in @("lore", "keycloak")) {
                if (Test-Path "$previous/$database.dump") {
                    try { Restore-Database $database "$previous/$database.dump" }
                    catch { Write-Warning "Couldn't roll back database ${database}; its pre-restore dump is kept in $previous" }
                }
            }
        }
        foreach ($directory in @("lore", "control-plane")) {
            if (Test-Path "$previous/$directory") {
                Remove-Item "$dataRoot/$directory" -Recurse -Force -ErrorAction SilentlyContinue
                Move-Item "$previous/$directory" "$dataRoot/$directory"
            }
        }
        if ($envSaved) { Copy-Item "$previous/env" ".env" -Force }
    }
    Remove-Item $staging -Recurse -Force -ErrorAction SilentlyContinue
    if ($completed -or -not $databasesTouched) {
        Remove-Item $previous -Recurse -Force -ErrorAction SilentlyContinue
    } else {
        Write-Warning "Dumps of the databases as they were before the restore are kept in $previous"
    }
    # Recreate rather than just start: restored secrets in .env must reach the containers.
    if ($stopped) { docker compose up -d --wait --wait-timeout 180 keycloak control-plane lore caddy | Out-Host }
}
