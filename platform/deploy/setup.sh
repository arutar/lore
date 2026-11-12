#!/usr/bin/env sh
set -eu

cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
command -v docker >/dev/null 2>&1 || { echo "Docker is required" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 is required" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "Docker engine isn't running" >&2; exit 1; }

if [ -f .env ]; then
    set -a; . ./.env; set +a
fi
PUBLIC_HOST=${LORE_PUBLIC_HOST:-host.docker.internal}
SCHEME=${LORE_PUBLIC_SCHEME:-http}
if [ "$SCHEME" = "https" ]; then
    DEFAULT_WEB_URL="https://$PUBLIC_HOST"
else
    DEFAULT_WEB_URL="http://$PUBLIC_HOST:8080"
fi
WEB_URL=${LORE_PUBLIC_URL:-$DEFAULT_WEB_URL}
KEYCLOAK_URL=${LORE_AUTH_PUBLIC_URL:-$WEB_URL/auth}
PUBLIC_ADDRESS=${LORE_PUBLIC_ADDRESS:-}
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
set -a; . ./.env; set +a
: "${POSTGRES_PASSWORD:?}" "${KEYCLOAK_ADMIN_PASSWORD:?}" "${LORE_ADMIN_PASSWORD:?}" "${LORE_SERVICE_CLIENT_SECRET:?}"

mkdir -p data/generated/caddy data/postgres data/lore data/control-plane data/caddy
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
    LORE_SMTP_HOST LORE_SMTP_PORT LORE_SMTP_FROM LORE_SMTP_FROM_NAME LORE_SMTP_USERNAME LORE_SMTP_PASSWORD \
    LORE_SMTP_STARTTLS LORE_SMTP_SSL LORE_DATA_ROOT LORE_PROXY_BIND; do
    eval "value=\${$name:-}"
    if [ -n "$value" ]; then set_env "$name" "$value"; fi
done
WEB_ESCAPED=$(escape_sed "$WEB_URL")
KEYCLOAK_ESCAPED=$(escape_sed "$KEYCLOAK_URL")
HOST_ESCAPED=$(escape_sed "$PUBLIC_HOST")
SERVICE_ESCAPED=$(escape_sed "$LORE_SERVICE_CLIENT_SECRET")
ADMIN_ESCAPED=$(escape_sed "$LORE_ADMIN_PASSWORD")
sed -e "s|http://host.docker.internal:8080/auth|$KEYCLOAK_ESCAPED|g" -e "s|http://host.docker.internal:8080|$WEB_ESCAPED|g" \
    lore/docker.toml > data/generated/lore.toml
sed -e "s|http://localhost:8080|$WEB_ESCAPED|g" -e "s|host.docker.internal|$HOST_ESCAPED|g" \
    -e "s|lore-service-dev-only|$SERVICE_ESCAPED|g" -e "s|admin-dev-only|$ADMIN_ESCAPED|g" \
    keycloak/lore-realm.json > data/generated/lore-realm.json
if [ "$SCHEME" = "https" ]; then
    sed -i 's/"sslRequired": "none"/"sslRequired": "external"/' data/generated/lore-realm.json
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
        "$SMTP_AUTH" "$(escape_json "${LORE_SMTP_USERNAME:-}")" "$(escape_json "${LORE_SMTP_PASSWORD:-}")")
    SMTP_BLOCK="$SMTP_BLOCK" awk '{if (index($0, "\"smtpServer\": {}")) print ENVIRON["SMTP_BLOCK"] ","; else print}' data/generated/lore-realm.json > data/generated/lore-realm.tmp
    mv data/generated/lore-realm.tmp data/generated/lore-realm.json
    sed -i 's/"resetPasswordAllowed": false/"resetPasswordAllowed": true/' data/generated/lore-realm.json
else
    sed -i -e 's/"verifyEmail": true/"verifyEmail": false/' -e 's/"resetPasswordAllowed": true/"resetPasswordAllowed": false/' data/generated/lore-realm.json
