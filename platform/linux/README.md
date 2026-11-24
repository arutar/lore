# Lore Linux client

Lore commands in the context menu and status emblems in GNOME Files (Nautilus), Nemo and Caja
through one Python extension, and in KDE Dolphin through two plugins. The menu opens the same
operation dialogs as the Windows client. The design is in
[docs/proposals/2026-11-17-linux-file-manager-integration.md](../../docs/proposals/2026-11-17-linux-file-manager-integration.md).

| Part | What it is |
|---|---|
| `LoreCompanion.Linux/` | The companion: a GTK 3 window with WebKitGTK showing `platform/windows/LoreCompanion.Web`, running the operations of `platform/companion/LoreCompanion.Core` |
| `file-manager/lore_extension.py` | The extension for Nautilus, Nemo and Caja: the menu and the status emblems |
| `dolphin/` | The Dolphin plugins, for KDE Frameworks 5 or 6: the menu (`KAbstractFileItemActionPlugin`) and the emblems (`KOverlayIconPlugin`) |
| `Build-Linux.sh` | Builds the CLI, this fork's native library, the dialogs and the companion into `bin/lore` |
| `Install-Dev.sh` | Installs `bin/lore` for the current user; `--uninstall` removes it |

`.deb` and `.rpm` packages come next.

## Requirements

- To run: GTK 3 and WebKitGTK 4.1 or 4.0 (`webkit2gtk3` on Rocky Linux and RHEL 9, `libwebkit2gtk-4.1-0`
  on Debian and Ubuntu), and the file manager's Python bindings (`nautilus-python` on Rocky Linux
  and RHEL, `python3-nautilus` on Debian and Ubuntu, or the Nemo or Caja bindings).
  For Dolphin: KDE Frameworks 5 (Plasma 5, as in EPEL for Rocky Linux 9) or 6.
- To build: Rust, Node.js with npm, and the .NET 10 SDK; for the Dolphin plugins CMake, a C++ compiler,
  `extra-cmake-modules` and the KIO development files (`kf5-kio-devel` or `kf6-kio-devel` on Rocky
  Linux and Fedora, `libkf6kio-dev` on Debian and Ubuntu). Without them the build skips Dolphin.

## Build and install

```sh
platform/linux/Build-Linux.sh
platform/linux/Install-Dev.sh
nautilus -q
```

The client goes to `~/.local/lib/lore`, `lore` to `~/.local/bin`, the extension to each installed
file manager's extensions folder under `~/.local/share`, and the emblems to
`~/.local/share/icons/hicolor/scalable/emblems`, the menu icons beside them in `apps`. The Dolphin
plugins stay in `~/.local/lib/lore/plugins`, and `~/.config/plasma-workspace/env/lore-plugins.sh`
adds that folder to Qt's plugin path at the next login; packages will install them where Dolphin
looks without it. Right-click a folder: outside a working copy the menu offers **Lore Clone...**,
inside one **Lore Sync**, **Lore Commit...** and the **Lore** submenu.

## How a command runs

The extension writes the command and the selected paths to a file in `$XDG_RUNTIME_DIR/lore`,
readable only by the user, and starts the companion on it. The companion accepts only such a file,
deletes it after reading, and runs the operation in its window; the file manager itself never runs
Lore.

## Status emblems

Files and folders in a working copy show the Windows overlays as emblems: conflicted, staged and
modified, staged, modified, locked, ignored, and clean on a working copy's root. A folder shows the
strongest state of what it holds. As on Windows the emblems change when a Lore operation runs: after
each one the companion records the working copy's status in `$XDG_RUNTIME_DIR/lore/status-cache`
(`FileStatusCache` in `platform/companion/LoreCompanion.Core`), and the extension redraws the files
it shows; the Dolphin plugin tells Dolphin the new emblems. The cache lives for the session, so
after logging in the emblems return with the first operation in a working copy. Nautilus 43 and
later show emblems in fewer places than Nautilus 40.

The extension's tests need no file manager: `python3
platform/linux/file-manager/lore_extension_test.py`.

## Diagnostics

`LORE_COMPANION` names another companion executable, and `LORE_COMPANION_DEBUG=1` prints the messages
between the window and the dialogs, and the dialogs' console, to standard output.
