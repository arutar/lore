[CmdletBinding()]
param(
    [string]$PublicHost = $(if ($env:LORE_PUBLIC_HOST) { $env:LORE_PUBLIC_HOST } else { "host.docker.internal" }),
    [string]$Scheme = $(if ($env:LORE_PUBLIC_SCHEME) { $env:LORE_PUBLIC_SCHEME } else { "http" }),
    [string]$PublicUrl = $env:LORE_PUBLIC_URL,
    [string]$AuthUrl = $env:LORE_AUTH_PUBLIC_URL,
    [switch]$DirectCaddy,
    [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw "Docker is required" }
docker compose version | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Docker Compose v2 is required" }
docker info | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Docker engine isn't running" }

function New-HexSecret {
    $bytes = [byte[]]::new(32)
    $random = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $random.GetBytes($bytes) } finally { $random.Dispose() }
    return -join ($bytes | ForEach-Object { $_.ToString("x2") })
}
function Write-Utf8NoBom([string]$Path, [Parameter(ValueFromPipeline = $true)][string]$Text) {
    process {
        $fullPath = Join-Path (Get-Location).Path $Path
        [IO.File]::WriteAllText($fullPath, $Text, [Text.UTF8Encoding]::new($false))
    }
}
$settings = @{}
if (Test-Path .env) {
    Get-Content .env | ForEach-Object {
        $key, $value = $_ -split "=", 2
        if ($key -and $value) { $settings[$key] = $value }
    }
}
if (-not $PublicUrl) {
    if ($settings.LORE_PUBLIC_URL) { $PublicUrl = $settings.LORE_PUBLIC_URL }
    elseif ($Scheme -eq "https") { $PublicUrl = "https://$PublicHost" }
    else { $PublicUrl = "http://${PublicHost}:8080" }
}
if (-not $AuthUrl) { $AuthUrl = "$PublicUrl/auth" }
if (-not (Test-Path .env)) {
    @(
        "POSTGRES_PASSWORD=$(New-HexSecret)"
        "KEYCLOAK_ADMIN_PASSWORD=$(New-HexSecret)"
        "LORE_ADMIN_PASSWORD=$(New-HexSecret)"
        "LORE_SERVICE_CLIENT_SECRET=$(New-HexSecret)"
        "LORE_PUBLIC_URL=$PublicUrl"
        "LORE_PUBLIC_HOST=$PublicHost"
        "LORE_PUBLIC_SCHEME=$Scheme"
    ) | Set-Content -Path .env -Encoding ascii
    Get-Content .env | ForEach-Object {
        $key, $value = $_ -split "=", 2
        $settings[$key] = $value
    }
}
foreach ($required in @("POSTGRES_PASSWORD", "KEYCLOAK_ADMIN_PASSWORD", "LORE_ADMIN_PASSWORD", "LORE_SERVICE_CLIENT_SECRET")) {
    if (-not $settings[$required]) { throw "$required is missing from .env" }
}
foreach ($entry in $settings.GetEnumerator()) {
    if (-not [Environment]::GetEnvironmentVariable($entry.Key)) {
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, "Process")
    }
}
function Set-EnvSetting([string]$Name, [string]$Value) {
    $settings[$Name] = $Value
    [Environment]::SetEnvironmentVariable($Name, $Value, "Process")
    $found = $false
    $updated = @(Get-Content .env | ForEach-Object {
        if ($_ -match "^$Name=") {
            $found = $true
            "$Name=$Value"
        } else {
            $_
        }
    })
    if (-not $found) { $updated += "$Name=$Value" }
    [IO.File]::WriteAllLines((Join-Path (Get-Location).Path ".env"), [string[]]$updated, [Text.UTF8Encoding]::new($false))
}
Set-EnvSetting "LORE_PUBLIC_URL" $PublicUrl
Set-EnvSetting "LORE_PUBLIC_HOST" $PublicHost
Set-EnvSetting "LORE_PUBLIC_SCHEME" $Scheme
Set-EnvSetting "LORE_AUTH_PUBLIC_URL" $AuthUrl
Set-EnvSetting "LORE_PUBLIC_ADDRESS" $(if ($Scheme -eq "https") { ([Uri]$PublicUrl).Authority } else { $PublicUrl })
Set-EnvSetting "SECURE_COOKIES" $(if ($Scheme -eq "https") { "true" } else { "false" })
foreach ($name in @(
    "LORE_LDAP_URL", "LORE_LDAP_BASE_DN", "LORE_LDAP_BIND_DN", "LORE_LDAP_BIND_PASSWORD",
    "LORE_LDAP_USERNAME_ATTRIBUTE", "LORE_LDAP_STARTTLS", "LORE_LDAP_INSECURE_SKIP_VERIFY", "LORE_LDAP_TIMEOUT",
    "LORE_SMTP_HOST", "LORE_SMTP_PORT", "LORE_SMTP_FROM", "LORE_SMTP_FROM_NAME",
    "LORE_SMTP_USERNAME", "LORE_SMTP_PASSWORD", "LORE_SMTP_STARTTLS", "LORE_SMTP_SSL",
    "LORE_DATA_ROOT", "LORE_PROXY_BIND"
)) {
    $value = [Environment]::GetEnvironmentVariable($name)
    if ($value) { Set-EnvSetting $name $value }
}

