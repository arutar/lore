[CmdletBinding()]
param(
    [string]$Configuration = "Debug",
    [string]$CompanionDirectory = "$PSScriptRoot/LoreCompanion/bin/$Configuration/net10.0-windows/win-x64",
    [string]$ShellDll = "$PSScriptRoot/LoreShell/build/$Configuration/LoreShell.dll"
)

$ErrorActionPreference = "Stop"
$shell = (Resolve-Path $ShellDll).Path
$companion = (Resolve-Path "$CompanionDirectory/LoreCompanion.exe").Path
Copy-Item $shell (Join-Path (Resolve-Path $CompanionDirectory).Path "LoreShell.dll") -Force

$process = Start-Process -FilePath "$env:WINDIR/System32/regsvr32.exe" -ArgumentList "/s", "`"$shell`"" -Wait -PassThru
if ($process.ExitCode -ne 0) {
    throw "regsvr32 failed with exit code $($process.ExitCode)"
}

Start-Process $companion
Write-Host "Lore Explorer extension registered for the current user. Restart Explorer to reload overlays."
