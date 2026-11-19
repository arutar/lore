#!/usr/bin/env bash
# Packs the deployment directory into a self-contained server bundle for a release:
# compose.yaml points at the published images of this version instead of building them, so a
# host needs only Docker and the bundle, not the source tree or a Rust toolchain.
#
#   bash platform/deploy/make-bundle.sh VERSION IMAGE_PREFIX OUTPUT_DIR
#   bash platform/deploy/make-bundle.sh 0.1.12 ghcr.io/arutar ./dist
set -euo pipefail

VERSION=${1:?version, for example 0.1.12}
PREFIX=${2:?image prefix, for example ghcr.io/arutar}
OUTPUT=${3:?output directory}
DEPLOY=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
NAME="lore-platform-server-$VERSION"
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
mkdir -p "$STAGE/$NAME" "$OUTPUT"
OUTPUT=$(cd "$OUTPUT" && pwd)

# Everything an installation runs, nothing it produces (.env, data, backups).
(cd "$DEPLOY" && tar -cf - --exclude=./.env --exclude=./data --exclude=./backups --exclude=./make-bundle.sh .) |
    (cd "$STAGE/$NAME" && tar -xf -)

compose="$STAGE/$NAME/compose.yaml"
# Published images by default; LORE_CONTROL_PLANE_IMAGE and LORE_SERVER_IMAGE still override.
sed -i \
    -e "s|\${LORE_CONTROL_PLANE_IMAGE:-lore-platform-control-plane:local}|\${LORE_CONTROL_PLANE_IMAGE:-$PREFIX/lore-platform-control-plane:$VERSION}|" \
    -e "s|\${LORE_SERVER_IMAGE:-lore-platform-server:local}|\${LORE_SERVER_IMAGE:-$PREFIX/lore-platform-server:$VERSION}|" \
    "$compose"
# The build blocks point into the source tree, which the bundle doesn't carry. Without them
# `setup` (which passes --build) simply pulls the images.
sed -i -e '/^    build:$/,/^      dockerfile: /d' "$compose"
grep -q "$PREFIX/lore-platform-control-plane:$VERSION" "$compose" || { echo "control plane image wasn't set" >&2; exit 1; }
grep -q "$PREFIX/lore-platform-server:$VERSION" "$compose" || { echo "server image wasn't set" >&2; exit 1; }
if grep -q '^    build:' "$compose"; then echo "build blocks remain in compose.yaml" >&2; exit 1; fi
printf '%s\n' "$VERSION" > "$STAGE/$NAME/VERSION"

(cd "$STAGE" && tar -czf "$OUTPUT/$NAME.tar.gz" --owner=0 --group=0 "$NAME")
if command -v zip >/dev/null 2>&1; then
    (cd "$STAGE" && zip -qr "$OUTPUT/$NAME.zip" "$NAME")
fi
ls -l "$OUTPUT"
