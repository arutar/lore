---
lep: 2026-11-08-windows-explorer-integration
title: Windows Explorer integration for Lore
authors:
  - arutar
status: Draft
created: 2026-11-08
updated: 2026-11-08
discussion: pending
---

# Windows Explorer integration for Lore

## Summary

This proposal adds a Windows desktop client that integrates Lore with File Explorer. It provides Lore Clone, Sync, and Commit commands, a TortoiseSVN-style submenu, operation dialogs, and file-status overlay icons. An unmanaged C++ shell extension handles Explorer integration. A C# companion performs Lore operations through the official SDK and local service interfaces. Explorer never performs network access, OIDC login, or a [working tree](../glossary.md#working-tree) scan in its own thread.

## Motivation

The Lore CLI exposes full functionality through commands and SDKs, as the [project overview](../../README.md) describes. This proposal targets contributors whose file workflow already happens in File Explorer. The [roadmap](../roadmap.md) includes a desktop client, but this tree contains no open-source Windows shell integration.

A browser client doesn't replace local working-tree operations. Contributors need to clone a repository, inspect local changes, lock binary files, commit, sync, switch branches, and resolve conflicts where their files already live.

Embedding these operations in an Explorer extension would put network latency, authentication, and potentially expensive tree operations inside a sensitive host process.

## Goals / Non-Goals

### Goals

- Add context-sensitive Lore commands to File Explorer with icons and a TortoiseSVN-style structure.
- Provide responsive dialogs for clone, status, commit, sync, history, branches, merges, locks, and conflict resolution.
- Show deterministic normal, modified, staged, conflicted, locked, and ignored states through cached overlays.
- Use the same identity, repository policy, and Lore operations as the Lore CLI.
- Keep network calls, authentication, and unbounded working-tree operations out of Explorer threads.
- Provide an installable, signed, and cleanly removable Windows package.

### Non-Goals

