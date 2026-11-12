[CmdletBinding()]
param(
    [string]$PlatformVersion = "0.1.0",
    [string]$OutputDirectory = "",
    [string]$Generator = ""
)

$ErrorActionPreference = "Stop"
$env:CARGO_INCREMENTAL = "0"
if ($PlatformVersion -notmatch '^\d+\.\d+\.\d+(\.\d+)?$') { throw "Platform version must contain three or four numeric components" }
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $PSScriptRoot "bin/client" }
$root = (Resolve-Path "$PSScriptRoot/../..").Path
$dotnet = if (Get-Command dotnet -ErrorAction SilentlyContinue) { "dotnet" } else { "$root/.tooling/dotnet/dotnet.exe" }
$cmake = if (Get-Command cmake -ErrorAction SilentlyContinue) { "cmake" } else { "C:/CMake/bin/cmake.exe" }
$localCargo = "$root/.tooling/cargo/bin/cargo.exe"
$cargo = if (Get-Command cargo -ErrorAction SilentlyContinue) { "cargo" } else { $localCargo }
if ($cargo -eq $localCargo) {
    $env:CARGO_HOME = "$root/.tooling/cargo"
    $env:RUSTUP_HOME = "$root/.tooling/rustup"
}
if (-not (Get-Command $cargo -ErrorAction SilentlyContinue)) { throw "Rust and Cargo are required to build the Lore native library and CLI" }
if (-not $Generator) {
    $vswhere = "${env:ProgramFiles(x86)}\Microsoft Visual Studio\Installer\vswhere.exe"
    $visualStudioVersion = if (Test-Path $vswhere) { & $vswhere -latest -property installationVersion } else { $null }
    $Generator = switch -Regex ($visualStudioVersion) {
        '^18\.' { "Visual Studio 18 2026"; break }
        '^17\.' { "Visual Studio 17 2022"; break }
        default { throw "Visual Studio 2022 or 2026 is required" }
    }
}

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
& $dotnet publish "$PSScriptRoot/LoreCompanion/LoreCompanion.csproj" -c Release -r win-x64 --self-contained true -p:PublishSingleFile=true "-p:Version=$PlatformVersion" -o "$OutputDirectory"
if ($LASTEXITCODE -ne 0) { throw "Companion publish failed" }
& $cargo build --release -p lore-capi --manifest-path "$root/Cargo.toml"
if ($LASTEXITCODE -ne 0) { throw "Lore native library build failed" }
& $cargo build --release -p lore-client --bin lore --manifest-path "$root/Cargo.toml"
if ($LASTEXITCODE -ne 0) { throw "Lore CLI build failed" }
Copy-Item "$root/target/release/lore.dll" "$OutputDirectory/lorelib.dll" -Force
Copy-Item "$root/target/release/lore.exe" "$OutputDirectory/lore.exe" -Force
& $cmake -S "$PSScriptRoot/LoreShell" -B "$PSScriptRoot/LoreShell/build" -G $Generator -A x64 "-DLORE_PLATFORM_VERSION=$PlatformVersion"
if ($LASTEXITCODE -ne 0) { throw "CMake configure failed" }
& $cmake --build "$PSScriptRoot/LoreShell/build" --config Release
if ($LASTEXITCODE -ne 0) { throw "Shell extension build failed" }
Copy-Item "$PSScriptRoot/LoreShell/build/Release/LoreShell.dll" $OutputDirectory -Force
Compress-Archive "$OutputDirectory/*" "$PSScriptRoot/bin/LoreClient-win-x64.zip" -Force
Write-Host "Windows client bundle: $OutputDirectory"
