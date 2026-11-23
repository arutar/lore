#!/usr/bin/env sh
set -eu

cd "$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
# --client-downloads online|offline|off: where the WebUI's home page gets the client from
# (LORE_CLIENT_DOWNLOADS, saved in .env). See "Client downloads" below.
# --mcp off|internal|on and --mcp-networks "10.0.0.0/16;192.168.0.0/24": the MCP endpoint for AI
# agents (LORE_MCP, LORE_MCP_NETWORKS, saved in .env). See "MCP" below and docs/mcp.md.
while [ $# -gt 0 ]; do
    case "$1" in
        --client-downloads=*) LORE_CLIENT_DOWNLOADS=${1#*=}; export LORE_CLIENT_DOWNLOADS ;;
        --client-downloads) [ $# -ge 2 ] || { echo "--client-downloads needs online, offline or off" >&2; exit 1; }
            LORE_CLIENT_DOWNLOADS=$2; export LORE_CLIENT_DOWNLOADS; shift ;;
        --mcp=*) LORE_MCP=${1#*=}; export LORE_MCP ;;
        --mcp) [ $# -ge 2 ] || { echo "--mcp needs off, internal or on" >&2; exit 1; }
            LORE_MCP=$2; export LORE_MCP; shift ;;
        --mcp-networks=*) LORE_MCP_NETWORKS=${1#*=}; export LORE_MCP_NETWORKS ;;
        --mcp-networks) [ $# -ge 2 ] || { echo "--mcp-networks needs networks such as \"10.0.0.0/16;192.168.0.0/24\"" >&2; exit 1; }
            LORE_MCP_NETWORKS=$2; export LORE_MCP_NETWORKS; shift ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
    shift
done
command -v docker >/dev/null 2>&1 || { echo "Docker is required" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 is required" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "Docker engine isn't running" >&2; exit 1; }

# load_env reads KEY=VALUE lines literally (no shell evaluation, so values may contain
# spaces, quotes or $). A variable already set in the environment wins over .env, so
# `LORE_PUBLIC_HOST=new.example sh setup.sh` changes a saved setting.
load_env() {
    [ -f .env ] || return 0
    while IFS= read -r line || [ -n "$line" ]; do
        line=${line%"$(printf '\r')"}
        case "$line" in
            ''|'#'*) continue ;;
            *=*) key=${line%%=*}; value=${line#*=} ;;
            *) continue ;;
        esac
        case "$key" in ''|[0-9]*|*[!A-Za-z0-9_]*) continue ;; esac
        if eval "[ -z \"\${$key+set}\" ]"; then export "$key=$value"; fi
    done < .env
}
saved() { if [ -f .env ]; then sed -n "s/^$1=//p" .env | tr -d '\r' | tail -n 1; fi; }
# This computer's DNS name (FQDN when it has one): it resolves for browsers and clients on the
# network and for this machine itself. Containers reach it through the proxy's network alias.
default_host() {
    name=$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)
    name=$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')
    # A name with "_" or other characters DNS doesn't allow breaks URL parsing in Keycloak (Java);
    # use this computer's network address instead.
    case "$name" in
        ''|localhost*|*[!a-z0-9.-]*)
            address=$(hostname -I 2>/dev/null | awk '{print $1}')
            if [ -n "$address" ]; then echo "$address"; else echo host.docker.internal; fi ;;
        *) echo "$name" ;;
    esac
}
# What the caller passed explicitly, before .env fills in the saved values.
EXPLICIT_URL=${LORE_PUBLIC_URL:-}
EXPLICIT_AUTH_URL=${LORE_AUTH_PUBLIC_URL:-}
EXPLICIT_ADDRESS=${LORE_PUBLIC_ADDRESS:-}
SAVED_HOST=$(saved LORE_PUBLIC_HOST)
SAVED_SCHEME=$(saved LORE_PUBLIC_SCHEME)
SAVED_PORT=$(saved LORE_HTTP_PORT)
load_env
PUBLIC_HOST=${LORE_PUBLIC_HOST:-$(default_host)}
SCHEME=${LORE_PUBLIC_SCHEME:-http}
# Port of the WebUI in HTTP mode, when 8080 is taken on this host.
HTTP_PORT=${LORE_HTTP_PORT:-8080}
case "$HTTP_PORT" in ''|*[!0-9]*) echo "LORE_HTTP_PORT must be a port number" >&2; exit 1 ;; esac
if [ "$SCHEME" = "https" ]; then
    DEFAULT_WEB_URL="https://$PUBLIC_HOST"
