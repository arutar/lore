#!/usr/bin/env sh
# Restores a backup made by backup.sh (or backup.ps1) over the current installation.
# Everything that can fail without touching data runs first; once the databases are being
# replaced, any failure puts the previous databases and storage back.
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
BACKUP=${1:-}
[ -n "$BACKUP" ] || { echo "Usage: restore.sh BACKUP [--yes]" >&2; exit 2; }
for file in lore.dump keycloak.dump storage.tar manifest.json SHA256SUMS; do
    [ -f "$BACKUP/$file" ] || { echo "Backup is incomplete: $file" >&2; exit 1; }
done
if command -v sha256sum >/dev/null 2>&1; then (cd "$BACKUP" && sha256sum -c --quiet SHA256SUMS); else (cd "$BACKUP" && shasum -a 256 -c --quiet SHA256SUMS); fi
[ -f .env ] || { echo ".env is missing; run setup on this host first" >&2; exit 1; }
if [ "${2:-}" != "--yes" ]; then
    printf 'Restore %s over the current databases and storage? [y/N] ' "$BACKUP"
    read answer
    [ "$answer" = "y" ] || exit 1
fi

# The data root comes from the environment or, as setup stores it, from .env.
if [ -z "${LORE_DATA_ROOT:-}" ]; then
    LORE_DATA_ROOT=$(sed -n 's/^LORE_DATA_ROOT=//p' .env | tail -n 1)
fi
DATA_ROOT=${LORE_DATA_ROOT:-./data}
STAGING_NAME=.restore-new-$$
PREVIOUS_NAME=.restore-old-$$
STAGING=$DATA_ROOT/$STAGING_NAME
PREVIOUS=$DATA_ROOT/$PREVIOUS_NAME
STOPPED=false
DATABASES_TOUCHED=false
ENV_SAVED=false

# The storage belongs to the services' users, so unpacking, moving and removing it happen in a
# container as root (in the data root, mounted at /lore-data); the database dumps are written
# from here, into PREVIOUS, which this user creates.
DATA_ABSOLUTE=$(cd "$DATA_ROOT" && pwd)
BACKUP_ABSOLUTE=$(cd "$BACKUP" && pwd)
as_root() {
    docker compose run --rm --no-deps -T -v "$DATA_ABSOLUTE:/lore-data" -v "$BACKUP_ABSOLUTE:/lore-backup:ro" \
        --entrypoint sh postgres -c "cd /lore-data && $1"
}

# Step 1: unpack and check the storage before anything is stopped or replaced.
mkdir -p "$PREVIOUS"
cleanup() {
    result=$?
    trap - EXIT
    if [ "$result" -ne 0 ]; then
        echo "Restore failed; putting the previous installation back" >&2
        if [ "$DATABASES_TOUCHED" = true ]; then
            for database in lore keycloak; do
                if [ -f "$PREVIOUS/$database.dump" ]; then
                    docker compose exec -T postgres pg_restore -U lore --clean --if-exists --single-transaction --dbname="$database" < "$PREVIOUS/$database.dump" ||
                        echo "Couldn't roll back database $database; its pre-restore dump is kept in $PREVIOUS" >&2
                fi
            done
        fi
        as_root "for d in lore control-plane; do if [ -d $PREVIOUS_NAME/\$d ]; then rm -rf \$d && mv $PREVIOUS_NAME/\$d \$d; fi; done" ||
            echo "Couldn't put the previous storage back; it is kept in $PREVIOUS" >&2
        if [ "$ENV_SAVED" = true ]; then cp "$PREVIOUS/env" .env; fi
    fi
    as_root "rm -rf $STAGING_NAME" || true
    if [ "$result" -eq 0 ] || [ "$DATABASES_TOUCHED" = false ]; then
        as_root "rm -rf $PREVIOUS_NAME" || true
    else
        echo "Dumps of the databases as they were before the restore are kept in $PREVIOUS" >&2
    fi
    # Recreate rather than just start: restored secrets in .env must reach the containers.
    if [ "$STOPPED" = true ]; then docker compose up -d --wait --wait-timeout 180 keycloak control-plane lore caddy || true; fi
    exit "$result"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
as_root "mkdir -p $STAGING_NAME && tar -C $STAGING_NAME -xf /lore-backup/storage.tar"
for directory in lore control-plane; do
    [ -d "$STAGING/$directory" ] || { echo "Backup storage is missing $directory" >&2; exit 1; }
done

# Step 2: stop the services and keep a dump of the current databases for a rollback.
STOPPED=true
docker compose stop caddy lore control-plane keycloak
for database in lore keycloak; do
    docker compose exec -T postgres pg_dump -U lore --format=custom "$database" > "$PREVIOUS/$database.dump"
done

# Step 3: databases, each in one transaction that stops at the first error.
DATABASES_TOUCHED=true
for database in lore keycloak; do
    docker compose exec -T postgres pg_restore -U lore --clean --if-exists --single-transaction --exit-on-error --dbname="$database" < "$BACKUP/$database.dump"
done

# Step 4: storage.
as_root "for d in lore control-plane; do if [ -d \$d ]; then mv \$d $PREVIOUS_NAME/\$d; fi; mv $STAGING_NAME/\$d \$d; done"

# Step 5: secrets that live in the restored Keycloak database. Host settings (URLs, data
# root, the PostgreSQL password of this cluster) stay as they are.
if [ -f "$BACKUP/env" ]; then
    cp .env "$PREVIOUS/env"
    ENV_SAVED=true
    for key in KEYCLOAK_ADMIN_PASSWORD LORE_SERVICE_CLIENT_SECRET; do
        value=$(sed -n "s/^$key=//p" "$BACKUP/env" | tail -n 1)
        [ -n "$value" ] || continue
        grep -v "^$key=" .env > .env.restore-$$ || true
        printf '%s=%s\n' "$key" "$value" >> .env.restore-$$
        chmod 600 .env.restore-$$
        mv .env.restore-$$ .env
    done
else
    echo "This backup has no settings file (made before it was added). If Keycloak sign-in fails after the restore, copy KEYCLOAK_ADMIN_PASSWORD and LORE_SERVICE_CLIENT_SECRET from the original host's .env." >&2
fi
printf 'Restore completed: %s\n' "$BACKUP"
