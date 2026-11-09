[CmdletBinding()]
param(
    [string]$BundlePath = "",
    [string]$Destination = "",
    [switch]$NoShell,
    [switch]$AllUsers
)

$ErrorActionPreference = "Stop"
if (-not $Destination) {
    $Destination = if ($AllUsers) { "$env:ProgramFiles/Lore" } else { "$env:LOCALAPPDATA/Programs/Lore" }
}
if ($AllUsers) {
    $isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    if (-not $isAdmin) {
        $script = $MyInvocation.MyCommand.Path
        $arguments = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$script`"", "-AllUsers", "-BundlePath", "`"$BundlePath`"", "-Destination", "`"$Destination`"")
        if ($NoShell) { $arguments += "-NoShell" }
        $process = Start-Process powershell.exe -Verb RunAs -ArgumentList $arguments -Wait -PassThru
        if ($process.ExitCode -ne 0) { throw "Elevated installation failed" }
        return
    }
}
if (-not $BundlePath) { $BundlePath = Join-Path $PSScriptRoot "../windows/bin/client" }
$source = (Resolve-Path $BundlePath).Path
foreach ($file in @("lore.exe", "LoreCompanion.exe", "LoreShell.dll")) {
    if (-not (Test-Path "$source/$file")) { throw "$file is missing from the client bundle" }
}
$legacy = "$env:LOCALAPPDATA/Programs/Lore/LoreShell.dll"
if ($AllUsers -and (Test-Path $legacy)) {
    $process = Start-Process "$env:WINDIR/System32/regsvr32.exe" -ArgumentList "/u", "/s", "`"$legacy`"" -Wait -PassThru
    if ($process.ExitCode -ne 0) { throw "Legacy per-user unregistration failed" }
    Move-Item $legacy "$legacy.disabled" -Force
}
$installRoot = $Destination
New-Item -ItemType Directory -Force -Path $Destination | Out-Null
Stop-Process -Name LoreCompanion -Force -ErrorAction SilentlyContinue
if (-not $NoShell) { Stop-Process -Name explorer -Force -ErrorAction SilentlyContinue }
$backups = @()
try {
    foreach ($file in Get-ChildItem $source -File) {
        $target = Join-Path $Destination $file.Name
        if (Test-Path $target) {
            $backup = "$target.$([Guid]::NewGuid().ToString('N')).old"
            Move-Item $target $backup -Force
            $backups += $backup
        }
        Copy-Item $file.FullName $target -Force
    }

    $pathScope = if ($AllUsers) { "Machine" } else { "User" }
    $userPath = [Environment]::GetEnvironmentVariable("Path", $pathScope)
    $parts = @($userPath -split ";" | Where-Object { $_ -and -not $_.StartsWith("$installRoot/versions/") -and -not $_.StartsWith("$installRoot\versions\") })
    if ($parts -notcontains $Destination) {
        [Environment]::SetEnvironmentVariable("Path", (($parts + $Destination) -join ";"), $pathScope)
    }

    if (-not $NoShell) {
        $process = Start-Process "$env:WINDIR/System32/regsvr32.exe" -ArgumentList "/s", "`"$Destination/LoreShell.dll`"" -Wait -PassThru
        if ($process.ExitCode -ne 0) { throw "Shell extension registration failed" }
    }
}
finally {
    if (-not $NoShell) { Start-Process "$env:WINDIR/explorer.exe" }
    foreach ($backup in $backups) { Remove-Item $backup -Force -ErrorAction SilentlyContinue }
    Remove-Item "$installRoot/versions" -Recurse -Force -ErrorAction SilentlyContinue
}
Start-Process "$Destination/LoreCompanion.exe"
& "$Destination/lore.exe" --version
Write-Host "Lore client installed to $Destination. Explorer restarted with the updated extension."