else
    DEFAULT_WEB_URL="http://$PUBLIC_HOST:$HTTP_PORT"
fi
# Saved addresses are reused only while the host name and scheme stay the same: a new
# LORE_PUBLIC_HOST must not keep the old URL (sign-in would then point at the previous name).
if [ "$PUBLIC_HOST" = "$SAVED_HOST" ] && [ "$SCHEME" = "$SAVED_SCHEME" ] && [ "$HTTP_PORT" = "${SAVED_PORT:-8080}" ] && [ -z "$EXPLICIT_URL" ]; then
    WEB_URL=${LORE_PUBLIC_URL:-$DEFAULT_WEB_URL}
    KEYCLOAK_URL=${EXPLICIT_AUTH_URL:-${LORE_AUTH_PUBLIC_URL:-$WEB_URL/auth}}
    PUBLIC_ADDRESS=${EXPLICIT_ADDRESS:-${LORE_PUBLIC_ADDRESS:-}}
else
    WEB_URL=${EXPLICIT_URL:-$DEFAULT_WEB_URL}
    KEYCLOAK_URL=${EXPLICIT_AUTH_URL:-$WEB_URL/auth}
    PUBLIC_ADDRESS=$EXPLICIT_ADDRESS
fi
if [ -z "$PUBLIC_ADDRESS" ]; then
    if [ "$SCHEME" = "https" ]; then PUBLIC_ADDRESS=$PUBLIC_HOST; else PUBLIC_ADDRESS=$WEB_URL; fi
fi

random_hex() { od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; }
if [ ! -f .env ]; then
    # Only .env is private to its owner; the umask mustn't leak into the data directories below,
    # whose files the containers (other users) have to read.
    (umask 077 && {
        echo "POSTGRES_PASSWORD=$(random_hex)"
        echo "KEYCLOAK_ADMIN_PASSWORD=$(random_hex)"
        echo "LORE_ADMIN_PASSWORD=$(random_hex)"
        echo "LORE_SERVICE_CLIENT_SECRET=$(random_hex)"
        echo "LORE_PUBLIC_URL=$WEB_URL"
        echo "LORE_PUBLIC_HOST=$PUBLIC_HOST"
        echo "LORE_PUBLIC_SCHEME=$SCHEME"
    } > .env)
fi
chmod 600 .env 2>/dev/null || true
load_env
: "${POSTGRES_PASSWORD:?}" "${KEYCLOAK_ADMIN_PASSWORD:?}" "${LORE_ADMIN_PASSWORD:?}" "${LORE_SERVICE_CLIENT_SECRET:?}"

