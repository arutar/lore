#!/usr/bin/env bash
# Builds the Linux client into platform/linux/bin/lore: the lore CLI and this fork's native library
# (cargo), the operation dialogs (npm), the companion (dotnet) and the file manager extension.
# Install-Dev.sh installs the result for the current user.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
out="$here/bin/lore"
version="${PLATFORM_VERSION:-0.0.0}"

command -v cargo >/dev/null || { echo "Rust and Cargo are required to build the Lore CLI and native library" >&2; exit 1; }
command -v npm >/dev/null || { echo "Node.js and npm are required to build the operation dialogs" >&2; exit 1; }
command -v dotnet >/dev/null || { echo "The .NET 10 SDK is required to build the companion" >&2; exit 1; }

cargo build --release --locked -p lore-capi -p lore-client --manifest-path "$root/Cargo.toml"

web="$root/platform/windows/LoreCompanion.Web"
(cd "$web" && npm ci --no-audit --no-fund && npm run build)

rm -rf "$out"
dotnet publish "$here/LoreCompanion.Linux/LoreCompanion.Linux.csproj" -c Release -r linux-x64 --self-contained true \
  "-p:Version=$version" -o "$out"
# The companion and the CLI run on this fork's library, not the package's.
strip --strip-debug -o "$out/liblorelib.so" "$root/target/release/liblore.so"
strip --strip-debug -o "$out/lore" "$root/target/release/lore"
install -m 0644 "$here/file-manager/lore_extension.py" "$out/lore_extension.py"
# The status emblems are the Windows overlays, framed around their badge rather than the corner of a
# file icon.
mkdir -p "$out/emblems"
for state in modified staged conflicted locked ignored clean staged-modified; do
  box="13 153 346 346"; [ "$state" = staged-modified ] && box="13 16 483 483"
  sed -e "s|width=\"512\" height=\"512\" viewBox=\"0 0 512 512\"|width=\"64\" height=\"64\" viewBox=\"$box\"|" \
    "$root/platform/windows/shared/svg/overlay-$state.svg" > "$out/emblems/emblem-lore-$state.svg"
done
# The menu icons, named lore-<name>.
mkdir -p "$out/icons"
for name in clone sync commit stage unstage revert resolve status history lock unlock accounts menu; do
  install -m 0644 "$root/platform/windows/shared/svg/$name.svg" "$out/icons/lore-$name.svg"
done
# The Dolphin plugins, where KDE Frameworks are installed: 6 if they are there, else 5.
if command -v cmake >/dev/null && { [ -d /usr/include/KF6/KIOWidgets ] || [ -d /usr/include/KF5/KIOWidgets ]; }; then
  frameworks=5; [ -d /usr/include/KF6/KIOWidgets ] && frameworks=6
  cmake -S "$here/dolphin" -B "$here/bin/dolphin-build" -DCMAKE_BUILD_TYPE=Release -DLORE_KF=$frameworks
  cmake --build "$here/bin/dolphin-build" -j "$(nproc)"
  mkdir -p "$out/plugins"
  cp -r "$here/bin/dolphin-build/bin/kf$frameworks" "$out/plugins/"
else
  echo "KDE Frameworks development files not found: the Dolphin plugins are not built" >&2
fi
install -m 0755 "$here/Install-Dev.sh" "$out/install.sh"
echo "Built the Linux client in $out"
