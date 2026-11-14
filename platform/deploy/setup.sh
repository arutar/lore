#!/usr/bin/env sh
set -eu

cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
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
saved() { [ -f .env ] && sed -n "s/^$1=//p" .env | tr -d '\r' | tail -n 1 || true; }
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
load_env
PUBLIC_HOST=${LORE_PUBLIC_HOST:-$(default_host)}
SCHEME=${LORE_PUBLIC_SCHEME:-http}
if [ "$SCHEME" = "https" ]; then
    DEFAULT_WEB_URL="https://$PUBLIC_HOST"
else
    DEFAULT_WEB_URL="http://$PUBLIC_HOST:8080"
fi
# Saved addresses are reused only while the host name and scheme stay the same: a new
# LORE_PUBLIC_HOST must not keep the old URL (sign-in would then point at the previous name).
if [ "$PUBLIC_HOST" = "$SAVED_HOST" ] && [ "$SCHEME" = "$SAVED_SCHEME" ] && [ -z "$EXPLICIT_URL" ]; then
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
    umask 077
    {
        echo "POSTGRES_PASSWORD=$(random_hex)"
        echo "KEYCLOAK_ADMIN_PASSWORD=$(random_hex)"
        echo "LORE_ADMIN_PASSWORD=$(random_hex)"
        echo "LORE_SERVICE_CLIENT_SECRET=$(random_hex)"
        echo "LORE_PUBLIC_URL=$WEB_URL"
        echo "LORE_PUBLIC_HOST=$PUBLIC_HOST"
        echo "LORE_PUBLIC_SCHEME=$SCHEME"
    } > .env
fi
chmod 600 .env 2>/dev/null || true
load_env
: "${POSTGRES_PASSWORD:?}" "${KEYCLOAK_ADMIN_PASSWORD:?}" "${LORE_ADMIN_PASSWORD:?}" "${LORE_SERVICE_CLIENT_SECRET:?}"

# Every path below derives from the data root, so a custom LORE_DATA_ROOT moves the
# generated configuration together with the data that compose mounts from it.
DATA_ROOT=${LORE_DATA_ROOT:-./data}
GENERATED=$DATA_ROOT/generated
mkdir -p "$GENERATED/caddy" "$DATA_ROOT/postgres" "$DATA_ROOT/lore" "$DATA_ROOT/control-plane" "$DATA_ROOT/caddy"
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
set_env LORE_AUTH_PUBLIC_URL "$KEYCLOAK_URL"
set_env LORE_PUBLIC_ADDRESS "$PUBLIC_ADDRESS"
if [ "$SCHEME" = "https" ]; then set_env SECURE_COOKIES true; else set_env SECURE_COOKIES false; fi
for name in LORE_LDAP_URL LORE_LDAP_BASE_DN LORE_LDAP_BIND_DN LORE_LDAP_BIND_PASSWORD \
    LORE_LDAP_USERNAME_ATTRIBUTE LORE_LDAP_STARTTLS LORE_LDAP_INSECURE_SKIP_VERIFY LORE_LDAP_TIMEOUT \
    LORE_LDAP_REFRESH LORE_LDAP_OUTAGE_GRACE \
    LORE_SMTP_HOST LORE_SMTP_PORT LORE_SMTP_FROM LORE_SMTP_FROM_NAME LORE_SMTP_USERNAME LORE_SMTP_PASSWORD \
    LORE_SMTP_STARTTLS LORE_SMTP_SSL LORE_DATA_ROOT LORE_PROXY_BIND LORE_CONTROL_PLANE_IMAGE LORE_SERVER_IMAGE; do
    eval "value=\${$name:-}"
    if [ -n "$value" ]; then set_env "$name" "$value"; fi
