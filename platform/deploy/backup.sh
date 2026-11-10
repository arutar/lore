#!/usr/bin/env sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
DATA_ROOT=${LORE_DATA_ROOT:-./data}
DESTINATION=${1:-./backups}
NAME=${2:-$(date -u +%Y%m%d-%H%M%S)}
case "$NAME" in *[!a-zA-Z0-9._-]*) echo "Invalid backup name" >&2; exit 1 ;; esac
BACKUP=$DESTINATION/$NAME
[ ! -e "$BACKUP" ] || { echo "Backup already exists: $BACKUP" >&2; exit 1; }
mkdir -p "$BACKUP"
STOPPED=false
restore_services() {
    if [ "$STOPPED" = true ]; then docker compose up -d --no-recreate --wait --wait-timeout 120 keycloak control-plane lore caddy; fi
}
trap restore_services EXIT HUP INT TERM
STOPPED=true
docker compose stop caddy lore control-plane keycloak
for database in lore keycloak; do
    docker compose exec -T postgres pg_dump -U lore --format=custom "$database" > "$BACKUP/$database.dump"
done
tar -C "$DATA_ROOT" -cf "$BACKUP/storage.tar" lore control-plane
printf '{"createdAt":"%s","databases":["lore","keycloak"],"storage":["lore","control-plane"]}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$BACKUP/manifest.json"
if command -v sha256sum >/dev/null 2>&1; then
    (cd "$BACKUP" && sha256sum lore.dump keycloak.dump storage.tar manifest.json > SHA256SUMS)
else
    (cd "$BACKUP" && shasum -a 256 lore.dump keycloak.dump storage.tar manifest.json > SHA256SUMS)
fi
printf 'Backup created: %s\n' "$BACKUP"
