[CmdletBinding()]
param(
    [string]$MsiPath = "$PSScriptRoot/bin/LoreClient-x64.msi",
    [string]$SmokeTest = "$PSScriptRoot/LoreShell/build/Release/LoreShellMenuSmoke.exe"
)

$ErrorActionPreference = "Stop"
$uninstallPaths = @(
    "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*",
    "HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*"
)
$existingClient = Get-ItemProperty $uninstallPaths -ErrorAction SilentlyContinue |
    Where-Object { $_.DisplayName -eq "Lore Client" }
if ($existingClient) { throw "Test-Msi.ps1 requires a clean VM without an installed Lore Client" }
$MsiPath = (Resolve-Path $MsiPath).Path
$SmokeTest = (Resolve-Path $SmokeTest).Path
$clientSource = Join-Path $PSScriptRoot "bin/client"
$ownedBinaries = @("LoreCompanion.exe", "LoreShell.dll")
foreach ($file in $ownedBinaries) {
    $fileVersion = (Get-Item "$clientSource/$file").VersionInfo.FileVersion
    if (-not $fileVersion -or [version]$fileVersion -eq [version]"0.0.0.0") {
        throw "$file must have a non-zero file version for MSI upgrades"
    }
}
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
    foreach ($file in $ownedBinaries) {
        $sourceHash = (Get-FileHash "$clientSource/$file" -Algorithm SHA256).Hash
        $installedHash = (Get-FileHash "$destination/$file" -Algorithm SHA256).Hash
        if ($sourceHash -ne $installedHash) { throw "MSI installed a different $file" }
    }
    $handler = "HKLM:\SOFTWARE\Classes\Directory\shellex\ContextMenuHandlers\Lore"
    if (-not (Test-Path $handler)) { throw "Explorer context menu registration is missing" }
    $contextClsid = "{cc69a511-5c48-46bc-9a41-d14050831a93}"
    if ((Get-ItemProperty $handler)."(default)" -ne $contextClsid) { throw "Explorer handler has the wrong CLSID" }
    $shellClasses = @(
        $contextClsid,
        "{b056f7b6-3445-46d0-b3e3-01573d936aba}",
        "{95ecca15-ad76-4802-8f4e-37e967bf7139}",
        "{9dcc2578-1112-455a-9af7-d28f18cea2b1}",
        "{224f8e7b-61ba-4da3-97d3-6092d9c47a0b}",
        "{902208de-1b5a-462f-9aa7-a409d88b8042}",
        "{80444c1d-93fc-4241-9af5-7eff9280985e}",
        "{e3d52a22-7739-4a37-a35f-47a1d00f8d6f}"
    )
    foreach ($clsid in $shellClasses) {
        $server = Get-ItemProperty "HKLM:\SOFTWARE\Classes\CLSID\$clsid\InprocServer32" -ErrorAction SilentlyContinue
        if ($server."(default)" -ne "$destination\LoreShell.dll") { throw "Wrong COM server for $clsid" }
    }
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