$generated = "data/generated"
New-Item -ItemType Directory -Force -Path "$generated/caddy", "data/postgres", "data/lore", "data/control-plane", "data/caddy" | Out-Null
(Get-Content lore/docker.toml -Raw).Replace("http://host.docker.internal:8080/auth", $AuthUrl).Replace("http://host.docker.internal:8080", $PublicUrl) |
    Write-Utf8NoBom "$generated/lore.toml"
(Get-Content keycloak/lore-realm.json -Raw).Replace("http://localhost:8080", $PublicUrl).Replace("host.docker.internal", $PublicHost).Replace("lore-service-dev-only", $settings.LORE_SERVICE_CLIENT_SECRET).Replace("admin-dev-only", $settings.LORE_ADMIN_PASSWORD) |
    Write-Utf8NoBom "$generated/lore-realm.json"
$realm = Get-Content "$generated/lore-realm.json" -Raw | ConvertFrom-Json
if ($Scheme -eq "https") { $realm.sslRequired = "external" }
if ($env:LORE_SMTP_HOST) {
    $smtpFrom = $env:LORE_SMTP_FROM
    if ($smtpFrom -match "<([^>]+)>") { $smtpFrom = $matches[1] }
    $realm.smtpServer = [ordered]@{
        host = $env:LORE_SMTP_HOST
        port = $(if ($env:LORE_SMTP_PORT) { $env:LORE_SMTP_PORT } else { "587" })
        from = $smtpFrom
        fromDisplayName = $(if ($env:LORE_SMTP_FROM_NAME) { $env:LORE_SMTP_FROM_NAME } else { "Lore" })
        starttls = $(if ($env:LORE_SMTP_STARTTLS) { $env:LORE_SMTP_STARTTLS } else { "true" })
        ssl = $(if ($env:LORE_SMTP_SSL) { $env:LORE_SMTP_SSL } else { "false" })
        auth = [bool]($env:LORE_SMTP_USERNAME -or $env:LORE_SMTP_PASSWORD)
        user = $env:LORE_SMTP_USERNAME
        password = $env:LORE_SMTP_PASSWORD
    }
    $realm.verifyEmail = $true
    $realm.resetPasswordAllowed = $true
} else {
    $realm.verifyEmail = $false
    $realm.resetPasswordAllowed = $false
}
if ($env:LORE_LDAP_URL) {
    $startTls = $(if ($env:LORE_LDAP_STARTTLS) { $env:LORE_LDAP_STARTTLS } else { "false" })
    $truststore = $(if ($env:LORE_LDAP_INSECURE_SKIP_VERIFY -eq "true") { "never" } else { "always" })
    $providerJson = Get-Content keycloak/ldap-provider.json -Raw
    $ldapValues = [ordered]@{
        "__LORE_LDAP_URL__" = $env:LORE_LDAP_URL
        "__LORE_LDAP_BASE_DN__" = $env:LORE_LDAP_BASE_DN
        "__LORE_LDAP_BIND_DN__" = $env:LORE_LDAP_BIND_DN
        "__LORE_LDAP_BIND_PASSWORD__" = $env:LORE_LDAP_BIND_PASSWORD
        "__LORE_LDAP_STARTTLS__" = $startTls
        "__LORE_LDAP_USE_TRUSTSTORE__" = $truststore
    }
    foreach ($entry in $ldapValues.GetEnumerator()) {
        $providerJson = $providerJson.Replace('"' + $entry.Key + '"', ($entry.Value | ConvertTo-Json -Compress))
    }
    $provider = $providerJson | ConvertFrom-Json
    $realm.components = @{ "org.keycloak.storage.UserStorageProvider" = @($provider) }
    $adHost = ([Uri]$env:LORE_LDAP_URL).DnsSafeHost
    if (-not $adHost.Contains(".")) {
        $adAddress = Resolve-DnsName $adHost -Type A -ErrorAction SilentlyContinue |
            Select-Object -First 1 -ExpandProperty IPAddress
        if ($adAddress) {
            Set-EnvSetting "LORE_AD_HOST" $adHost
            Set-EnvSetting "LORE_AD_IP" $adAddress
        }
    }
}
$realm | ConvertTo-Json -Depth 100 | Write-Utf8NoBom "$generated/lore-realm.json"
Remove-Item "$generated/caddy/auth.conf" -ErrorAction SilentlyContinue
if ($AuthUrl -ne "$PublicUrl/auth") {
    $authAddress = ([Uri]$AuthUrl).Authority
    Write-Utf8NoBom "$generated/caddy/auth.conf" "$authAddress`n`nreverse_proxy keycloak:8080`n"
}

