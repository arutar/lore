[CmdletBinding()]
param(
    [string]$BundlePath = "",
    [string]$Destination = "",
    [string]$WebView2Bootstrapper = "",
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
        $arguments = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "`"$script`"", "-AllUsers", "-BundlePath", "`"$BundlePath`"", "-Destination", "`"$Destination`"", "-WebView2Bootstrapper", "`"$WebView2Bootstrapper`"")
        if ($NoShell) { $arguments += "-NoShell" }
        $process = Start-Process powershell.exe -Verb RunAs -ArgumentList $arguments -Wait -PassThru
        if ($process.ExitCode -ne 0) { throw "Elevated installation failed" }
        return
    }
}
if (-not $BundlePath) { $BundlePath = Join-Path $PSScriptRoot "../windows/bin/client" }
$source = (Resolve-Path $BundlePath).Path
foreach ($file in @("lore.exe", "LoreCompanion.exe", "LoreShell.dll", "Web/index.html")) {
    if (-not (Test-Path "$source/$file")) { throw "$file is missing from the client bundle" }
}
$runtimeClient = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
$runtimePaths = @(
    "HKLM:\SOFTWARE\Microsoft\EdgeUpdate\Clients\$runtimeClient",
    "HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\$runtimeClient",
    "HKCU:\SOFTWARE\Microsoft\EdgeUpdate\Clients\$runtimeClient"
)
$runtimeVersion = $runtimePaths | ForEach-Object {
    (Get-ItemProperty $_ -Name pv -ErrorAction SilentlyContinue).pv
} | Where-Object { $_ } | Select-Object -First 1
if (-not $runtimeVersion) {
    $bootstrapper = $WebView2Bootstrapper
    $downloaded = ""
    try {
        if (-not $bootstrapper) {
            $downloaded = Join-Path ([IO.Path]::GetTempPath()) "MicrosoftEdgeWebview2Setup-$([Guid]::NewGuid().ToString('N')).exe"
            Write-Host "Downloading WebView2 Runtime..."
            Invoke-WebRequest "https://go.microsoft.com/fwlink/p/?LinkId=2124703" -OutFile $downloaded
            $bootstrapper = $downloaded
        }
        $process = Start-Process $bootstrapper -ArgumentList "/silent", "/install" -Wait -PassThru
        if ($process.ExitCode -ne 0) { throw "WebView2 Runtime installation failed with exit code $($process.ExitCode)" }
    }
    finally {
        if ($downloaded) { Remove-Item $downloaded -Force -ErrorAction SilentlyContinue }
    }
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
    foreach ($item in Get-ChildItem $source) {
        $target = Join-Path $Destination $item.Name
        if (Test-Path $target) {
            $backup = "$target.$([Guid]::NewGuid().ToString('N')).old"
            Move-Item $target $backup -Force
            $backups += $backup
        }
        Copy-Item $item.FullName $target -Recurse -Force
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
    foreach ($backup in $backups) { Remove-Item $backup -Recurse -Force -ErrorAction SilentlyContinue }
    Remove-Item "$installRoot/versions" -Recurse -Force -ErrorAction SilentlyContinue
}
Start-Process "$Destination/LoreCompanion.exe"
& "$Destination/lore.exe" --version
Write-Host "Lore client installed to $Destination. Explorer restarted with the updated extension."