done
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
export LORE_WEB_PUBLIC_URL=$WEB_URL
export LORE_EXCHANGE_PUBLIC_URL=$WEB_URL
export KEYCLOAK_PUBLIC_URL=$KEYCLOAK_URL
export LORE_PUBLIC_ADDRESS=$PUBLIC_ADDRESS
export LORE_PROXY_BIND=${LORE_PROXY_BIND:-0.0.0.0}
export LORE_REALM_FILE=$GENERATED/lore-realm.json
export LORE_CONFIG_FILE=$GENERATED/lore.toml
export LORE_TOKEN_SIGNING_KEY_FILE=//data/token-exchange-signing.pem
if [ "$SCHEME" = "https" ]; then export SECURE_COOKIES=true; else export SECURE_COOKIES=false; fi

# Published images (LORE_CONTROL_PLANE_IMAGE and LORE_SERVER_IMAGE, for example
# ghcr.io/arutar/lore-platform-control-plane:0.1.12) are pulled; otherwise both are built
# from this checkout, which compiles the Rust server and takes much longer.
if [ -n "${LORE_CONTROL_PLANE_IMAGE:-}" ] && [ -n "${LORE_SERVER_IMAGE:-}" ]; then
    BUILD=--pull=always
else
    BUILD=--build
fi
if [ "${LORE_DIRECT_CADDY:-false}" = "true" ]; then
    COMPOSE_FILES="-f compose.yaml -f compose.caddy.yaml"
    case "$PUBLIC_HOST" in localhost|*.internal) COMPOSE_FILES="$COMPOSE_FILES -f compose.caddy-local.yaml" ;; esac
    case "$COMPOSE_FILES" in
        *caddy-local.yaml)
            docker compose $COMPOSE_FILES up -d $BUILD --wait --wait-timeout 240 caddy
            CERTIFICATE="${LORE_DATA_ROOT:-./data}/caddy/caddy/pki/authorities/local/root.crt"
            for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
                [ -f "$CERTIFICATE" ] && break
                sleep 2
            done
            [ -f "$CERTIFICATE" ] || { echo "Caddy local CA wasn't created" >&2; exit 1; }
            ;;
    esac
    docker compose $COMPOSE_FILES up -d $BUILD --wait --wait-timeout 240
else
    COMPOSE_FILES="-f compose.yaml"
    docker compose $COMPOSE_FILES up -d $BUILD --wait --wait-timeout 240
fi

# The realm file is imported only on the first start; bring an existing realm in line with
# the current settings. Secrets reach the script through the container environment.
docker compose $COMPOSE_FILES exec -T \
    -e LORE_PUBLIC_URL="$WEB_URL" -e LORE_PUBLIC_HOST="$PUBLIC_HOST" -e LORE_PUBLIC_SCHEME="$SCHEME" \
    -e LORE_SMTP_HOST="${LORE_SMTP_HOST:-}" -e LORE_SMTP_PORT="${LORE_SMTP_PORT:-}" -e LORE_SMTP_FROM="${LORE_SMTP_FROM:-}" \
    -e LORE_SMTP_FROM_NAME="${LORE_SMTP_FROM_NAME:-}" -e LORE_SMTP_USERNAME="${LORE_SMTP_USERNAME:-}" \
    -e LORE_SMTP_STARTTLS="${LORE_SMTP_STARTTLS:-}" -e LORE_SMTP_SSL="${LORE_SMTP_SSL:-}" \
    -e LORE_LDAP_URL="${LORE_LDAP_URL:-}" -e LORE_LDAP_BASE_DN="${LORE_LDAP_BASE_DN:-}" -e LORE_LDAP_BIND_DN="${LORE_LDAP_BIND_DN:-}" \
    -e LORE_LDAP_STARTTLS="${LORE_LDAP_STARTTLS:-}" -e LORE_LDAP_INSECURE_SKIP_VERIFY="${LORE_LDAP_INSECURE_SKIP_VERIFY:-}" \
    keycloak bash /opt/lore/configure-realm.sh

printf '\nLore is ready.\nWebUI: %s\nKeycloak: %s\nAdministrator: admin\nInitial administrator password: %s\nData: %s\n' \
    "$WEB_URL" "$KEYCLOAK_URL" "$LORE_ADMIN_PASSWORD" "$(cd "$DATA_ROOT" && pwd)"
