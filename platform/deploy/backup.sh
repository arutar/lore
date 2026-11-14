#!/usr/bin/env sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
# The data root comes from the environment or, as setup stores it, from .env. Compose reads
# .env on its own; this script has to do the same or it archives the wrong directory.
if [ -z "${LORE_DATA_ROOT:-}" ] && [ -f .env ]; then
    LORE_DATA_ROOT=$(sed -n 's/^LORE_DATA_ROOT=//p' .env | tail -n 1)
fi
DATA_ROOT=${LORE_DATA_ROOT:-./data}
DESTINATION=${1:-./backups}
NAME=${2:-$(date -u +%Y%m%d-%H%M%S)}
case "$NAME" in *[!a-zA-Z0-9._-]*) echo "Invalid backup name" >&2; exit 1 ;; esac
FINAL=$DESTINATION/$NAME
[ ! -e "$FINAL" ] || { echo "Backup already exists: $FINAL" >&2; exit 1; }
# Refuse to produce an archive that can't restore the server: an empty or wrong data root
# used to yield a "successful" backup without the content store and the signing key.
for directory in lore control-plane; do
    [ -d "$DATA_ROOT/$directory" ] || { echo "Data directory is missing: $DATA_ROOT/$directory. Check LORE_DATA_ROOT." >&2; exit 1; }
done
[ -n "$(ls -A "$DATA_ROOT/lore" 2>/dev/null)" ] || { echo "Lore storage is empty: $DATA_ROOT/lore. Check LORE_DATA_ROOT." >&2; exit 1; }
[ -f "$DATA_ROOT/control-plane/token-exchange-signing.pem" ] || [ "${LORE_BACKUP_WITHOUT_SIGNING_KEY:-false}" = "true" ] || {
    echo "Signing key is missing: $DATA_ROOT/control-plane/token-exchange-signing.pem. Set LORE_BACKUP_WITHOUT_SIGNING_KEY=true if the key is stored elsewhere." >&2; exit 1; }
# Work in a temporary directory so an interrupted run never leaves something that looks like a backup.
BACKUP=$FINAL.partial
rm -rf "$BACKUP"
# The backup holds the secrets from .env and the token signing key: owner access only.
mkdir -p "$DESTINATION"
(umask 077 && mkdir "$BACKUP")
STOPPED=false
restore_services() {
    result=$?
    if [ "$result" -ne 0 ]; then rm -rf "$BACKUP"; fi
    if [ "$STOPPED" = true ]; then docker compose up -d --no-recreate --wait --wait-timeout 120 keycloak control-plane lore caddy; fi
    exit "$result"
}
trap restore_services EXIT
trap 'exit 130' HUP INT TERM
STOPPED=true
docker compose stop caddy lore control-plane keycloak
for database in lore keycloak; do
    docker compose exec -T postgres pg_dump -U lore --format=custom "$database" > "$BACKUP/$database.dump"
done
# The files belong to the services' users (the signing key is readable by its owner only), so
# the archive is made in a container as root, which also keeps their owners for the restore.
DATA_ABSOLUTE=$(cd "$DATA_ROOT" && pwd)
docker compose run --rm --no-deps -T -v "$DATA_ABSOLUTE:/lore-data:ro" --entrypoint tar postgres \
    -C /lore-data -cf - lore control-plane > "$BACKUP/storage.tar"
# The Keycloak database stores the service client secret and the database password must
# match the restored cluster, so a restore on a new host needs these exact values.
[ -f .env ] || { echo ".env is missing; run setup first" >&2; exit 1; }
(umask 077 && cp .env "$BACKUP/env")
printf '{"createdAt":"%s","databases":["lore","keycloak"],"storage":["lore","control-plane"],"settings":"env"}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$BACKUP/manifest.json"
if command -v sha256sum >/dev/null 2>&1; then
    (cd "$BACKUP" && sha256sum lore.dump keycloak.dump storage.tar env manifest.json > SHA256SUMS)
else
    (cd "$BACKUP" && shasum -a 256 lore.dump keycloak.dump storage.tar env manifest.json > SHA256SUMS)
fi
mv "$BACKUP" "$FINAL"
printf 'Backup created: %s (data root: %s)\n' "$FINAL" "$DATA_ROOT"
