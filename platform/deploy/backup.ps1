[CmdletBinding()]
param([string]$Destination, [string]$Name)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
# The data root comes from the environment or, as setup stores it, from .env. Compose reads
# .env on its own; this script has to do the same or it works on the wrong directory.
$dataRootSetting = $env:LORE_DATA_ROOT
if (-not $dataRootSetting -and (Test-Path ".env")) {
    $dataRootSetting = Get-Content ".env" | Where-Object { $_ -like "LORE_DATA_ROOT=*" } | ForEach-Object { $_.Substring("LORE_DATA_ROOT=".Length) } | Select-Object -Last 1
}
if (-not $dataRootSetting) { $dataRootSetting = "./data" }
$dataRoot = [IO.Path]::GetFullPath($dataRootSetting)
if (-not $Destination) { $Destination = "./backups" }
if (-not $Name) { $Name = [DateTime]::UtcNow.ToString("yyyyMMdd-HHmmss") }
if ($Name -notmatch "^[a-zA-Z0-9._-]+$") { throw "Backup name contains invalid characters" }
$final = [IO.Path]::GetFullPath((Join-Path $Destination $Name))
if (Test-Path $final) { throw "Backup already exists: $final" }
# Refuse to produce an archive that can't restore the server: an empty or wrong data root
# used to yield a "successful" backup without the content store and the signing key.
foreach ($directory in @("lore", "control-plane")) {
    if (-not (Test-Path "$dataRoot/$directory" -PathType Container)) { throw "Data directory is missing: $dataRoot/$directory. Check LORE_DATA_ROOT." }
}
if (-not (Get-ChildItem "$dataRoot/lore" -Force | Select-Object -First 1)) { throw "Lore storage is empty: $dataRoot/lore. Check LORE_DATA_ROOT." }
if (-not (Test-Path "$dataRoot/control-plane/token-exchange-signing.pem" -PathType Leaf) -and $env:LORE_BACKUP_WITHOUT_SIGNING_KEY -ne "true") {
    throw "Signing key is missing: $dataRoot/control-plane/token-exchange-signing.pem. Set LORE_BACKUP_WITHOUT_SIGNING_KEY=true if the key is stored elsewhere."
}
# Work in a temporary directory so an interrupted run never leaves something that looks like a backup.
$backup = "$final.partial"
if (Test-Path $backup) { Remove-Item $backup -Recurse -Force }
New-Item -ItemType Directory -Force -Path $backup | Out-Null
# The backup holds the secrets from .env and the token signing key: owner access only.
if ($IsWindows -or $PSVersionTable.PSEdition -eq "Desktop") {
    & icacls $backup /inheritance:r /grant:r "$($env:USERNAME):(OI)(CI)F" | Out-Null
} else {
    & chmod 700 $backup
}
if (-not (Test-Path ".env")) { throw ".env is missing; run setup first" }
$completed = $false
$services = @("caddy", "lore", "control-plane", "keycloak")
$stopped = $false
try {
    $stopped = $true
    docker compose stop $services
    if ($LASTEXITCODE -ne 0) { throw "Couldn't stop Lore services" }
    foreach ($database in @("lore", "keycloak")) {
        $remote = "/tmp/lore-backup-$database.dump"
        docker compose exec -T postgres pg_dump -U lore --format=custom --file=$remote $database
        if ($LASTEXITCODE -ne 0) { throw "Couldn't dump $database" }
        docker compose cp "postgres`:$remote" "$backup/$database.dump"
        if ($LASTEXITCODE -ne 0) { throw "Couldn't copy $database dump" }
        docker compose exec -T postgres rm -f $remote
    }
    $tar = if ($env:SystemRoot -and (Test-Path "$env:SystemRoot\System32\tar.exe")) { "$env:SystemRoot\System32\tar.exe" } else { "tar" }
    & $tar -C $dataRoot -cf "$backup/storage.tar" lore control-plane
    if ($LASTEXITCODE -ne 0) { throw "Couldn't archive Lore storage" }
    # A restore on a new host needs the secrets that match the Keycloak database.
    Copy-Item ".env" "$backup/env"
    @{ createdAt = [DateTime]::UtcNow.ToString("o"); databases = @("lore", "keycloak"); storage = @("lore", "control-plane"); settings = "env" } |
        ConvertTo-Json -Compress | Set-Content "$backup/manifest.json" -Encoding ascii
    $checksums = Get-ChildItem $backup -File | Get-FileHash -Algorithm SHA256 | ForEach-Object { "$($_.Hash.ToLowerInvariant())  $([IO.Path]::GetFileName($_.Path))" }
    [IO.File]::WriteAllText("$backup/SHA256SUMS", (($checksums -join "`n") + "`n"), [Text.Encoding]::ASCII)
    Move-Item $backup $final
    $completed = $true
    Write-Output "Backup created: $final (data root: $dataRoot)"
} finally {
    if (-not $completed) { Remove-Item $backup -Recurse -Force -ErrorAction SilentlyContinue }
    if ($stopped) { docker compose up -d --no-recreate --wait --wait-timeout 120 keycloak control-plane lore caddy | Out-Host }
}
