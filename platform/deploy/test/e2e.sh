#!/usr/bin/env bash
# End-to-end check of an installation, the way an administrator uses it:
#   setup -> administrator signs in through Keycloak -> creates a repository and a local
#   user -> the user signs in (changing the temporary password) -> the owner grants, changes
#   and revokes access on the WebUI's Access tab in a headless browser -> the user sees the
#   repository only after a grant -> backup -> changes after the backup -> restore -> the
#   state at backup time is back, including the Lore storage and both databases.
#
# It drives the real WebUI sign-in (Keycloak login form, OIDC callback, session cookie) with
# curl. The Access tab step also drives a headless browser when Node 22+ and Chrome,
# Chromium or Edge are installed. Needs bash, curl, docker and the deploy directory.
#
# DESTRUCTIVE: it runs setup, backup and RESTORE in the deploy directory, which replaces the
# data. Use it only on a test installation, and confirm with LORE_E2E=destroy-test-data.
#
#   LORE_E2E=destroy-test-data bash platform/deploy/test/e2e.sh
#
# Optional: LORE_E2E_SKIP_SETUP=true reuses a running installation; LORE_E2E_POWERSHELL=true
# runs setup.ps1, backup.ps1 and restore.ps1 instead of the shell scripts (Windows hosts).
set -euo pipefail

[ "${LORE_E2E:-}" = "destroy-test-data" ] || {
    echo "This test runs restore and replaces the installation's data. Set LORE_E2E=destroy-test-data to confirm." >&2
    exit 2
}
DEPLOY=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$DEPLOY"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
STEP=0
if [ "${LORE_E2E_POWERSHELL:-false}" = "true" ]; then
    PWSH=$(command -v pwsh || command -v powershell) || { echo "PowerShell isn't installed" >&2; exit 2; }
    run_setup() { "$PWSH" -NoProfile -ExecutionPolicy Bypass -File setup.ps1; }
    run_backup() { "$PWSH" -NoProfile -ExecutionPolicy Bypass -File backup.ps1 -Destination "$1" -Name "$2"; }
    run_restore() { "$PWSH" -NoProfile -ExecutionPolicy Bypass -File restore.ps1 -Backup "$1"; }
else
    run_setup() { sh setup.sh; }
    run_backup() { sh backup.sh "$1" "$2"; }
    run_restore() { sh restore.sh "$1" --yes; }
fi
step() { STEP=$((STEP + 1)); printf '\n[%02d] %s\n' "$STEP" "$*"; }
ok() { printf '     ok: %s\n' "$*"; }
fail() { printf '     FAIL: %s\n' "$*" >&2; exit 1; }

env_value() { sed -n "s/^$1=//p" .env | tail -n 1 | tr -d '\r'; }
# json_field NAME reads the first "NAME":"value" pair; enough for the flat answers used here.
json_field() { grep -o "\"$1\":\"[^\"]*\"" | head -n 1 | cut -d'"' -f4; }

# --- setup -----------------------------------------------------------------------------
if [ "${LORE_E2E_SKIP_SETUP:-false}" != "true" ]; then
    step "setup"
    started=$(date +%s)
    run_setup > "$WORK/setup.log" 2>&1 || { tail -n 40 "$WORK/setup.log" >&2; fail "setup exited with an error"; }
    ok "finished in $(( $(date +%s) - started )) s"
    grep -q "Local administrator admin" "$WORK/setup.log" || fail "setup didn't check the local administrator"
    ok "local administrator checked: $(grep -o 'Local administrator admin[^;]*' "$WORK/setup.log" | head -n 1)"
fi
WEB=$(env_value LORE_PUBLIC_URL)
ADMIN_PASSWORD=$(env_value LORE_ADMIN_PASSWORD)
[ -n "$WEB" ] && [ -n "$ADMIN_PASSWORD" ] || fail ".env lacks LORE_PUBLIC_URL or LORE_ADMIN_PASSWORD"
curl -fsS "$WEB/healthz" > /dev/null || fail "$WEB/healthz isn't reachable from here"

# --- helpers ---------------------------------------------------------------------------
# form_action FILE prints the action of the first form in a Keycloak page.
form_action() { grep -o 'action="[^"]*"' "$1" | head -n 1 | sed -e 's/^action="//' -e 's/"$//' -e 's/&amp;/\&/g'; }

