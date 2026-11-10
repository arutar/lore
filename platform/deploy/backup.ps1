[CmdletBinding()]
param([string]$Destination, [string]$Name)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
$dataRoot = [IO.Path]::GetFullPath($(if ($env:LORE_DATA_ROOT) { $env:LORE_DATA_ROOT } else { "./data" }))
if (-not $Destination) { $Destination = "./backups" }
if (-not $Name) { $Name = [DateTime]::UtcNow.ToString("yyyyMMdd-HHmmss") }
if ($Name -notmatch "^[a-zA-Z0-9._-]+$") { throw "Backup name contains invalid characters" }
$backup = [IO.Path]::GetFullPath((Join-Path $Destination $Name))
if (Test-Path $backup) { throw "Backup already exists: $backup" }
New-Item -ItemType Directory -Force -Path $backup | Out-Null
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
    & "$env:SystemRoot\System32\tar.exe" -C $dataRoot -cf "$backup/storage.tar" lore control-plane
    if ($LASTEXITCODE -ne 0) { throw "Couldn't archive Lore storage" }
    @{ createdAt = [DateTime]::UtcNow.ToString("o"); databases = @("lore", "keycloak"); storage = @("lore", "control-plane") } |
        ConvertTo-Json -Compress | Set-Content "$backup/manifest.json" -Encoding ascii
    $checksums = Get-ChildItem $backup -File | Get-FileHash -Algorithm SHA256 | ForEach-Object { "$($_.Hash.ToLowerInvariant())  $([IO.Path]::GetFileName($_.Path))" }
    [IO.File]::WriteAllText("$backup/SHA256SUMS", (($checksums -join "`n") + "`n"), [Text.Encoding]::ASCII)
    Write-Output "Backup created: $backup"
} finally {
    if ($stopped) { docker compose up -d --no-recreate --wait --wait-timeout 120 keycloak control-plane lore caddy | Out-Host }
}