$env:LORE_WEB_PUBLIC_URL = $PublicUrl
$env:LORE_EXCHANGE_PUBLIC_URL = $PublicUrl
$env:KEYCLOAK_PUBLIC_URL = $AuthUrl
$env:LORE_PUBLIC_ADDRESS = if ($Scheme -eq "https") { ([Uri]$PublicUrl).Authority } else { $PublicUrl }
if (-not $env:LORE_PROXY_BIND) { $env:LORE_PROXY_BIND = "0.0.0.0" }
$env:LORE_REALM_FILE = "./data/generated/lore-realm.json"
$env:LORE_CONFIG_FILE = "./data/generated/lore.toml"
$env:LORE_TOKEN_SIGNING_KEY_FILE = "/data/token-exchange-signing.pem"
$env:SECURE_COOKIES = if ($Scheme -eq "https") { "true" } else { "false" }
$env:POSTGRES_PASSWORD = $settings.POSTGRES_PASSWORD
$env:KEYCLOAK_ADMIN_PASSWORD = $settings.KEYCLOAK_ADMIN_PASSWORD
$env:LORE_SERVICE_CLIENT_SECRET = $settings.LORE_SERVICE_CLIENT_SECRET
$direct = $DirectCaddy -or $env:LORE_DIRECT_CADDY -eq "true"
$localCa = $direct -and ($PublicHost -eq "localhost" -or $PublicHost.EndsWith(".internal"))
$composeFiles = @("compose", "-f", "compose.yaml")
if ($direct) { $composeFiles += @("-f", "compose.caddy.yaml") }
if ($localCa) { $composeFiles += @("-f", "compose.caddy-local.yaml") }
$arguments = @("up", "-d", "--wait", "--wait-timeout", "240")
if (-not $SkipBuild) { $arguments += "--build" }
if ($localCa) {
    & docker @composeFiles @arguments caddy
    if ($LASTEXITCODE -ne 0) { throw "Caddy startup failed" }
    $certificate = Join-Path $(if ($env:LORE_DATA_ROOT) { $env:LORE_DATA_ROOT } else { "./data" }) "caddy/caddy/pki/authorities/local/root.crt"
    for ($attempt = 0; $attempt -lt 12 -and -not (Test-Path $certificate); $attempt++) { Start-Sleep 2 }
    if (-not (Test-Path $certificate)) { throw "Caddy local CA wasn't created" }
}
& docker @composeFiles @arguments
if ($LASTEXITCODE -ne 0) { throw "Docker Compose failed" }
& docker @composeFiles exec -T keycloak bash -c 'set -e; KC=/opt/keycloak/bin/kcadm.sh; "$KC" config credentials --server http://127.0.0.1:8080/auth --realm master --user "$KC_BOOTSTRAP_ADMIN_USERNAME" --password "$KC_BOOTSTRAP_ADMIN_PASSWORD"; "$KC" add-roles -r lore --uusername service-account-lore-control-plane --cclientid realm-management --rolename manage-realm; "$KC" add-roles -r lore --uusername service-account-lore-control-plane --cclientid realm-management --rolename manage-users'
if ($LASTEXITCODE -ne 0) { throw "Keycloak service account configuration failed" }

Write-Host "`nLore is ready.`nWebUI: $PublicUrl`nKeycloak: $AuthUrl`nAdministrator: admin`nInitial administrator password: $($settings.LORE_ADMIN_PASSWORD)`nData: $PSScriptRoot/data"