# Every path below derives from the data root, so a custom LORE_DATA_ROOT moves the
# generated configuration together with the data that compose mounts from it.
DATA_ROOT=${LORE_DATA_ROOT:-./data}
GENERATED=$DATA_ROOT/generated
mkdir -p "$GENERATED/caddy" "$DATA_ROOT/postgres" "$DATA_ROOT/lore" "$DATA_ROOT/control-plane" "$DATA_ROOT/caddy" "$DATA_ROOT/downloads"
# On Linux each service writes its directory as its own user (postgres, lore, root), not as the
# user running setup. The data root itself is closed to other users of the host; the service
# directories inside are open to the containers, and what the services keep in them carries
# their own permissions (PostgreSQL 700, the signing key 600). Docker Desktop ignores both.
chmod 700 "$DATA_ROOT" 2>/dev/null || true
chmod 777 "$DATA_ROOT/postgres" "$DATA_ROOT/lore" "$DATA_ROOT/control-plane" "$DATA_ROOT/caddy" 2>/dev/null || true
chmod 755 "$GENERATED" "$GENERATED/caddy" "$DATA_ROOT/downloads" 2>/dev/null || true
escape_sed() { printf '%s' "$1" | sed 's/[&|\\]/\\&/g'; }
escape_json() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }
set_env() {
    key=$1
    value=$2
    export "$key=$value"
    escaped=$(escape_sed "$value")
    if grep -q "^$key=" .env; then
        sed -i "s|^$key=.*|$key=$escaped|" .env
    else
        printf '%s=%s\n' "$key" "$value" >> .env
    fi
}
set_env LORE_PUBLIC_URL "$WEB_URL"
set_env LORE_PUBLIC_HOST "$PUBLIC_HOST"
set_env LORE_PUBLIC_SCHEME "$SCHEME"
set_env LORE_HTTP_PORT "$HTTP_PORT"
set_env LORE_AUTH_PUBLIC_URL "$KEYCLOAK_URL"
set_env LORE_PUBLIC_ADDRESS "$PUBLIC_ADDRESS"
if [ "$SCHEME" = "https" ]; then set_env SECURE_COOKIES true; else set_env SECURE_COOKIES false; fi
for name in LORE_LDAP_URL LORE_LDAP_BASE_DN LORE_LDAP_BIND_DN LORE_LDAP_BIND_PASSWORD \
    LORE_LDAP_USERNAME_ATTRIBUTE LORE_LDAP_STARTTLS LORE_LDAP_INSECURE_SKIP_VERIFY LORE_LDAP_TIMEOUT \
    LORE_LDAP_REFRESH LORE_LDAP_OUTAGE_GRACE \
    LORE_SMTP_HOST LORE_SMTP_PORT LORE_SMTP_FROM LORE_SMTP_FROM_NAME LORE_SMTP_USERNAME LORE_SMTP_PASSWORD \
    LORE_SMTP_STARTTLS LORE_SMTP_SSL LORE_DATA_ROOT LORE_PROXY_BIND LORE_CONTROL_PLANE_IMAGE LORE_SERVER_IMAGE \
    LORE_MCP_CONCURRENCY LORE_MCP_MAX_READ LORE_MCP_LINK_TTL; do
    eval "value=\${$name:-}"
    if [ -n "$value" ]; then set_env "$name" "$value"; fi
done

# MCP for AI agents, always with a personal token (docs/mcp.md).
#   off (default): no MCP endpoint.
#   internal: only clients in LORE_MCP_NETWORKS, which you must give, such as
#     "10.0.0.0/16;192.168.0.0/24" (networks in CIDR form, separated by ";").
#   on: from anywhere the WebUI is reachable.
MCP_MODE=${LORE_MCP:-off}
case "$MCP_MODE" in off|internal|on) ;; *) echo "LORE_MCP must be off, internal or on" >&2; exit 1 ;; esac
MCP_NETWORKS=$(printf '%s' "${LORE_MCP_NETWORKS:-}" | tr ', ' ';;' | tr -s ';' | sed 's/^;//; s/;$//')
for network in $(printf '%s' "$MCP_NETWORKS" | tr ';' ' '); do
    case "$network" in
        *[!0-9A-Fa-f:./]*|'') echo "LORE_MCP_NETWORKS: \"$network\" isn't a network such as 10.0.0.0/16" >&2; exit 1 ;;
    esac
done
if [ "$MCP_MODE" = internal ] && [ -z "$MCP_NETWORKS" ]; then
    echo "--mcp internal needs the networks it answers, for example: --mcp-networks \"10.0.0.0/16;192.168.0.0/24\"" >&2
    exit 1
