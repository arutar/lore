[CmdletBinding()]
param(
    [string]$MsiPath = "$PSScriptRoot/bin/LoreClient-x64.msi",
    [string]$SmokeTest = "$PSScriptRoot/LoreShell/build/Release/LoreShellMenuSmoke.exe"
)

$ErrorActionPreference = "Stop"
$MsiPath = (Resolve-Path $MsiPath).Path
$SmokeTest = (Resolve-Path $SmokeTest).Path
$destination = Join-Path ([IO.Path]::GetTempPath()) "LoreMsiSmoke-$([Guid]::NewGuid().ToString('N'))"
$logDirectory = New-Item -ItemType Directory -Force -Path "$PSScriptRoot/bin/msi-smoke"
$installed = $false

function Invoke-Msi([string]$action, [string]$log) {
    $arguments = @($action, "`"$MsiPath`"", "/qn", "/norestart")
    if ($action -eq "/i") { $arguments += "APPLICATIONFOLDER=`"$destination`"" }
    $arguments += @("/L*v", "`"$log`"")
    $process = Start-Process msiexec.exe -ArgumentList $arguments -Wait -PassThru
    if ($process.ExitCode -ne 0) { throw "msiexec $action failed with exit code $($process.ExitCode). See $log" }
}

try {
    Invoke-Msi "/i" "$logDirectory/install.log"
    $installed = $true
    foreach ($file in @("LoreCompanion.exe", "LoreShell.dll", "lore.exe", "WebView2Loader.dll", "Web/index.html")) {
        if (-not (Test-Path "$destination/$file")) { throw "Installed file is missing: $file" }
    }
    $handler = "HKLM:\SOFTWARE\Classes\Directory\shellex\ContextMenuHandlers\Lore"
    if (-not (Test-Path $handler)) { throw "Explorer context menu registration is missing" }
    & "$destination/lore.exe" --version
    if ($LASTEXITCODE -ne 0) { throw "Installed Lore CLI failed to start" }
    Copy-Item $SmokeTest "$destination/LoreShellMenuSmoke.exe"
    & "$destination/LoreShellMenuSmoke.exe"
    if ($LASTEXITCODE -ne 0) { throw "Installed shell extension smoke test failed" }

    Invoke-Msi "/x" "$logDirectory/uninstall.log"
    $installed = $false
    if (Test-Path "$destination/LoreCompanion.exe") { throw "LoreCompanion.exe was not removed" }
    if (Test-Path "$destination/LoreShell.dll") { throw "LoreShell.dll was not removed" }
    if (Test-Path $handler) { throw "Explorer context menu registration was not removed" }
    Write-Host "MSI install/uninstall smoke test passed"
}
finally {
    if ($installed) { Invoke-Msi "/x" "$logDirectory/uninstall-cleanup.log" }
    Remove-Item $destination -Recurse -Force -ErrorAction SilentlyContinue
}
