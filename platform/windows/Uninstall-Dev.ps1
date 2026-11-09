[CmdletBinding()]
param(
    [string]$Configuration = "Debug",
    [string]$ShellDll = "$PSScriptRoot/LoreShell/build/$Configuration/LoreShell.dll"
)

$ErrorActionPreference = "Stop"
$shell = (Resolve-Path $ShellDll).Path
$process = Start-Process -FilePath "$env:WINDIR/System32/regsvr32.exe" -ArgumentList "/u", "/s", "`"$shell`"" -Wait -PassThru
if ($process.ExitCode -ne 0) {
    throw "regsvr32 failed with exit code $($process.ExitCode)"
}
Write-Host "Lore Explorer extension unregistered. Restart Explorer to unload the DLL."