fi
set_env LORE_MCP "$MCP_MODE"
set_env LORE_MCP_NETWORKS "$MCP_NETWORKS"
case "$MCP_MODE" in
    off) MCP_SUMMARY=off ;;
    internal) MCP_SUMMARY="internal ($MCP_NETWORKS): $WEB_URL/mcp" ;;
    on) MCP_SUMMARY="on: $WEB_URL/mcp" ;;
esac

# Client downloads. The control plane serves DATA_ROOT/downloads at /downloads/, and the home
# page lists what manifest.json there names. The bundle's clients.tsv lists this release's client
# builds (platform, kind, architecture, size, SHA-256, file name).
#   online (default): links to the release on GitHub; nothing is stored here.
#   offline: the files are kept here, for networks without internet access. They come from
#     ./clients next to this script when it holds them, otherwise from GitHub.
#   off: the home page offers no client.
CLIENT_MODE=${LORE_CLIENT_DOWNLOADS:-online}
case "$CLIENT_MODE" in online|offline|off) ;; *) echo "LORE_CLIENT_DOWNLOADS must be online, offline or off" >&2; exit 1 ;; esac
set_env LORE_CLIENT_DOWNLOADS "$CLIENT_MODE"
DOWNLOADS=$DATA_ROOT/downloads
sha256_of() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
has_file() { [ -f "$1" ] && [ "$(sha256_of "$1")" = "$2" ]; }
rm -f "$DOWNLOADS/manifest.json"
if [ "$CLIENT_MODE" = off ] || [ ! -f clients.tsv ] || [ ! -f VERSION ]; then
    # A source checkout has no clients.tsv: the home page links to the releases instead.
    find "$DOWNLOADS" -maxdepth 1 -type f -delete
else
    VERSION=$(tr -d '\r\n' < VERSION)
    RELEASE=${LORE_CLIENT_RELEASE_URL:-https://github.com/arutar/lore/releases/download}/platform-v$VERSION
    ITEMS=""
    KEEP=" manifest.json "
    TAB=$(printf '\t')
    while IFS=$TAB read -r platform kind arch size sha file || [ -n "${file:-}" ]; do
        file=${file%"$(printf '\r')"}
        [ -n "$file" ] || continue
        case "$file" in *[!A-Za-z0-9._-]*|.*) echo "clients.tsv names an invalid file: $file" >&2; exit 1 ;; esac
        case "$platform$kind$arch" in *[!a-z0-9_]*) echo "clients.tsv has an invalid line for $file" >&2; exit 1 ;; esac
        case "$size" in ''|*[!0-9]*) echo "clients.tsv has an invalid size for $file" >&2; exit 1 ;; esac
        case "$sha" in *[!0-9a-f]*) echo "clients.tsv has an invalid SHA-256 for $file" >&2; exit 1 ;; esac
        [ ${#sha} -eq 64 ] || { echo "clients.tsv has an invalid SHA-256 for $file" >&2; exit 1; }
        if [ "$CLIENT_MODE" = offline ]; then
            if ! has_file "$DOWNLOADS/$file" "$sha"; then
                if [ -f "clients/$file" ]; then
                    cp "clients/$file" "$DOWNLOADS/$file.part"
                else
                    echo "Downloading $file"
                    curl -fL# --retry 3 -o "$DOWNLOADS/$file.part" "$RELEASE/$file" || {
                        rm -f "$DOWNLOADS/$file.part"
                        echo "Couldn't download $file. Put the release's client files in $(pwd)/clients and run setup again." >&2; exit 1; }
                fi
                has_file "$DOWNLOADS/$file.part" "$sha" || { rm -f "$DOWNLOADS/$file.part"; echo "$file doesn't match its SHA-256 in clients.tsv" >&2; exit 1; }
                mv "$DOWNLOADS/$file.part" "$DOWNLOADS/$file"
                chmod 644 "$DOWNLOADS/$file"
            fi
            KEEP="$KEEP$file "
            url="/downloads/$file"
        else
            url="$RELEASE/$file"
        fi
        ITEMS="$ITEMS${ITEMS:+,}{\"platform\":\"$platform\",\"kind\":\"$kind\",\"arch\":\"$arch\",\"file\":\"$file\",\"url\":\"$url\",\"size\":$size,\"sha256\":\"$sha\"}"
    done < clients.tsv
    # Files of an earlier version, or all of them after switching to online, go.
    for path in "$DOWNLOADS"/* "$DOWNLOADS"/.[!.]*; do
        [ -f "$path" ] || continue
        case "$KEEP" in *" ${path##*/} "*) ;; *) rm -f "$path" ;; esac
    done
    if [ "$CLIENT_MODE" = offline ]; then SOURCE=server; else SOURCE=github; fi
    printf '{"version":"%s","source":"%s","items":[%s]}\n' "$VERSION" "$SOURCE" "$ITEMS" > "$DOWNLOADS/manifest.json.part"
    chmod 644 "$DOWNLOADS/manifest.json.part"
    mv "$DOWNLOADS/manifest.json.part" "$DOWNLOADS/manifest.json"
