#!/usr/bin/env bash
# Installs the Linux client built by Build-Linux.sh for the current user: the companion and the CLI
# in ~/.local/lib/lore, `lore` on ~/.local/bin, and the menu extension for each of GNOME Files,
# Nemo and Caja whose Python bindings are installed. --uninstall removes them again; repository
# instances and settings stay. An optional argument names the built client; by default it is the
# folder holding this script, or bin/lore beside it.
set -euo pipefail

source_dir="$(cd "$(dirname "$0")" && pwd)"
[ -x "$source_dir/LoreCompanion.Linux" ] || source_dir="$source_dir/bin/lore"
if [ -n "${1:-}" ] && [ "$1" != "--uninstall" ]; then source_dir="$(cd "$1" && pwd)"; fi
target="$HOME/.local/lib/lore"
data="${XDG_DATA_HOME:-$HOME/.local/share}"
extensions=("$data/nautilus-python/extensions" "$data/nemo-python/extensions" "$data/caja-python/extensions")
emblems="$data/icons/hicolor/scalable/emblems"
icons="$data/icons/hicolor/scalable/apps"
# Dolphin looks for plugins only on Qt's plugin path, which Plasma takes from these scripts at login.
plasma_env="${XDG_CONFIG_HOME:-$HOME/.config}/plasma-workspace/env/lore-plugins.sh"

if [ "${1:-}" = "--uninstall" ]; then
  rm -rf "$target"
  rm -f "$HOME/.local/bin/lore"
  for folder in "${extensions[@]}"; do rm -f "$folder/lore_extension.py"; done
  rm -f "$emblems"/emblem-lore-*.svg "$icons"/lore-*.svg "$plasma_env"
  echo "Removed the Lore Linux client. Restart the file manager (nautilus -q) to drop its menu."
  exit 0
fi

[ -x "$source_dir/LoreCompanion.Linux" ] || { echo "Run Build-Linux.sh first" >&2; exit 1; }
rm -rf "$target"
mkdir -p "$target" "$HOME/.local/bin"
cp -a "$source_dir/." "$target/"
ln -sf "$target/lore" "$HOME/.local/bin/lore"

# A file manager loads Python extensions only through its python module, lib<name>-python.so.
has_binding() {
  compgen -G "/usr/lib*/$1/extensions-*/lib$1-python.so" >/dev/null || compgen -G "/usr/lib/*/$1/extensions-*/lib$1-python.so" >/dev/null
}

installed=0
for pair in "nautilus:${extensions[0]}" "nemo:${extensions[1]}" "caja:${extensions[2]}"; do
  manager="${pair%%:*}"; folder="${pair#*:}"
  if has_binding "$manager"; then
    mkdir -p "$folder"
    install -m 0644 "$target/lore_extension.py" "$folder/lore_extension.py"
    installed=$((installed + 1))
  fi
done
mkdir -p "$emblems" "$icons"
install -m 0644 "$target"/emblems/emblem-lore-*.svg "$emblems/"
install -m 0644 "$target"/icons/lore-*.svg "$icons/"

if [ -d "$target/plugins" ]; then
  mkdir -p "$(dirname "$plasma_env")"
  # shellcheck disable=SC2016 # the variable is expanded at login, not here
  printf 'export QT_PLUGIN_PATH="%s${QT_PLUGIN_PATH:+:$QT_PLUGIN_PATH}"\n' "$target/plugins" > "$plasma_env"
  installed=$((installed + 1))
  echo "Dolphin: log out and in again (or start dolphin with QT_PLUGIN_PATH=$target/plugins) to load the Lore plugins."
fi
[ "$installed" -gt 0 ] || echo "No file manager Python bindings found: install nautilus-python (python3-nautilus on Debian and Ubuntu)." >&2

echo "Installed the Lore Linux client in $target. Restart the file manager (nautilus -q) to load its menu."