fi
if [ -n "${LORE_LDAP_URL:-}" ]; then
    LDAP_STARTTLS=${LORE_LDAP_STARTTLS:-false}
    LDAP_TRUSTSTORE=always
    if [ "${LORE_LDAP_INSECURE_SKIP_VERIFY:-false}" = "true" ]; then LDAP_TRUSTSTORE=never; fi
    LDAP_URL=$(escape_sed "$(escape_json "$LORE_LDAP_URL")")
    LDAP_BASE_DN=$(escape_sed "$(escape_json "$LORE_LDAP_BASE_DN")")
    LDAP_BIND_DN=$(escape_sed "$(escape_json "$LORE_LDAP_BIND_DN")")
    LDAP_BIND_PASSWORD=$(escape_sed "$(escape_json "$LORE_LDAP_BIND_PASSWORD")")
    sed -e "s|__LORE_LDAP_URL__|$LDAP_URL|g" -e "s|__LORE_LDAP_BASE_DN__|$LDAP_BASE_DN|g" \
        -e "s|__LORE_LDAP_BIND_DN__|$LDAP_BIND_DN|g" -e "s|__LORE_LDAP_BIND_PASSWORD__|$LDAP_BIND_PASSWORD|g" \
        -e "s|__LORE_LDAP_STARTTLS__|$LDAP_STARTTLS|g" -e "s|__LORE_LDAP_USE_TRUSTSTORE__|$LDAP_TRUSTSTORE|g" \
        keycloak/ldap-provider.json > data/generated/ldap-provider.json
    awk 'FNR==NR {provider=provider $0 "\n"; next} index($0, "\"components\": {}") {printf "\"components\": {\"org.keycloak.storage.UserStorageProvider\":[\n%s]},\n", provider; next} {print}' \
        data/generated/ldap-provider.json data/generated/lore-realm.json > data/generated/lore-realm.tmp
    mv data/generated/lore-realm.tmp data/generated/lore-realm.json
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
rm -f data/generated/caddy/auth.conf
if [ "$KEYCLOAK_URL" != "$WEB_URL/auth" ]; then
    AUTH_ADDRESS=$(printf '%s' "$KEYCLOAK_URL" | sed -E 's|^https?://||; s|/.*$||')
    printf '%s\n\nreverse_proxy keycloak:8080\n' "$AUTH_ADDRESS" > data/generated/caddy/auth.conf
fi

export POSTGRES_PASSWORD KEYCLOAK_ADMIN_PASSWORD LORE_SERVICE_CLIENT_SECRET
export LORE_WEB_PUBLIC_URL=$WEB_URL
export LORE_EXCHANGE_PUBLIC_URL=$WEB_URL
export KEYCLOAK_PUBLIC_URL=$KEYCLOAK_URL
export LORE_PUBLIC_ADDRESS=$PUBLIC_ADDRESS
export LORE_PROXY_BIND=${LORE_PROXY_BIND:-0.0.0.0}
export LORE_REALM_FILE=./data/generated/lore-realm.json
export LORE_CONFIG_FILE=./data/generated/lore.toml
export LORE_TOKEN_SIGNING_KEY_FILE=//data/token-exchange-signing.pem
if [ "$SCHEME" = "https" ]; then export SECURE_COOKIES=true; else export SECURE_COOKIES=false; fi

if [ "${LORE_DIRECT_CADDY:-false}" = "true" ]; then
    COMPOSE_FILES="-f compose.yaml -f compose.caddy.yaml"
    case "$PUBLIC_HOST" in localhost|*.internal) COMPOSE_FILES="$COMPOSE_FILES -f compose.caddy-local.yaml" ;; esac
    case "$COMPOSE_FILES" in
        *caddy-local.yaml)
            docker compose $COMPOSE_FILES up -d --build --wait --wait-timeout 240 caddy
            CERTIFICATE="${LORE_DATA_ROOT:-./data}/caddy/caddy/pki/authorities/local/root.crt"
            for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
                [ -f "$CERTIFICATE" ] && break
                sleep 2
            done
            [ -f "$CERTIFICATE" ] || { echo "Caddy local CA wasn't created" >&2; exit 1; }
            ;;
    esac
    docker compose $COMPOSE_FILES up -d --build --wait --wait-timeout 240
else
    COMPOSE_FILES="-f compose.yaml"
    docker compose $COMPOSE_FILES up -d --build --wait --wait-timeout 240
fi

docker compose $COMPOSE_FILES exec -T keycloak bash -c 'set -e; KC=/opt/keycloak/bin/kcadm.sh; "$KC" config credentials --server http://127.0.0.1:8080/auth --realm master --user "$KC_BOOTSTRAP_ADMIN_USERNAME" --password "$KC_BOOTSTRAP_ADMIN_PASSWORD"; "$KC" add-roles -r lore --uusername service-account-lore-control-plane --cclientid realm-management --rolename manage-realm; "$KC" add-roles -r lore --uusername service-account-lore-control-plane --cclientid realm-management --rolename manage-users'

printf '\nLore is ready.\nWebUI: %s\nKeycloak: %s\nAdministrator: admin\nInitial administrator password: %s\nData: %s/data\n' \
    "$WEB_URL" "$KEYCLOAK_URL" "$LORE_ADMIN_PASSWORD" "$(pwd)"