# login NAME PASSWORD [NEW_PASSWORD]: signs in through the WebUI like a browser, keeping the
# cookies in $WORK/NAME.jar. NEW_PASSWORD answers Keycloak's "update password" form.
login() {
    local name=$1 password=$2 new=${3:-} jar="$WORK/$1.jar" page="$WORK/$1.html" action
    rm -f "$jar"
    curl -sS -L -c "$jar" -b "$jar" -o "$page" "$WEB/web/v1/login"
    action=$(form_action "$page")
    [ -n "$action" ] || fail "no Keycloak login form for $name"
    curl -sS -L -c "$jar" -b "$jar" -o "$page" --data-urlencode "username=$name" --data-urlencode "password=$password" \
        --data-urlencode credentialId= "$action"
    if grep -q 'name="password-new"' "$page"; then
        [ -n "$new" ] || fail "Keycloak asks $name for a new password"
        action=$(form_action "$page")
        curl -sS -L -c "$jar" -b "$jar" -o "$page" --data-urlencode "password-new=$new" --data-urlencode "password-confirm=$new" "$action"
    fi
    if ! session "$name" | grep -q '"authenticated":true'; then
        grep -o '<span[^>]*kc-feedback-text[^>]*>[^<]*' "$page" | sed 's/.*>//' >&2 || true
        fail "$name isn't signed in after the Keycloak form"
    fi
}
session() { curl -sS -b "$WORK/$1.jar" "$WEB/web/v1/session"; }
csrf() { session "$1" | json_field csrfToken; }
# api NAME METHOD PATH [JSON] prints the body and fails on an unexpected status.
api() {
    local name=$1 method=$2 path=$3 body=${4:-} expect=${5:-2} status
    local args=(-sS -o "$WORK/body" -w '%{http_code}' -b "$WORK/$name.jar" -X "$method")
    if [ "$method" != GET ]; then args+=(-H "X-CSRF-Token: $(csrf "$name")"); fi
    if [ -n "$body" ]; then args+=(-H 'Content-Type: application/json' --data "$body"); fi
    status=$(curl "${args[@]}" "$WEB/web/v1$path")
    case "$status" in
        "$expect"*) cat "$WORK/body" ;;
        *) printf '%s %s -> %s: %s\n' "$method" "$path" "$status" "$(head -c 300 "$WORK/body")" >&2; return 1 ;;
    esac
}
sees() { api "$1" GET "/catalog?q=$2" | grep -q "\"name\":\"$2\""; }

RUN=$(date -u +%H%M%S)
REPO="e2e-$RUN/assets"
AFTER="e2e-$RUN/after-backup"
USER_EMAIL="e2e-$RUN@example.invalid"
TEMPORARY="Temp-$RUN-password"
PERMANENT="Kept-$RUN-password"

# --- sign-in and administration ----------------------------------------------------------
step "administrator signs in through Keycloak"
login admin "$ADMIN_PASSWORD"
session admin | grep -q '"role":"admin"' || fail "admin has no administrator role: $(session admin | head -c 300)"
session admin | grep -q '"source":"local"' || fail "admin isn't the local account: $(session admin | head -c 300)"
ok "local account with the administrator role"

step "administrator creates a private repository and a local user"
REPO_ID=$(api admin POST /admin/repositories "{\"name\":\"$REPO\",\"description\":\"end-to-end test\",\"public\":false}" | json_field id)
[ -n "$REPO_ID" ] || fail "repository wasn't created"
api admin GET "/repositories/$REPO_ID/tree" > /dev/null || fail "Lore server doesn't serve the new repository"
ok "repository $REPO ($REPO_ID) exists in the control plane and on the Lore server"
USER_ID=$(api admin POST /admin/users "{\"email\":\"$USER_EMAIL\",\"displayName\":\"E2E $RUN\",\"password\":\"$TEMPORARY\",\"role\":\"user\"}" | json_field id)
[ -n "$USER_ID" ] || fail "local user wasn't created"
ok "local user $USER_EMAIL ($USER_ID)"