fi

WEB_ESCAPED=$(escape_sed "$WEB_URL")
KEYCLOAK_ESCAPED=$(escape_sed "$KEYCLOAK_URL")
HOST_ESCAPED=$(escape_sed "$PUBLIC_HOST")
sed -e "s|http://host.docker.internal:8080/auth|$KEYCLOAK_ESCAPED|g" -e "s|http://host.docker.internal:8080|$WEB_ESCAPED|g" \
    lore/docker.toml > "$GENERATED/lore.toml"
sed -e "s|http://localhost:8080|$WEB_ESCAPED|g" -e "s|host.docker.internal|$HOST_ESCAPED|g" \
    keycloak/lore-realm.json > "$GENERATED/lore-realm.json"
if [ "$SCHEME" = "https" ]; then
    sed -i 's/"sslRequired": "none"/"sslRequired": "external"/' "$GENERATED/lore-realm.json"
fi
if [ -n "${LORE_SMTP_HOST:-}" ]; then
    case "${LORE_SMTP_FROM:-}" in
        *\<*\>*) LORE_SMTP_FROM=$(printf '%s' "$LORE_SMTP_FROM" | sed -n 's/.*<\([^>]*\)>.*/\1/p') ;;
    esac
    SMTP_AUTH=false
    if [ -n "${LORE_SMTP_USERNAME:-}" ] || [ -n "${LORE_SMTP_PASSWORD:-}" ]; then SMTP_AUTH=true; fi
    # Keycloak expands the password placeholder in the generated realm.
    # shellcheck disable=SC2016
    SMTP_BLOCK=$(printf '"smtpServer": {"host":"%s","port":"%s","from":"%s","fromDisplayName":"%s","starttls":"%s","ssl":"%s","auth":"%s","user":"%s","password":"%s"}' \
        "$(escape_json "$LORE_SMTP_HOST")" "$(escape_json "${LORE_SMTP_PORT:-587}")" "$(escape_json "${LORE_SMTP_FROM:-}")" \
        "$(escape_json "${LORE_SMTP_FROM_NAME:-Lore}")" "$(escape_json "${LORE_SMTP_STARTTLS:-true}")" "$(escape_json "${LORE_SMTP_SSL:-false}")" \
        "$SMTP_AUTH" "$(escape_json "${LORE_SMTP_USERNAME:-}")" '${LORE_SMTP_PASSWORD}')
    SMTP_BLOCK="$SMTP_BLOCK" awk '{if (index($0, "\"smtpServer\": {}")) print ENVIRON["SMTP_BLOCK"] ","; else print}' "$GENERATED/lore-realm.json" > "$GENERATED/lore-realm.tmp"
    mv "$GENERATED/lore-realm.tmp" "$GENERATED/lore-realm.json"
    sed -i 's/"resetPasswordAllowed": false/"resetPasswordAllowed": true/' "$GENERATED/lore-realm.json"
