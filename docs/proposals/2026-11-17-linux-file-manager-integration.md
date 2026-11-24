---
lep: 2026-11-17-linux-file-manager-integration
title: Linux file manager integration for Lore
authors:
  - arutar
status: Draft
created: 2026-11-17
updated: 2026-11-24
discussion: pending
---

# Linux file manager integration for Lore

## Summary

This proposal brings the Lore Windows client's file-manager integration to Linux desktops. It adds Lore commands to the context menu and status icons to files in GNOME Files (Nautilus) and its forks Nemo and Caja, and in KDE Dolphin. The menu entries open the same operation dialogs that the Windows companion shows. The Windows design in [Windows Explorer integration](2026-11-08-windows-explorer-integration.md) applies unchanged in principle: file-manager plugins only read cached state and start the companion, and the companion performs every Lore operation through the official SDK.

## Motivation

The platform ships a Windows client with Explorer integration and only a CLI on Linux, as [`platform/docs/current-status.md`](../../platform/docs/current-status.md) records. Contributors on Linux desktops who work with files in a file manager have no way to clone, sync, commit, stage, lock, or resolve conflicts there, and no indication of which files are modified, staged, conflicted, or locked.

Linux desktops in the target organizations use both GNOME and KDE, so one file manager isn't enough.

## Goals / Non-Goals

### Goals

- The same commands as the Windows menu: Clone, Sync, Commit, Stage, Unstage, Revert, Resolve conflicts, Status, History, Lock, Unlock, Accounts.
- The same operation dialogs and safety behaviour as the Windows companion, from one shared implementation.
- Status icons with the same states and precedence as the Windows overlays.
- GNOME Files, Nemo and Caja, and KDE Dolphin.
- No Lore operation, network access, credential, or recursive scan inside a file-manager process.
- Installable `.deb` and `.rpm` packages produced by the platform release.

### Non-Goals

- Changing Lore protocols, repository formats, or command semantics.
- Other file managers (Thunar, PCManFM) beyond what the shared design makes easy later.
- Changing the Windows client's behaviour.

## Proposed Design

### Shared companion

The Windows companion's operation layer — operations, request validation, accounts, client settings, the lock cache — already depends only on the [LoreVcs](https://www.nuget.org/packages/LoreVcs) SDK and the native `lore-capi` library, which builds on Linux. This proposal moves it into a platform-neutral library that both companions use. The Windows-specific parts stay in the Windows host: the WebView2 window, the named shared-memory overlay cache, and `SHChangeNotify` redraw hints. Each host supplies its own status-cache writer and redraw hint.

The Linux companion is a separate executable with the same `--request <file>` contract as on Windows. It hosts the existing operation dialogs (`platform/windows/LoreCompanion.Web`) in a WebKitGTK window and exchanges the same JSON messages with them.

The window calls GTK 3 and WebKitGTK directly through P/Invoke rather than through a webview library: Photino.NET needs WebKitGTK 4.1, and Rocky Linux 9 ships only 4.0. The companion loads `libwebkit2gtk-4.1` where it exists and `libwebkit2gtk-4.0` otherwise; both offer the same API. The dialogs load from the companion's folder under a `lore://client/` scheme, the counterpart of WebView2's `https://lore.client` virtual host, and a user script gives them the `window.chrome.webview` object they use on Windows. The same script formats numbers without `Intl`, because JavaScriptCore in WebKitGTK 2.54 on Rocky Linux 9 aborts the page's process the first time `Intl` formats a number. The request file lives in `$XDG_RUNTIME_DIR` with mode `0600`, carries paths and an operation identifier, never tokens, and the companion deletes it after reading.

Credentials use the existing store in [`lore-credential/src/token_store.rs`](../../lore-credential/src/token_store.rs), which on Linux uses the kernel session keyring.

### Status cache

The companion writes file states to a per-user cache file at `$XDG_RUNTIME_DIR/lore/status-cache`. It keeps the Windows layout: a fixed number of fixed-size entries keyed by a path hash, with the same state values and precedence (conflicted, staged and modified, staged, modified, locked, ignored, normal). Linux paths are case-sensitive, so the hash covers the exact UTF-8 path. Writers serialize through an advisory lock and replace the file whole, by a rename, once an operation's states are complete; the replacement is what tells the plugins to redraw. Readers don't take the lock: they read the file as one piece and treat a file of another version, or an unknown entry, as no state.

A cache miss shows no icon. A plugin never waits for the companion. After an operation the companion rewrites the affected entries, and plugins watch the cache file and redraw.

### GNOME Files, Nemo and Caja

