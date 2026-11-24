# SPDX-License-Identifier: MIT
"""Lore in GNOME Files (Nautilus), Nemo and Caja: the context menu and the status emblems.

The menu matches the Windows Explorer one: outside a working copy, "Lore Clone..."; inside one,
"Lore Sync" and "Lore Commit..." on top and the other commands in a "Lore" submenu. A command only
writes a request file and starts the Lore companion, which runs the operation in its own window.

The emblems come from the status cache the companion writes after every operation,
$XDG_RUNTIME_DIR/lore/status-cache, laid out as LoreCompanion.FileStatusCache describes. As on
Windows they change only when an operation ran: this module never runs Lore, touches the network or
scans a folder inside the file manager.
"""

import json
import os
import struct
import tempfile

import gi
from gi.repository import GLib, GObject

# The file manager that loaded this module has already asked gi for its own namespace. Outside one,
# as in the tests, only the functions below are defined.
FileManager = None
for _name in ("Nautilus", "Nemo", "Caja"):
    if gi.get_required_version(_name) is not None:
        FileManager = getattr(__import__("gi.repository", fromlist=[_name]), _name)
        break

# The values of LoreCompanion.CompanionOperation, as the Windows menu sends them.
CLONE, SYNC, COMMIT, STAGE, STATUS, HISTORY, LOCK, UNLOCK, ACCOUNTS, UNSTAGE, REVERT, RESOLVE = range(12)

SUBMENU = (
    (STAGE, "Stage selected paths"),
    (UNSTAGE, "Unstage"),
    (REVERT, "Revert changes..."),
    (RESOLVE, "Resolve conflicts..."),
    (STATUS, "Status"),
    (HISTORY, "History"),
    (LOCK, "Acquire locks"),
    (UNLOCK, "Release locks"),
    (ACCOUNTS, "Accounts"),
)

# The Windows menu's icons, installed in the hicolor theme as lore-<name>.
ICONS = {
    CLONE: "clone", SYNC: "sync", COMMIT: "commit", STAGE: "stage", STATUS: "status", HISTORY: "history",
    LOCK: "lock", UNLOCK: "unlock", ACCOUNTS: "accounts", UNSTAGE: "unstage", REVERT: "revert", RESOLVE: "resolve",
}

MAXIMUM_PATHS = 256

# The status cache, as LoreCompanion.FileStatusCache writes it.
CAPACITY = 4096
HEADER_SIZE = 16
ENTRY = struct.Struct("<QiqI")  # path hash, state, ticks, root hash: 24 bytes
MAGIC = 0x3143534C

# LoreCompanion.OverlayState values and their emblems, the overlays of the Windows client. A working
# copy's root with no state shows as clean, as on Windows.
EMBLEMS = {
    1: "lore-modified",
    2: "lore-staged",
    3: "lore-conflicted",
    4: "lore-locked",
    5: "lore-ignored",
    6: "lore-staged-modified",
}
CLEAN = "lore-clean"


def runtime_folder():
    runtime = os.environ.get("XDG_RUNTIME_DIR")
    return os.path.join(runtime, "lore") if runtime else None


def path_hash(path):
    """LoreCompanion.PathText.Hash: 64-bit FNV-1a over the path's UTF-16 code units, without a
    trailing separator and with case kept."""
    value = 14695981039346656037
    for (unit,) in struct.iter_unpack("<H", path.rstrip("/").encode("utf-16-le", "surrogatepass")):
        value = ((value ^ unit) * 1099511628211) & 0xFFFFFFFFFFFFFFFF
    return value


def read_states(data):
    """The path hashes and states in a status cache file's bytes; nothing for a file that isn't one."""
    if len(data) != HEADER_SIZE + CAPACITY * ENTRY.size:
        return {}
    count, magic = struct.unpack_from("<iI", data)
    if magic != MAGIC:
        return {}
    states = {}
    for index in range(max(0, min(count, CAPACITY))):
        hash_, state, _, _ = ENTRY.unpack_from(data, HEADER_SIZE + index * ENTRY.size)
        if hash_ and state in EMBLEMS:
            states[hash_] = state
    return states


def companion():
    """The companion executable: LORE_COMPANION, else the per-user or system installation."""
    candidates = [
        os.environ.get("LORE_COMPANION", ""),
        os.path.expanduser("~/.local/lib/lore/LoreCompanion.Linux"),
        "/opt/lore/LoreCompanion.Linux",
    ]
    for candidate in candidates:
        if candidate and os.access(candidate, os.X_OK):
            return candidate
    return None


def repository_root(path):
    """The working copy holding path: the nearest folder upwards with a .lore folder, or None."""
    directory = path if os.path.isdir(path) else os.path.dirname(path)
    while True:
        if os.path.isdir(os.path.join(directory, ".lore")):
            return directory
        parent = os.path.dirname(directory)
        if parent == directory:
            return None
        directory = parent


def local_path(item):
    location = item.get_location()
    return location.get_path() if location is not None else None