else
    sed -i -e 's/"verifyEmail": true/"verifyEmail": false/' -e 's/"resetPasswordAllowed": true/"resetPasswordAllowed": false/' "$GENERATED/lore-realm.json"
fi
if [ -n "${LORE_LDAP_URL:-}" ]; then
    LDAP_STARTTLS=${LORE_LDAP_STARTTLS:-false}
    LDAP_TRUSTSTORE=always
    if [ "${LORE_LDAP_INSECURE_SKIP_VERIFY:-false}" = "true" ]; then LDAP_TRUSTSTORE=never; fi
    LDAP_URL=$(escape_sed "$(escape_json "$LORE_LDAP_URL")")
    LDAP_BASE_DN=$(escape_sed "$(escape_json "$LORE_LDAP_BASE_DN")")
    LDAP_BIND_DN=$(escape_sed "$(escape_json "$LORE_LDAP_BIND_DN")")
    sed -e "s|__LORE_LDAP_URL__|$LDAP_URL|g" -e "s|__LORE_LDAP_BASE_DN__|$LDAP_BASE_DN|g" \
        -e "s|__LORE_LDAP_BIND_DN__|$LDAP_BIND_DN|g" \
        -e "s|__LORE_LDAP_STARTTLS__|$LDAP_STARTTLS|g" -e "s|__LORE_LDAP_USE_TRUSTSTORE__|$LDAP_TRUSTSTORE|g" \
        keycloak/ldap-provider.json > "$GENERATED/ldap-provider.json"
    awk 'FNR==NR {provider=provider $0 "\n"; next} index($0, "\"components\": {}") {printf "\"components\": {\"org.keycloak.storage.UserStorageProvider\":[\n%s]},\n", provider; next} {print}' \
        "$GENERATED/ldap-provider.json" "$GENERATED/lore-realm.json" > "$GENERATED/lore-realm.tmp"
    mv "$GENERATED/lore-realm.tmp" "$GENERATED/lore-realm.json"
    LDAP_HOST=$(printf '%s' "$LORE_LDAP_URL" | sed -E 's|^[a-zA-Z]+://||; s|[:/].*$||')
    case "$LDAP_HOST" in
        *.*|localhost) ;;
        *)
            LDAP_IP=$(getent ahostsv4 "$LDAP_HOST" 2>/dev/null | awk 'NR == 1 {print $1}')
            if [ -n "$LDAP_IP" ]; then
                set_env LORE_AD_HOST "$LDAP_HOST"
                set_env LORE_AD_IP "$LDAP_IP"
            fi
            ;;
    esac
fi
rm -f "$GENERATED/caddy/auth.conf"
if [ "$KEYCLOAK_URL" != "$WEB_URL/auth" ]; then
    AUTH_ADDRESS=$(printf '%s' "$KEYCLOAK_URL" | sed -E 's|^https?://||; s|/.*$||')
    printf '%s {\n\timport keycloak_guard\n\thandle {\n\t\treverse_proxy keycloak:8080\n\t}\n}\n' "$AUTH_ADDRESS" > "$GENERATED/caddy/auth.conf"
fi

export POSTGRES_PASSWORD KEYCLOAK_ADMIN_PASSWORD LORE_SERVICE_CLIENT_SECRET
export LORE_WEB_PUBLIC_URL="$WEB_URL"
export LORE_EXCHANGE_PUBLIC_URL="$WEB_URL"
export KEYCLOAK_PUBLIC_URL="$KEYCLOAK_URL"
export LORE_PUBLIC_ADDRESS="$PUBLIC_ADDRESS"
export LORE_PROXY_BIND="${LORE_PROXY_BIND:-0.0.0.0}"
export LORE_REALM_FILE="$GENERATED/lore-realm.json"
export LORE_CONFIG_FILE="$GENERATED/lore.toml"
export LORE_TOKEN_SIGNING_KEY_FILE=//data/token-exchange-signing.pem
if [ "$SCHEME" = "https" ]; then export SECURE_COOKIES=true; else export SECURE_COOKIES=false; fi

