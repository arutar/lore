[CmdletBinding()]
param(
    [string]$Version = "v0.13.0",
    [string]$OutputDirectory = ""
)

$ErrorActionPreference = "Stop"
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $PSScriptRoot "bin/client" }
$root = (Resolve-Path "$PSScriptRoot/../..").Path
$dotnet = if (Get-Command dotnet -ErrorAction SilentlyContinue) { "dotnet" } else { "$root/.tooling/dotnet/dotnet.exe" }
$cmake = if (Get-Command cmake -ErrorAction SilentlyContinue) { "cmake" } else { "C:/CMake/bin/cmake.exe" }
$cliChecksum = "26b84459fd6ab2faa10d5f8eb8393848286935414210d7070dafffd97e4c1343"

Remove-Item $OutputDirectory -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
Push-Location "$PSScriptRoot/LoreCompanion.Web"
try {
    & npm ci
    if ($LASTEXITCODE -ne 0) { throw "Companion Web UI dependency installation failed" }
    & npm run build
    if ($LASTEXITCODE -ne 0) { throw "Companion Web UI build failed" }
}
finally {
    Pop-Location
}
& $dotnet publish "$PSScriptRoot/LoreCompanion/LoreCompanion.csproj" -c Release -r win-x64 --self-contained true -p:PublishSingleFile=true -o "$OutputDirectory"
if ($LASTEXITCODE -ne 0) { throw "Companion publish failed" }
& $cmake -S "$PSScriptRoot/LoreShell" -B "$PSScriptRoot/LoreShell/build" -G "Visual Studio 17 2022" -A x64
if ($LASTEXITCODE -ne 0) { throw "CMake configure failed" }
& $cmake --build "$PSScriptRoot/LoreShell/build" --config Release
if ($LASTEXITCODE -ne 0) { throw "Shell extension build failed" }
Copy-Item "$PSScriptRoot/LoreShell/build/Release/LoreShell.dll" $OutputDirectory -Force

$temp = New-Item -ItemType Directory -Path ([IO.Path]::Combine([IO.Path]::GetTempPath(), [Guid]::NewGuid().ToString()))
try {
    $archive = "$temp/lore.zip"
    Invoke-WebRequest "https://github.com/EpicGames/lore/releases/download/$Version/lore-$Version-x86_64-pc-windows-msvc.zip" -OutFile $archive
    if ((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $cliChecksum) { throw "Lore CLI checksum mismatch" }
    Expand-Archive $archive "$temp/cli"
    $cli = Get-ChildItem "$temp/cli" -Filter lore.exe -Recurse | Select-Object -First 1
    if (-not $cli) { throw "lore.exe is missing from the release archive" }
    Copy-Item $cli.FullName "$OutputDirectory/lore.exe"
}
finally {
    Remove-Item $temp -Recurse -Force
}
Compress-Archive "$OutputDirectory/*" "$PSScriptRoot/bin/LoreClient-win-x64.zip" -Force
Write-Host "Windows client bundle: $OutputDirectory"
