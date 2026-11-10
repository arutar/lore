[CmdletBinding()]
param(
    [string]$Version = "0.1.0",
    [string]$ClientDirectory = "",
    [string]$OutputDirectory = ""
)

$ErrorActionPreference = "Stop"
if ($Version -notmatch '^\d+\.\d+\.\d+(\.\d+)?$') { throw "MSI version must contain three or four numeric components" }
$root = (Resolve-Path "$PSScriptRoot/../..").Path
if (-not $ClientDirectory) { $ClientDirectory = Join-Path $PSScriptRoot "bin/client" }
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $PSScriptRoot "bin" }
$ClientDirectory = (Resolve-Path $ClientDirectory).Path
if (-not (Test-Path "$ClientDirectory/LoreCompanion.exe")) { throw "Build the Windows client before packaging MSI" }
if (-not (Test-Path "$ClientDirectory/Web/index.html")) { throw "The client Web UI is missing" }
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

$dotnet = if (Get-Command dotnet -ErrorAction SilentlyContinue) { "dotnet" } else { "$root/.tooling/dotnet/dotnet.exe" }
$wix = if (Get-Command wix -ErrorAction SilentlyContinue) { "wix" } else { "$root/.tooling/wix/wix.exe" }
if (-not (Test-Path $wix)) {
    & $dotnet tool install wix --version 7.0.1 --tool-path "$root/.tooling/wix"
    if ($LASTEXITCODE -ne 0) { throw "WiX installation failed" }
}
if (-not $env:DOTNET_ROOT) { $env:DOTNET_ROOT = Split-Path $dotnet }
& $wix build "$PSScriptRoot/Installer/Package.wxs" -nologo -acceptEula wix7 -arch x64 `
    -d "Version=$Version" -d "ClientDir=$ClientDirectory" -d "SharedDir=$PSScriptRoot/shared" `
    -out "$OutputDirectory/LoreClient-x64.msi"
if ($LASTEXITCODE -ne 0) { throw "MSI build failed" }
Get-FileHash "$OutputDirectory/LoreClient-x64.msi" -Algorithm SHA256 | Format-List
Write-Host "MSI package: $OutputDirectory/LoreClient-x64.msi"
