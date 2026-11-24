#!/usr/bin/env bash
# Packs the Linux client that Build-Linux.sh built into the packages of one distribution:
#   lore-client          the companion, the CLI, the file manager extension and the icons
#   lore-client-dolphin  the Dolphin plugins, built for that distribution's KDE Frameworks
#
# make-packages.sh <el9|ubuntu2404|debian13> <client folder> <Dolphin build folder or -> <version> <output folder>
#
# The client folder is Build-Linux.sh's bin/lore. The Dolphin build folder is a configured CMake build
# of platform/linux/dolphin; without one only lore-client is made.
set -euo pipefail

distro=$1 client=$(cd "$2" && pwd) dolphin=$3 version=$4
mkdir -p "$5"
out=$(cd "$5" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

case "$distro" in
  el9) format=rpm ;;
  ubuntu2404|debian13) format=deb ;;
  *) echo "unknown distribution $distro" >&2; exit 1 ;;
esac

# The client's files in the places the system and the file managers look.
stage_client() {
  local root=$1
  mkdir -p "$root/opt/lore" "$root/usr/bin" "$root/usr/share/icons/hicolor/scalable/emblems" "$root/usr/share/icons/hicolor/scalable/apps"
  cp -a "$client/." "$root/opt/lore/"
  rm -rf "$root/opt/lore/emblems" "$root/opt/lore/icons" "$root/opt/lore/plugins" "$root/opt/lore/install.sh" "$root/opt/lore/lore_extension.py"
  find "$root/opt/lore" -name "*.pdb" -delete
  ln -s /opt/lore/lore "$root/usr/bin/lore"
  for manager in nautilus nemo caja; do
    install -D -m 0644 "$client/lore_extension.py" "$root/usr/share/$manager-python/extensions/lore_extension.py"
  done
  install -m 0644 "$client"/emblems/emblem-lore-*.svg "$root/usr/share/icons/hicolor/scalable/emblems/"
  install -m 0644 "$client"/icons/lore-*.svg "$root/usr/share/icons/hicolor/scalable/apps/"
}

# The Dolphin plugins where this distribution's Qt finds them.
stage_dolphin() {
  DESTDIR="$1" cmake --install "$dolphin" --prefix /usr > /dev/null
}

icon_cache='if command -v gtk-update-icon-cache >/dev/null 2>&1; then gtk-update-icon-cache -qtf /usr/share/icons/hicolor || :; fi'

deb() {
  local name=$1 root=$2 depends=$3 recommends=$4 description=$5
  local tag=${distro/ubuntu2404/ubuntu24.04}; tag=${tag/debian13/debian13}
  local full="$version-1.$tag"
  mkdir -p "$root/DEBIAN"
  {
    echo "Package: $name"
    echo "Version: $full"
    echo "Architecture: amd64"
    echo "Maintainer: Lore Platform <noreply@github.com>"
    echo "Homepage: https://github.com/arutar/lore"
    echo "Section: vcs"
    echo "Priority: optional"
    echo "Installed-Size: $(du -sk --exclude=DEBIAN "$root" | cut -f1)"
    echo "Depends: $depends"
    [ -z "$recommends" ] || echo "Recommends: $recommends"
    echo "Description: $description"
  } > "$root/DEBIAN/control"
  for script in postinst postrm; do
    printf '#!/bin/sh\nset -e\n%s\n' "$icon_cache" > "$root/DEBIAN/$script"
    chmod 0755 "$root/DEBIAN/$script"
  done
  dpkg-deb --root-owner-group -Zxz --build "$root" "$out/${name}_${full}_amd64.deb" > /dev/null
}

rpm() {
  local name=$1 root=$2 requires=$3 recommends=$4 summary=$5 autoreq=$6
  local spec="$work/$name.spec"
  {
    echo "Name: $name"
    echo "Version: $version"
    echo "Release: 1.el9"
    echo "Summary: $summary"
    echo "License: MIT"
    echo "URL: https://github.com/arutar/lore"
    echo "AutoReqProv: $autoreq"
    echo "Requires: $requires"
    [ -z "$recommends" ] || echo "Recommends: $recommends"
    echo "%define __strip /bin/true"
    echo "%define _build_id_links none"
    echo "%description"
    echo "$summary."
    echo "%install"
    echo "cp -a $root/. %{buildroot}/"
    echo "%post"
    echo "$icon_cache"
    echo "%postun"
    echo "$icon_cache"
    echo "%files"
    (cd "$root" && find . -mindepth 1 \( -type f -o -type l \) | sed 's|^\.||' | sort)
    # The folders the package owns, so removing it leaves none behind.
    (cd "$root" && find . -mindepth 1 -type d | sed 's|^\.||' | sort | { grep -E '^/opt/lore|-python/extensions$' || :; } | sed 's|^|%dir |')
  } > "$spec"
  rpmbuild -bb --quiet --define "_topdir $work/rpmbuild" --define "_rpmdir $out" --define "_build_name_fmt %%{NAME}-%%{VERSION}-%%{RELEASE}.%%{ARCH}.rpm" "$spec"
}

client_root="$work/client"
stage_client "$client_root"
if [ "$format" = deb ]; then
  deb lore-client "$client_root" "libgtk-3-0t64 | libgtk-3-0, libwebkit2gtk-4.1-0, libssl3t64 | libssl3" \
    "python3-nautilus | python3-nemo | python3-caja" \
    "Lore version control in the file manager: menus, status emblems and operation windows for GNOME Files, Nemo and Caja, and the lore command line client"
else
  rpm lore-client "$client_root" "gtk3, webkit2gtk3, openssl-libs" "nautilus-python" \
    "Lore version control in the file manager: menus, status emblems and operation windows, and the lore command line client" no
fi

if [ "$dolphin" != - ]; then
  dolphin_root="$work/dolphin"
  stage_dolphin "$dolphin_root"
  if [ "$format" = deb ]; then
    tag=${distro/ubuntu2404/ubuntu24.04}
    deb lore-client-dolphin "$dolphin_root" "lore-client (= $version-1.$tag), dolphin" "" \
      "Lore menus and status emblems in Dolphin"
  else
    rpm lore-client-dolphin "$dolphin_root" "lore-client = $version-1.el9, dolphin" "" "Lore menus and status emblems in Dolphin" yes
  fi
fi
ls -1 "$out"