step "the user signs in, replaces the temporary password and doesn't see the private repository"
login "$USER_EMAIL" "$TEMPORARY" "$PERMANENT"
sees "$USER_EMAIL" "$REPO" && fail "the user sees a private repository without a grant"
api "$USER_EMAIL" GET "/repositories/$REPO_ID" "" 404 > /dev/null || fail "the private repository's page isn't hidden from the user"
ok "private repository is hidden"

step "the owner grants, changes and revokes access on the WebUI's Access tab"
# A real browser: the API calls below can't catch a WebUI that sends requests the server
# refuses. Needs Node 22+ and Chrome, Chromium or Edge (LORE_E2E_BROWSER); without them the
# step is skipped unless LORE_E2E_REQUIRE_BROWSER=true.
NODE=$(command -v node || true)
if [ -n "$NODE" ] && "$NODE" -e 'process.exit(typeof WebSocket === "function" ? 0 : 1)' 2>/dev/null; then
    LORE_URL="$WEB" LORE_ADMIN_USER=admin LORE_ADMIN_PASSWORD="$ADMIN_PASSWORD" REPO_ID="$REPO_ID" SUBJECT_ID="$USER_ID" \
        SUBJECT_EMAIL="$USER_EMAIL" "$NODE" "$DEPLOY/test/webui-grants.mjs" || fail "the Access tab check failed"
    sees "$USER_EMAIL" "$REPO" && fail "the repository is still visible after the WebUI revoked the grant"
    ok "the user doesn't see the repository after the WebUI revoked the grant"
elif [ "${LORE_E2E_REQUIRE_BROWSER:-false}" = "true" ]; then
    fail "Node 22 or later isn't installed"
else
    ok "skipped: Node 22 or later isn't installed"
fi

step "a read grant shows it to the user, who still can't manage it"
api admin PUT "/repositories/$REPO_ID/grants/" "{\"subjectType\":\"user\",\"subject\":\"$USER_ID\",\"permission\":\"read\"}" > /dev/null
sees "$USER_EMAIL" "$REPO" || fail "the grant didn't reach the user's catalog"
api "$USER_EMAIL" GET "/repositories/$REPO_ID/grants/" "" 403 > /dev/null || fail "a reader can list the grants"
ok "visible after the grant; grants stay owner-only"

# --- backup and restore ------------------------------------------------------------------
step "backup"
BACKUP_NAME="e2e-$RUN"
run_backup ./backups "$BACKUP_NAME" > "$WORK/backup.log" 2>&1 || { tail -n 30 "$WORK/backup.log" >&2; fail "backup exited with an error"; }
[ -f "backups/$BACKUP_NAME/SHA256SUMS" ] || fail "backup has no checksums"
ok "backups/$BACKUP_NAME: $(ls "backups/$BACKUP_NAME" | tr '\n' ' ')"
# backup stops the services: sign in again.
login admin "$ADMIN_PASSWORD"
login "$USER_EMAIL" "$PERMANENT"

step "changes after the backup: a new repository, the grant revoked"
AFTER_ID=$(api admin POST /admin/repositories "{\"name\":\"$AFTER\",\"description\":\"\",\"public\":false}" | json_field id)
api admin DELETE "/repositories/$REPO_ID/grants/" "{\"subjectType\":\"user\",\"subject\":\"$USER_ID\",\"permission\":\"read\"}" > /dev/null
sees "$USER_EMAIL" "$REPO" && fail "revoking the grant didn't hide the repository"
ok "repository $AFTER created ($AFTER_ID), grant revoked"

step "restore"
run_restore "backups/$BACKUP_NAME" > "$WORK/restore.log" 2>&1 || { tail -n 40 "$WORK/restore.log" >&2; fail "restore exited with an error"; }
ok "restore finished"

step "the state at backup time is back"
login admin "$ADMIN_PASSWORD"
api admin GET "/admin/repositories?q=$AFTER" | grep -q "\"name\":\"$AFTER\"" && fail "a repository created after the backup survived the restore"
api admin GET "/repositories/$REPO_ID/tree" > /dev/null || fail "the Lore server lost the restored repository"
ok "repository created after the backup is gone; $REPO is served by Lore"
login "$USER_EMAIL" "$PERMANENT"
sees "$USER_EMAIL" "$REPO" || fail "the grant from backup time wasn't restored"
ok "the user signs in with the password kept in Keycloak's database and sees the repository again"

printf '\nEnd-to-end test passed (%d steps).\n' "$STEP"
