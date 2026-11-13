#!/usr/bin/env bash
# Brings the existing "lore" realm in line with the current setup settings. Keycloak
# imports lore-realm.json only when the realm doesn't exist yet, so without this step a
# changed public URL, LDAP or SMTP setting would never reach a running installation.
#
# Runs inside the Keycloak container (setup does `docker compose exec keycloak bash
# /opt/lore/configure-realm.sh`). Secrets come from the container environment; the other
# settings are passed with `exec -e`. Settings managed in the WebUI, such as whether
# registration is open, are left alone.
set -euo pipefail

KC=/opt/keycloak/bin/kcadm.sh
REALM=lore
WEB_URL=${LORE_PUBLIC_URL:?LORE_PUBLIC_URL is required}
PUBLIC_HOST=${LORE_PUBLIC_HOST:?LORE_PUBLIC_HOST is required}
SCHEME=${LORE_PUBLIC_SCHEME:-http}
SERVICE_CLIENT=lore-control-plane

# json_string quotes a value for kcadm `-s key=value`, which parses values as JSON.
json_string() {
    local value=$1
    value=${value//\\/\\\\}
    value=${value//\"/\\\"}
    printf '"%s"' "$value"
}

"$KC" config credentials --server http://127.0.0.1:8080/auth --realm master \
    --user "$KC_BOOTSTRAP_ADMIN_USERNAME" --password "$KC_BOOTSTRAP_ADMIN_PASSWORD" >/dev/null

client_uuid() {
    "$KC" get clients -r "$REALM" -q clientId="$1" --fields id --format csv --noquotes
}

# The Keycloak image has bash but no awk or sed: CSV lines are split with read.
mapper_uuid() {
    local id name
    while IFS=, read -r id name; do
        if [ "$name" = "$2" ]; then
            printf '%s\n' "$id"
            return
        fi
    done <<< "$("$KC" get "clients/$1/protocol-mappers/models" -r "$REALM" --fields id,name --format csv --noquotes)"
}

# Realm: transport security and mail.
ssl=none
if [ "$SCHEME" = "https" ]; then ssl=external; fi
realm_settings=(-s "sslRequired=$(json_string "$ssl")")
if [ -n "${LORE_SMTP_HOST:-}" ]; then
    smtp_auth=false
    if [ -n "${LORE_SMTP_USERNAME:-}${LORE_SMTP_PASSWORD:-}" ]; then smtp_auth=true; fi
    smtp_from=${LORE_SMTP_FROM:-}
    if [[ $smtp_from =~ \<([^>]+)\> ]]; then smtp_from=${BASH_REMATCH[1]}; fi
    realm_settings+=(
        -s "smtpServer.host=$(json_string "$LORE_SMTP_HOST")"
        -s "smtpServer.port=$(json_string "${LORE_SMTP_PORT:-587}")"
        -s "smtpServer.from=$(json_string "$smtp_from")"
        -s "smtpServer.fromDisplayName=$(json_string "${LORE_SMTP_FROM_NAME:-Lore}")"
        -s "smtpServer.starttls=$(json_string "${LORE_SMTP_STARTTLS:-true}")"
        -s "smtpServer.ssl=$(json_string "${LORE_SMTP_SSL:-false}")"
        -s "smtpServer.auth=$(json_string "$smtp_auth")"
        -s "smtpServer.user=$(json_string "${LORE_SMTP_USERNAME:-}")"
        -s "smtpServer.password=$(json_string "${LORE_SMTP_PASSWORD:-}")"
        -s verifyEmail=true -s resetPasswordAllowed=true
    )
else
    realm_settings+=(-s 'smtpServer={}' -s verifyEmail=false -s resetPasswordAllowed=false)
fi
"$KC" update "realms/$REALM" "${realm_settings[@]}"
echo "Realm: sslRequired=$ssl, mail $([ -n "${LORE_SMTP_HOST:-}" ] && echo "via $LORE_SMTP_HOST" || echo "disabled")"

# WebUI client: only the current public URL may receive sign-ins and logouts.
web=$(client_uuid lore-web)
"$KC" update "clients/$web" -r "$REALM" \
    -s "redirectUris=[$(json_string "$WEB_URL/*")]" \
    -s "webOrigins=[$(json_string "$WEB_URL")]" \
    -s "attributes.\"post.logout.redirect.uris\"=$(json_string "$WEB_URL/")"
echo "Client lore-web: $WEB_URL"

# Native client: the domains the exchanged tokens may be used for.
native=$(client_uuid lore-native)
domains_mapper=$(mapper_uuid "$native" "Control plane exchange domain")
if [ -n "$domains_mapper" ]; then
    "$KC" update "clients/$native/protocol-mappers/models/$domains_mapper" -r "$REALM" \
        -s "config.\"claim.value\"=$(json_string "[\"localhost\",\"$PUBLIC_HOST\"]")"
    echo "Client lore-native: root domains localhost, $PUBLIC_HOST"
fi

# Both clients put the Active Directory objectGUID (ldap_id) and the account's groups into
# the token. The mappers sit on the clients, not in client scopes: a realm imported with its
# own scope list lacks Keycloak's built-in scopes and refuses sign-in with invalid_scope.
ensure_mapper() {
    local client=$1 name=$2 json=$3
    if [ -z "$(mapper_uuid "$client" "$name")" ]; then
        "$KC" create "clients/$client/protocol-mappers/models" -r "$REALM" -f - <<< "$json"
        echo "Added mapper \"$name\" to client $client"
    fi
}
for client in "$web" "$native"; do
    ensure_mapper "$client" "Directory account ID" '{"name":"Directory account ID","protocol":"openid-connect","protocolMapper":"oidc-usermodel-attribute-mapper","config":{"user.attribute":"LDAP_ID","claim.name":"ldap_id","jsonType.label":"String","access.token.claim":"true","id.token.claim":"true","userinfo.token.claim":"false"}}'
    ensure_mapper "$client" "Groups" '{"name":"Groups","protocol":"openid-connect","protocolMapper":"oidc-group-membership-mapper","config":{"claim.name":"groups","full.path":"false","access.token.claim":"true","id.token.claim":"true","userinfo.token.claim":"false"}}'
done

# Service client: the secret in .env is the source of truth.
service=$(client_uuid "$SERVICE_CLIENT")
"$KC" update "clients/$service" -r "$REALM" -s "secret=$(json_string "${LORE_SERVICE_CLIENT_SECRET:?LORE_SERVICE_CLIENT_SECRET is required}")"
for role in manage-realm manage-users; do
    "$KC" add-roles -r "$REALM" --uusername "service-account-$SERVICE_CLIENT" --cclientid realm-management --rolename "$role"
done
echo "Client $SERVICE_CLIENT: secret and roles applied"

# Local administrator. Keycloak looks up its own users before the directory, so a local
# "admin" always wins over an Active Directory account with the same name. If the name
# currently belongs to an account imported from AD (or the local one was deleted), the
# local account is (re)created with LORE_ADMIN_PASSWORD. An existing local admin is left
# alone: its password may have been changed since setup. While the federation is enabled
# Keycloak refuses to create a user whose name exists in AD, so it's paused for the swap.
ADMIN_NAME=admin  # BOOTSTRAP_ADMIN of the control plane in compose.yaml
admin_id="" admin_link=""
# exact=true matches the stored username. The answer can't be compared with the name: with
# "email as username" on, Keycloak shows the email in place of the username.
IFS=, read -r admin_id admin_link <<< "$("$KC" get users -r "$REALM" -q "username=$ADMIN_NAME" -q exact=true --fields id,federationLink --format csv --noquotes)" || true
if [ -z "$admin_id" ] || [ -n "$admin_link" ]; then
    # Two realm settings get in the way of creating the account and are paused for a few
    # seconds: the federation (Keycloak consults AD for a new name and refuses one that
    # exists there) and "email as username" (the admin API would name the account after its
    # email instead of "admin").
    paused=()
    while IFS=, read -r component provider; do
        if [ "$provider" = "ldap" ]; then paused+=("$component"); fi
    done <<< "$("$KC" get components -r "$REALM" -q type=org.keycloak.storage.UserStorageProvider --fields id,providerId --format csv --noquotes)"
    email_as_username=$("$KC" get "realms/$REALM" --fields registrationEmailAsUsername --format csv --noquotes)
    resume() {
        local component
        for component in "${paused[@]}"; do
            "$KC" update "components/$component" -r "$REALM" -s 'config.enabled=["true"]'
        done
        paused=()
        if [ "$email_as_username" = "true" ]; then
            "$KC" update "realms/$REALM" -s registrationEmailAsUsername=true
            email_as_username=false
        fi
    }
    trap resume EXIT
    for component in "${paused[@]}"; do
        "$KC" update "components/$component" -r "$REALM" -s 'config.enabled=["false"]'
    done
    if [ "$email_as_username" = "true" ]; then
        "$KC" update "realms/$REALM" -s registrationEmailAsUsername=false
    fi
    if [ -n "$admin_id" ]; then
        # Deletes only Keycloak's copy; the account in AD is untouched.
        "$KC" delete "users/$admin_id" -r "$REALM"
    fi
    admin_id=$("$KC" create users -r "$REALM" -i -s "username=$(json_string "$ADMIN_NAME")" -s enabled=true -s emailVerified=true \
        -s firstName='"Lore"' -s lastName='"Administrator"')
    # The password goes through stdin, not the command line.
    "$KC" update "users/$admin_id/reset-password" -r "$REALM" -n -f - <<< \
        "{\"type\":\"password\",\"temporary\":false,\"value\":$(json_string "${LORE_ADMIN_PASSWORD:?LORE_ADMIN_PASSWORD is required}")}"
    resume
    trap - EXIT
    if [ -n "$admin_link" ]; then
        echo "Local administrator $ADMIN_NAME created; the Active Directory account with that name can no longer sign in as $ADMIN_NAME"
    else
        echo "Local administrator $ADMIN_NAME created"
    fi
else
    echo "Local administrator $ADMIN_NAME: present"
fi

# Active Directory federation.
ldap=""
while IFS=, read -r component provider; do
    if [ "$provider" = "ldap" ]; then
        ldap=$component
        break
    fi
done <<< "$("$KC" get components -r "$REALM" -q type=org.keycloak.storage.UserStorageProvider --fields id,providerId --format csv --noquotes)"
if [ -n "${LORE_LDAP_URL:-}" ]; then
    truststore=always
    if [ "${LORE_LDAP_INSECURE_SKIP_VERIFY:-false}" = "true" ]; then truststore=never; fi
    ldap_settings=(
        -s "config.connectionUrl=[$(json_string "$LORE_LDAP_URL")]"
        -s "config.usersDn=[$(json_string "${LORE_LDAP_BASE_DN:?LORE_LDAP_BASE_DN is required}")]"
        -s "config.bindDn=[$(json_string "${LORE_LDAP_BIND_DN:?LORE_LDAP_BIND_DN is required}")]"
        -s "config.bindCredential=[$(json_string "${LORE_LDAP_BIND_PASSWORD:?LORE_LDAP_BIND_PASSWORD is required}")]"
        -s "config.startTls=[$(json_string "${LORE_LDAP_STARTTLS:-false}")]"
        -s "config.useTruststoreSpi=[$(json_string "$truststore")]"
        -s 'config.pagination=["true"]'
        # Keycloak keeps an imported account's enabled flag and reads it from AD only when
        # it synchronizes: without a periodic sync, someone re-enabled in AD can't sign in
        # until an administrator triggers one. Disabling needs no sync (AD refuses the bind).
        -s "config.changedSyncPeriod=[$(json_string "${LORE_LDAP_CHANGED_SYNC_SECONDS:-300}")]"
    )
    if [ -n "$ldap" ]; then
        "$KC" update "components/$ldap" -r "$REALM" "${ldap_settings[@]}"
        echo "LDAP federation updated: $LORE_LDAP_URL"
    else
        realm_id=$("$KC" get "realms/$REALM" --fields id --format csv --noquotes)
        "$KC" create components -r "$REALM" \
            -s name=active-directory -s providerId=ldap -s providerType=org.keycloak.storage.UserStorageProvider \
            -s "parentId=$(json_string "$realm_id")" \
            -s 'config.enabled=["true"]' -s 'config.priority=["0"]' -s 'config.editMode=["READ_ONLY"]' \
            -s 'config.syncRegistrations=["false"]' -s 'config.trustEmail=["true"]' -s 'config.vendor=["ad"]' \
            -s 'config.usernameLDAPAttribute=["sAMAccountName"]' -s 'config.rdnLDAPAttribute=["cn"]' \
            -s 'config.uuidLDAPAttribute=["objectGUID"]' -s 'config.userObjectClasses=["person, organizationalPerson, user"]' \
            -s 'config.authType=["simple"]' -s 'config.connectionTimeout=["60000"]' -s 'config.readTimeout=["60000"]' \
            "${ldap_settings[@]}"
        echo "LDAP federation created: $LORE_LDAP_URL"
    fi
elif [ -n "$ldap" ]; then
    echo "LDAP federation exists in Keycloak but LORE_LDAP_URL is empty; left unchanged. Remove it in the Keycloak admin console if it's no longer used."
fi
