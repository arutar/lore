#!/usr/bin/env sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
BACKUP=${1:-}
[ -n "$BACKUP" ] || { echo "Usage: restore.sh BACKUP [--yes]" >&2; exit 2; }
for file in lore.dump keycloak.dump storage.tar manifest.json; do
    [ -f "$BACKUP/$file" ] || { echo "Backup is incomplete: $file" >&2; exit 1; }
done
if [ "${2:-}" != "--yes" ]; then
    printf 'Restore %s over the current databases and storage? [y/N] ' "$BACKUP"
    read answer
    [ "$answer" = "y" ] || exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then (cd "$BACKUP" && sha256sum -c SHA256SUMS); else (cd "$BACKUP" && shasum -a 256 -c SHA256SUMS); fi
DATA_ROOT=${LORE_DATA_ROOT:-./data}
STAGING=$DATA_ROOT/.restore-new-$$
PREVIOUS=$DATA_ROOT/.restore-old-$$
STOPPED=false
cleanup() {
    result=$?
    rm -rf "$STAGING"
    if [ "$result" -ne 0 ]; then
        for directory in lore control-plane; do
            if [ -d "$PREVIOUS/$directory" ] && [ ! -e "$DATA_ROOT/$directory" ]; then mv "$PREVIOUS/$directory" "$DATA_ROOT/$directory"; fi
        done
    fi
    if [ "$STOPPED" = true ]; then docker compose up -d --no-recreate --wait --wait-timeout 120 keycloak control-plane lore caddy; fi
}
trap cleanup EXIT HUP INT TERM
STOPPED=true
docker compose stop caddy lore control-plane keycloak
for database in lore keycloak; do
    docker compose exec -T postgres pg_restore -U lore --clean --if-exists --dbname="$database" < "$BACKUP/$database.dump"
done
mkdir -p "$STAGING" "$PREVIOUS"
tar -C "$STAGING" -xf "$BACKUP/storage.tar"
for directory in lore control-plane; do
    [ -d "$STAGING/$directory" ] || { echo "Backup storage is missing $directory" >&2; exit 1; }
    if [ -d "$DATA_ROOT/$directory" ]; then mv "$DATA_ROOT/$directory" "$PREVIOUS/$directory"; fi
done
for directory in lore control-plane; do mv "$STAGING/$directory" "$DATA_ROOT/$directory"; done
rm -rf "$PREVIOUS"
printf 'Restore completed: %s\n' "$BACKUP"