One Python extension serves the three file managers through [nautilus-python](https://gitlab.gnome.org/GNOME/nautilus-python) and its Nemo and Caja counterparts, with a thin adapter for each API. It provides:

- A menu: outside a repository instance, **Lore Clone…**; inside one, **Lore Sync** and **Lore Commit…** on top and the other commands in a **Lore** submenu, as on Windows. Finding the instance walks up from the selected path to the nearest `.lore` directory and stops there.
- Emblems: the state from the status cache, shown with Lore emblem icons installed in the `hicolor` icon theme.

A menu command writes a request file and starts the companion, then returns.

### KDE Dolphin

A C++ plugin built against KDE Frameworks provides the same menu through `KAbstractFileItemActionPlugin` and the same status icons through [`KOverlayIconPlugin`](https://api.kde.org/frameworks/kio/html/classKOverlayIconPlugin.html), reading the same status cache.

### Packaging and release

The platform release adds `.deb` and `.rpm` packages, x86-64 first. They contain the CLI, the companion as a self-contained .NET application, the native library, the GNOME extension, the Dolphin plugin and the icons, and they declare dependencies on the distribution's WebKitGTK and the file managers' Python bindings. Uninstall removes the plugins and leaves repository instances and user settings in place.

### Delivery order

1. Shared library, Linux companion and the GNOME menu.
2. GNOME emblems through the status cache.
3. Dolphin plugin.
4. Packages in the release workflow.

Each step is usable on its own.

## Compatibility

- **Wire format** — N/A. The client uses existing Lore protocols.
- **Client/server protocols** — N/A. No RPC changes.
- **On-disk format** — N/A. The client adds no files to repository instances or history.
- **CLI and public API** — N/A. No command or SDK changes.
- **Windows client** — The move of the operation layer into a shared library preserves the Windows companion's behaviour; the Windows client tests in `platform/windows/tests` verify it.
- **Local IPC and status cache** — Private to the Linux client and versioned with its package. A cache from another version is discarded and rebuilt.

## Non-Functional Considerations

- **Concurrency** — The companion serializes operations on the same instance, as on Windows. Plugins only read the status cache.
- **Memory** — The status cache has a fixed size. Plugins hold no per-repository state beyond what the file manager asks for.
- **Statelessness** — Plugins keep no durable state. The status cache is disposable and lives in the per-session runtime directory.
- **Determinism** — Menu commands run the same operations as on Windows and produce the same revisions as the CLI.
- **Latency** — Menu and icon callbacks do bounded local work: a walk up the parent directories and a cache lookup.

## Migration Plan

N/A — no breaking changes, no migration required. Installation is optional; existing instances remain usable through the CLI.

## Security Considerations

The trust model doesn't change. Plugins run inside the user's file manager and receive untrusted paths. They pass paths only as JSON data in a request file and never build a shell command line from them. The companion validates requests as on Windows before invoking Lore.

The request file and the status cache live in `$XDG_RUNTIME_DIR`, which only the user can access. The companion rejects a request file that it doesn't own or that other users can read. The status cache holds path hashes and states, not paths, file contents, or credentials. Tokens stay in the session keyring and never pass through plugins.

## Privacy Considerations

The client processes local paths, repository URLs, and account identifiers, as on Windows. It sends nothing beyond the Lore operations the user starts and uploads no diagnostics. The status cache contains no paths, and the runtime directory is cleared at logout.

## Risks and Assumptions

**Assumptions**

- **Assumption:** `lore-capi` and the LoreVcs SDK work on Linux as the CLI does — *invalidated if:* an operation the companion uses fails only on Linux; that operation then needs a fix in the SDK or library.
- **Assumption:** The target distributions package WebKitGTK and the file managers' Python bindings — *invalidated if:* a baseline distribution lacks them; the package then has to bring its own or drop that file manager.
- **Assumption:** The supported GNOME Files versions still display extension emblems — *invalidated if:* a target version ignores them; status then stays available in the Status dialog only, and the proposal needs a different indicator for that version.

**Risks**

- **Risk:** A plugin error hangs or crashes the file manager — *mitigation:* plugins do only bounded local reads, catch every error and show nothing rather than fail, and tests cover malformed caches and inaccessible paths.
- **Risk:** Separate APIs for Nautilus 3.x and 4.x, Nemo, Caja, KDE Frameworks 5 and 6 multiply the test matrix — *mitigation:* keep plugin logic in shared code with thin adapters and test the adapters against a fixed list of distributions.
- **Risk:** Session keyring tokens don't survive a reboot — *mitigation:* accepted; the user signs in again through the Accounts dialog, as with the CLI.

## Drawbacks

- The Linux client needs Python plugins, a C++ plugin, and a .NET companion: three languages for one feature.
- Each desktop environment needs its own plugin and its own tests.

## Alternatives Considered

### Desktop action files only

Install static menu entries (Dolphin service menus, Nautilus scripts) that run the CLI in a terminal.

*Rejected because:* static entries can't tell a repository instance from another folder, show no status icons, and leave the user with terminal output instead of the dialogs that stop before a destructive choice.

### A separate Linux client

Write the Linux companion from scratch, for example with a native GTK or Qt interface.

*Rejected because:* it duplicates the operation layer and dialogs that already exist and are tested on Windows, and the two clients would drift apart in safety behaviour.

### Plugins call Lore directly

Let the plugins run Lore operations and status queries in the file-manager process.

*Rejected because:* network access, authentication, and scans would block or crash the file manager, the same reason the Windows design separates them.

## Prior Art

The Nextcloud desktop client's [shell integration](https://github.com/nextcloud/desktop/tree/master/shell_integration) uses the same split: Python extensions for Nautilus, Nemo and Caja and a C++ Dolphin plugin, which show menu items and emblems from state that the main client process supplies. RabbitVCS offers TortoiseSVN-style menus and emblems for Subversion and Git in Nautilus, Nemo and Caja.

## Unresolved Questions

- Which distributions and versions form the supported baseline, and do they need KDE Frameworks 5 as well as 6?
- Whether the companion should become a per-session service later, to refresh status after edits made outside Lore, as the Windows design plans.