# Published images (LORE_CONTROL_PLANE_IMAGE and LORE_SERVER_IMAGE, for example
# ghcr.io/arutar/lore-platform-control-plane:0.1.12) are pulled; otherwise both are built
# from this checkout, which compiles the Rust server and takes much longer.
# LORE_SKIP_BUILD=true uses the images already on this host as they are: networks without
# internet access, where the images arrive with `docker load`.
if [ "${LORE_SKIP_BUILD:-false}" = "true" ]; then
    BUILD=--pull=never
elif [ -n "${LORE_CONTROL_PLANE_IMAGE:-}" ] && [ -n "${LORE_SERVER_IMAGE:-}" ]; then
    BUILD=--pull=always
else
    BUILD=--build
fi
set -- -f compose.yaml
if [ "${LORE_DIRECT_CADDY:-false}" = "true" ]; then
    set -- "$@" -f compose.caddy.yaml
    case "$PUBLIC_HOST" in
        localhost|*.internal)
            set -- "$@" -f compose.caddy-local.yaml
            docker compose "$@" up -d "$BUILD" --wait --wait-timeout 240 caddy
            CERTIFICATE="${LORE_DATA_ROOT:-./data}/caddy/caddy/pki/authorities/local/root.crt"
            for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
                [ -f "$CERTIFICATE" ] && break
                sleep 2
            done
            [ -f "$CERTIFICATE" ] || { echo "Caddy local CA wasn't created" >&2; exit 1; }
            ;;
    esac
fi
docker compose "$@" up -d "$BUILD" --wait --wait-timeout 240

# The realm file is imported only on the first start; bring an existing realm in line with
# the current settings. Secrets reach the script through the container environment.
docker compose "$@" exec -T \
    -e LORE_PUBLIC_URL="$WEB_URL" -e LORE_PUBLIC_HOST="$PUBLIC_HOST" -e LORE_PUBLIC_SCHEME="$SCHEME" \
    -e LORE_SMTP_HOST="${LORE_SMTP_HOST:-}" -e LORE_SMTP_PORT="${LORE_SMTP_PORT:-}" -e LORE_SMTP_FROM="${LORE_SMTP_FROM:-}" \
    -e LORE_SMTP_FROM_NAME="${LORE_SMTP_FROM_NAME:-}" -e LORE_SMTP_USERNAME="${LORE_SMTP_USERNAME:-}" \
    -e LORE_SMTP_STARTTLS="${LORE_SMTP_STARTTLS:-}" -e LORE_SMTP_SSL="${LORE_SMTP_SSL:-}" \
    -e LORE_LDAP_URL="${LORE_LDAP_URL:-}" -e LORE_LDAP_BASE_DN="${LORE_LDAP_BASE_DN:-}" -e LORE_LDAP_BIND_DN="${LORE_LDAP_BIND_DN:-}" \
    -e LORE_LDAP_STARTTLS="${LORE_LDAP_STARTTLS:-}" -e LORE_LDAP_INSECURE_SKIP_VERIFY="${LORE_LDAP_INSECURE_SKIP_VERIFY:-}" \
    keycloak bash /opt/lore/configure-realm.sh

printf '\nLore is ready.\nWebUI: %s\nKeycloak: %s\nAdministrator: admin\nInitial administrator password: %s\nData: %s\nClient downloads: %s\nMCP: %s\n' \
    "$WEB_URL" "$KEYCLOAK_URL" "$LORE_ADMIN_PASSWORD" "$(cd "$DATA_ROOT" && pwd)" "$CLIENT_MODE" "$MCP_SUMMARY"
