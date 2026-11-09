#!/usr/bin/env sh
set -eu

VERSION=${LORE_CLIENT_VERSION:-v0.13.0}
INSTALL_DIR=${LORE_INSTALL_DIR:-$HOME/.local/bin}
case $(uname -m) in
    x86_64|amd64)
        ARCH=x86_64
        SHA256=839a88c279794c8e31b1e79155408791b1bf26d17fb6e6b998bc5cda4291c12f
        ;;
    aarch64|arm64)
        ARCH=aarch64
        SHA256=7cd53cc8f5cdf582ec5cd5e9037c358b9bb643e1ad0fdde21f3e9ae5fcd1166d
        ;;
    *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
if [ "$VERSION" != "v0.13.0" ]; then
    echo "This pinned installer currently supports v0.13.0 only" >&2
    exit 1
fi

for tool in tar; do command -v "$tool" >/dev/null 2>&1 || { echo "$tool is required" >&2; exit 1; }; done
if [ -z "${LORE_ARCHIVE_PATH:-}" ]; then
    if command -v curl >/dev/null 2>&1; then
        download() { curl -fsSL "$1" -o "$2"; }
    elif command -v wget >/dev/null 2>&1; then
        download() { wget -qO "$2" "$1"; }
    else
        echo "curl or wget is required for downloading" >&2
        exit 1
    fi
fi

TEMP=$(mktemp -d)
trap 'rm -rf "$TEMP"' EXIT INT TERM
ARCHIVE="lore-$VERSION-$ARCH-unknown-linux-gnu.tar.gz"
if [ -n "${LORE_ARCHIVE_PATH:-}" ]; then
    cp "$LORE_ARCHIVE_PATH" "$TEMP/lore.tar.gz"
else
    download "https://github.com/EpicGames/lore/releases/download/$VERSION/$ARCHIVE" "$TEMP/lore.tar.gz"
fi
if command -v sha256sum >/dev/null 2>&1; then
    echo "$SHA256  $TEMP/lore.tar.gz" | sha256sum -c - >/dev/null
elif command -v shasum >/dev/null 2>&1; then
    echo "$SHA256  $TEMP/lore.tar.gz" | shasum -a 256 -c - >/dev/null
else
    echo "sha256sum or shasum is required" >&2
    exit 1
fi

tar -xzf "$TEMP/lore.tar.gz" -C "$TEMP" ./lore
mkdir -p "$INSTALL_DIR"
install -m 0755 "$TEMP/lore" "$INSTALL_DIR/lore"
"$INSTALL_DIR/lore" --version
printf 'Lore CLI installed to %s/lore\n' "$INSTALL_DIR"
case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) printf 'Add %s to PATH, for example in ~/.profile:\nexport PATH="%s:$PATH"\n' "$INSTALL_DIR" "$INSTALL_DIR" ;;
esac