def local_paths(files):
    """The local paths of the selected files; None when any of them isn't a local file."""
    paths = []
    for item in files[:MAXIMUM_PATHS]:
        path = local_path(item)
        if not path:
            return None
        paths.append(path)
    return paths


def start(operation, paths):
    """Writes a private request file in $XDG_RUNTIME_DIR/lore and starts the companion on it.
    Paths travel only as JSON data, never on a command line."""
    executable = companion()
    folder = runtime_folder()
    if executable is None or folder is None:
        return
    os.makedirs(folder, mode=0o700, exist_ok=True)
    os.chmod(folder, 0o700)
    descriptor, request = tempfile.mkstemp(prefix="request-", suffix=".json", dir=folder)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        json.dump({"Operation": operation, "Paths": paths}, stream)
    try:
        GLib.spawn_async([executable, "--request", request], flags=GLib.SpawnFlags.DEFAULT)
    except GLib.Error:
        os.unlink(request)


def menu_items(paths):
    if not paths or companion() is None:
        return []

    def item(name, label, operation):
        icon = f"lore-{ICONS[operation]}"
        entry = FileManager.MenuItem(name=f"Lore::{name}", label=label, tip="", icon=icon)
        entry.connect("activate", lambda *_: start(operation, paths))
        return entry

    if repository_root(paths[0]) is None:
        return [item("Clone", "Lore Clone...", CLONE)]

    submenu = FileManager.Menu()
    for operation, label in SUBMENU:
        submenu.append_item(item(label.split()[0], label, operation))
    lore = FileManager.MenuItem(name="Lore::Submenu", label="Lore", tip="", icon="lore-menu")
    lore.set_submenu(submenu)
    return [item("Sync", "Lore Sync", SYNC), item("Commit", "Lore Commit...", COMMIT), lore]


class StatusCache:
    """The states the companion recorded, read once per change of the cache file. Files the file
    manager asked about inside working copies are kept so they can be redrawn when it changes."""

    MAXIMUM_SHOWN = 50000

    def __init__(self):
        self.states = {}
        self.roots = {}  # folder -> its working copy's root, or None
        self.shown = {}  # path -> file info, for the redraw
        self.monitor = None
        folder = runtime_folder()
        if folder is None:
            return
        self.path = os.path.join(folder, "status-cache")
        try:
            os.makedirs(folder, mode=0o700, exist_ok=True)
            from gi.repository import Gio

            self.monitor = Gio.File.new_for_path(self.path).monitor_file(Gio.FileMonitorFlags.NONE, None)
            self.monitor.connect("changed", self.changed)
        except Exception:  # Without a monitor the emblems still show; they just don't redraw by themselves.
            self.monitor = None
        self.load()

    def load(self):
        try:
            with open(self.path, "rb") as stream:
                self.states = read_states(stream.read())
        except OSError:
            self.states = {}

    def changed(self, *_):
        self.load()
        # A working copy may have been cloned or deleted since the folders were looked at.
        self.roots.clear()
        shown, self.shown = self.shown, {}
        for info in shown.values():
            info.invalidate_extension_info()

    def root_of(self, folder):
        if folder not in self.roots:
            if len(self.roots) > self.MAXIMUM_SHOWN:
                self.roots.clear()
            self.roots[folder] = repository_root(folder)
        return self.roots[folder]

    def emblem(self, path, is_folder):
        """The emblem for path, or None outside a working copy and for files without a state."""
        if f"{os.sep}.lore" in path:
            return None
        root = self.root_of(path if is_folder else os.path.dirname(path))
        if root is None:
            return None
        state = self.states.get(path_hash(path))
        if state is not None:
            return EMBLEMS[state]
        return CLEAN if is_folder and path == root else None

    def remember(self, path, info):
        if len(self.shown) > self.MAXIMUM_SHOWN:
            self.shown.clear()
        self.shown[path] = info


if FileManager is not None:
    _COMPLETE = FileManager.OperationResult.COMPLETE

    class LoreExtension(GObject.GObject, FileManager.MenuProvider, FileManager.InfoProvider):
        def __init__(self):
            super().__init__()
            self.cache = StatusCache()

        # Nautilus 43 and later pass (files) and (folder); Nautilus 40, Nemo and Caja pass the window first.
        def get_file_items(self, *args):
            try:
                return menu_items(local_paths(args[-1]))
            except Exception:  # A plugin error must never break the file manager's menu.
                return []

        def get_background_items(self, *args):
            try:
                return menu_items(local_paths([args[-1]]))
            except Exception:
                return []

        def update_file_info(self, info):
            try:
                path = local_path(info)
                if path:
                    emblem = self.cache.emblem(path, info.is_directory())
                    if emblem is not None:
                        info.add_emblem(emblem)
                        self.cache.remember(path, info)
                    elif self.cache.root_of(path if info.is_directory() else os.path.dirname(path)) is not None:
                        self.cache.remember(path, info)
            except Exception:  # Never let an emblem break the file manager's view.
                pass
            return _COMPLETE