- Changing Lore wire protocols, repository formats, or command semantics.
- Implementing version-control operations in the shell extension.
- Adding shelve or stash commands, which have no Lore analog in the [documentation vocabulary](../developing/doc-standards/canon/language.md#concepts-with-no-lore-analog).
- Supporting non-Windows shells or implementing the planned cross-platform desktop client.
- Running any part of the Windows shell integration in a container.

## Proposed Design

### Process separation

The client has two parts. An unmanaged C++ COM shell extension implements context-menu and overlay handlers. A C# companion owns dialogs, background operations, progress, cancellation, OIDC authentication, and calls through the official [Lore C# SDK](https://github.com/EpicGames/lore-dotnet) and local service interfaces.

The shell extension starts the companion with a bounded JSON request file in the current user's temporary directory. Requests carry selected paths and an operation identifier, never bearer tokens. The companion deletes the request after reading it; operations use bounded status data for display and overlay caching. The companion uses the protected token mechanism in [`lore-credential/src/token_store.rs`](../../lore-credential/src/token_store.rs); the shell extension has no credential access.

If the companion isn't running, a menu command starts it and reports progress. An unavailable companion disables overlays and produces a clear menu error without blocking Explorer.

### Context-menu behavior and command mapping

Outside a [repository instance](../glossary.md#repository-instance), the top-level menu shows **Lore Clone…**. Inside an instance, top-level commands show **Lore Sync** and **Lore Commit…**, with remaining operations under **Lore**.

| Menu command | Lore operation |
| --- | --- |
| **Clone** | `lore repository clone` |
| **Sync / Sync to revision** | `lore sync` with an optional revision |
| **Commit** | `lore commit` after explicit staging |
| **History / Revision graph** | `lore history` and the revision-graph API |
| **Check for modifications** | `lore status --scan` |
| **Resolve** | `lore branch merge resolve` and its mine/theirs choices |
| **Reset local changes** | `lore reset` with confirmation |
| **Clean up** | `lore reset --purge` with an explicit destructive-action warning |
| **Get lock / Release lock** | `lore lock acquire` and `lore lock release` |
| **Branch / Switch / Merge** | The corresponding `lore branch` operations |
| **Add** | `lore dirty` for the selected paths |
| **Stage / Unstage** | `lore stage` and `lore unstage` |
| **Update instance path** | `lore repository update-path` |
| **Repo browser / Open web page** | The configured repository web URL |
| **Copy URL / Properties / Settings / Help / About** | Existing instance metadata and companion configuration |

Unsupported operations don't appear as inactive imitations. Commands that overwrite local changes preserve CLI refusal and confirmation semantics. Long-running operations provide progress, cancellation where supported, and actionable errors.

### Dialogs and operation model

The C# companion presents clone, commit, status, history, branch, sync, lock, merge, and settings dialogs. It calls official APIs rather than scraping CLI output. The same operation layer can host the future desktop client without duplicating shell behavior.

Operations run with bounded concurrency and cooperative cancellation. Status requests use persisted dirty and staged state before requesting a scan, matching the [status command reference](../reference/lore-cli-commands.md). Explicit scans run in the companion and report progress. The shell extension never triggers a recursive tree walk while populating a menu or overlay.

### Overlay states, precedence, and caching

A path can hold several Lore states at once. Explorer presents one deterministic overlay using this precedence:

1. Conflicted, including conflicted and locked.
2. Staged and modified, as a combined icon.
3. Staged, including ignored and staged.
4. Modified, including modified and locked.
5. Locked.
6. Ignored.
7. Normal.

The complete state remains available in status and properties dialogs. This model bounds the required handlers and icons and defines every combination rather than relying on registration order.

Overlay handlers answer from a bounded per-instance cache keyed by path, revision state, and working-tree change signals. The companion refreshes entries asynchronously and invalidates them after operations. A cache miss returns no overlay rather than waiting on disk, IPC, or the remote.

The cache also records ignored paths from the official dirty and status event streams when they report filter exclusion with the ignore reason. Dirty marking can emit this result without creating a dirty node, so the companion doesn't infer ignored state solely from dirty and staged records. Path changes and ignore-file changes invalidate this state; tests cover ignored-file creation, deletion, and rule changes.

Filesystem notifications enter a bounded per-instance queue that coalesces paths. Before refreshing status, the companion reports changed paths through the official dirty-marking API. It then verifies those marks through the official equivalent of `lore status --check-dirty`, which clears a dirty flag when the file again matches its tracked content and persists the result. The overlay cache refreshes from that reconciled state, so a reverted file returns to its normal or staged icon. Queue overflow marks overlay state uncertain and schedules an explicit recovery scan instead of accumulating events.

### Authentication and repository policy

The companion uses the standard Lore OIDC flow and caller-scoped tokens. Lore Server applies the same grants as it does to the Lore CLI. The shell client has no administrative bypass and no separate repository-rule store.

Anonymous public clone works only when the deployment supports [anonymous public repository read](2026-11-08-anonymous-public-repository-read.md). Private clone and every write require the appropriate caller grant.

### Packaging and local configuration

The Windows 11 package uses MSIX identity with sparse packaging for modern context-menu registration. The installer registers and unregisters COM handlers, installs the companion and required Lore libraries, and provides diagnostics without tokens or file contents. The release process signs production binaries and the installer.

Uninstallation removes registrations and terminates the companion. It preserves repository instances and user settings unless the user explicitly removes settings. A test-signed package supports development without establishing production trust.

Companion configuration stores UI preferences and repository web URLs outside working trees. Configuration has a schema version and additive upgrades. A downgrade ignores only optional fields it knows how to preserve; it refuses a newer required schema rather than overwriting it.

## Compatibility

- **Wire format** — N/A. The client uses existing Lore protocols.
- **Client/server protocols** — N/A. No RPC changes. Local IPC is private to the Windows client and versioned with its package.
- **On-disk format** — N/A for Lore repositories. The client uses official APIs and adds no files to history.
- **CLI and public API** — N/A. No command or SDK signature changes. Any missing SDK operation needs a separate proposal before its menu command ships.
- **Windows integration** — New COM registrations, MSIX metadata, and overlays affect only machines where the user installs the package.
- **Companion configuration and local state** — Settings and web URLs use versioned configuration. Caches are disposable. Upgrades migrate settings, downgrades refuse unsupported required schemas, and uninstall retains user data by default.

## Non-Functional Considerations

- **Concurrency** — The companion bounds concurrent Lore operations and serializes operations targeting the same mutable instance state. Overlay queries remain independent and read-only.
- **Memory** — Menus, IPC messages, notification queues, and overlay caches are bounded. Large file content doesn't pass through Explorer or menu IPC.
- **Statelessness** — Explorer extensions keep only disposable cache state. The companion stores durable settings and credentials through existing mechanisms.
- **Determinism** — Menu commands invoke the same operation semantics as their CLI counterparts and don't create different revision contents.
- **Latency** — Menu and overlay handlers avoid network calls, authentication, scans, and blocking IPC on Explorer threads.
- **Reliability and observability** — Companion failure disables overlays without affecting Explorer. Diagnostics identify unavailable companions, stale caches, missing overlay registration, and failed dependencies without logging secrets or content.

## Migration Plan

N/A — no breaking changes, no migration required. Installation is optional and existing repository instances remain usable through the Lore CLI.

## Security Considerations

The shell extension handles untrusted paths inside Explorer. IPC validates path length, encoding, operation identifiers, and current-user identity. It doesn't accept tokens, arbitrary command lines, or remote content as executable instructions.

The companion validates operation paths before invoking Lore. Dialogs don't display bearer tokens. Diagnostics redact credentials and avoid file contents. Package signing protects installation, while TLS and Lore token checks protect remote operations.

Overlay and menu code fail safely on malformed repository metadata and inaccessible paths. A failure can't grant repository access or change working-tree data.

## Privacy Considerations

The companion processes local paths, repository URLs, account identifiers, and operation history. Logs minimize path collection and exclude file contents and credentials. The companion doesn't upload diagnostics automatically.

Uninstall leaves repository instances and user settings in place by default. Explicit settings removal deletes companion caches and configuration without deleting repository content.

## Risks and Assumptions

**Assumptions**

- **Assumption:** The official C# SDK and local service expose the initial menu operations — *invalidated if:* a required operation exists only in the CLI. That operation needs an API addition rather than output parsing.
- **Assumption:** Sparse MSIX identity supports the required Windows 11 context-menu registration — *invalidated if:* organizational policy blocks the package or capability. A separately reviewed classic registration package would be needed.

**Risks**

- **Risk:** An extension hangs or crashes Explorer on an unusual path — *mitigation:* keep handlers small, bound every lookup, isolate work, and test clean Windows VMs with malformed and inaccessible instances.
- **Risk:** Windows drops Lore overlays because of the shared overlay limit discussed in [Microsoft's overlay-management guidance](https://learn.microsoft.com/en-us/answers/questions/4868389/how-do-i-manage-shelliconoverlayidentifiers-in-the) — *mitigation:* minimize registrations, detect missing overlays, and keep full status in dialogs.
- **Risk:** External changes leave stale status or an unbounded notification backlog — *mitigation:* report and verify dirty marks, clear reverted files through `status --check-dirty`, coalesce paths, mark overflow uncertain, and run bounded recovery scans.
- **Risk:** Enterprise policy blocks shell extensions or MSIX packages — *mitigation:* sign production artifacts and test standard-user and managed-device installation.

## Drawbacks

- The client requires two languages and a process boundary for shell and operation code.
- Overlay status remains advisory because external changes can outpace cache refresh.
- Package signing and managed-device deployment require organization-specific certificates and policy work.

## Alternatives Considered

### Invoke the Lore CLI directly from the shell extension

Start a CLI process for each menu command and parse output for dialogs and overlays.

*Rejected because:* command output isn't a stable SDK contract, process creation per overlay would be expensive, and Explorer would inherit command latency and error handling.

### Implement everything inside an Explorer extension

Put authentication, repository operations, and status scanning in the C++ handlers.

*Rejected because:* network and tree operations would run in a host process that expects short responses. Repository or server failure could hang Explorer, and tokens would needlessly enter that process.

### Browser-only local operations

Expose a web page for local clone, commit, and sync actions.

*Rejected because:* a browser doesn't receive trusted access to arbitrary local working trees or Explorer selection and overlays. The design would still need a local companion.

## Prior Art

The [TortoiseSVN user guide](https://tortoisesvn.net/docs/release/TortoiseSVN_en/tsvn-dug.html) demonstrates top-level update and commit commands, a submenu for less frequent operations, dialogs, and status overlays. This proposal borrows that structure while using Lore's own sync, stage, revision, branch, and working-tree semantics.

Microsoft documents [context-menu handlers](https://learn.microsoft.com/windows/win32/shell/context-menu-handlers), [icon overlay handlers](https://learn.microsoft.com/windows/win32/shell/how-to-implement-icon-overlay-handlers), and [package identity for unpackaged applications](https://learn.microsoft.com/windows/apps/desktop/modernize/grant-identity-to-nonpackaged-apps). The design separates in-process handlers from network and repository work.

## Unresolved Questions

- Which SDK or local-service operations need additions for revision graph and conflict-resolution dialogs? See the [low-level revision graph API proposal](2026-10-23-low-level-revision-graph-api.md) and [modified file tracking proposal](2026-05-03-modified-file-tracking.md).
- Which Windows 11 builds and enterprise policies form the supported installation baseline?
- Which status transitions should invalidate an entire instance cache rather than individual paths?
