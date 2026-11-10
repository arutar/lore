[CmdletBinding(SupportsShouldProcess = $true)]
param([Parameter(Mandatory = $true)][string]$Backup)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
$backup = [IO.Path]::GetFullPath($Backup)
foreach ($file in @("lore.dump", "keycloak.dump", "storage.tar", "manifest.json")) {
    if (-not (Test-Path "$backup/$file" -PathType Leaf)) { throw "Backup is incomplete: $file" }
}
if (-not $PSCmdlet.ShouldProcess($backup, "Restore Lore databases and storage")) { return }
$dataRoot = [IO.Path]::GetFullPath($(if ($env:LORE_DATA_ROOT) { $env:LORE_DATA_ROOT } else { "./data" }))
$staging = Join-Path $dataRoot (".restore-new-" + [Guid]::NewGuid())
$previous = Join-Path $dataRoot (".restore-old-" + [Guid]::NewGuid())
$services = @("caddy", "lore", "control-plane", "keycloak")
$stopped = $false
$completed = $false
try {
    $stopped = $true
    docker compose stop $services
    if ($LASTEXITCODE -ne 0) { throw "Couldn't stop Lore services" }
    foreach ($database in @("lore", "keycloak")) {
        $remote = "/tmp/lore-restore-$database.dump"
        docker compose cp "$backup/$database.dump" "postgres`:$remote"
        if ($LASTEXITCODE -ne 0) { throw "Couldn't copy $database dump" }
        docker compose exec -T postgres pg_restore -U lore --clean --if-exists --dbname=$database $remote
        if ($LASTEXITCODE -ne 0) { throw "Couldn't restore $database" }
        docker compose exec -T postgres rm -f $remote
    }
    New-Item -ItemType Directory -Force -Path $staging, $previous | Out-Null
    & "$env:SystemRoot\System32\tar.exe" -C $staging -xf "$backup/storage.tar"
    if ($LASTEXITCODE -ne 0) { throw "Couldn't extract storage" }
    foreach ($directory in @("lore", "control-plane")) {
        if (-not (Test-Path "$staging/$directory" -PathType Container)) { throw "Backup storage is missing $directory" }
        if (Test-Path "$dataRoot/$directory") { Move-Item "$dataRoot/$directory" "$previous/$directory" }
    }
    foreach ($directory in @("lore", "control-plane")) {
        Move-Item "$staging/$directory" "$dataRoot/$directory"
    }
    $completed = $true
    Remove-Item $previous -Recurse -Force
    Write-Output "Restore completed: $backup"
} finally {
    if (-not $completed -and (Test-Path $previous)) {
        foreach ($directory in @("lore", "control-plane")) {
            if (Test-Path "$previous/$directory") {
                Remove-Item "$dataRoot/$directory" -Recurse -Force -ErrorAction SilentlyContinue
                Move-Item "$previous/$directory" "$dataRoot/$directory"
            }
        }
        Remove-Item $previous -Recurse -Force -ErrorAction SilentlyContinue
    }
    Remove-Item $staging -Recurse -Force -ErrorAction SilentlyContinue
    if ($stopped) { docker compose up -d --no-recreate --wait --wait-timeout 120 keycloak control-plane lore caddy | Out-Host }
}
