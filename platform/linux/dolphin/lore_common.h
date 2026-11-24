// SPDX-License-Identifier: MIT
// What the Dolphin plugins share with lore_extension.py: finding a working copy, starting the
// companion on a request, and reading the status cache the companion writes after every operation
// (LoreCompanion.FileStatusCache). Nothing here runs Lore, touches the network or scans a folder.
#pragma once

#include <QHash>
#include <QObject>
#include <QString>
#include <QStringList>

class QFileSystemWatcher;

namespace Lore
{
// The values of LoreCompanion.CompanionOperation, as the Windows menu sends them.
enum Operation {
    Clone = 0,
    Sync = 1,
    Commit = 2,
    Stage = 3,
    Status = 4,
    History = 5,
    Lock = 6,
    Unlock = 7,
    Accounts = 8,
    Unstage = 9,
    Revert = 10,
    Resolve = 11,
};

/// LoreCompanion.PathText.Hash: 64-bit FNV-1a over the path's UTF-16 code units, without a trailing
/// separator and with case kept.
quint64 pathHash(const QString &path);

/// The working copy holding path: the nearest folder upwards with a .lore folder, or an empty string.
QString repositoryRoot(const QString &path);

/// The companion executable: LORE_COMPANION, else the per-user or system installation; empty when
/// there is none.
QString companion();

/// Writes a private request file in $XDG_RUNTIME_DIR/lore and starts the companion on it. Paths
/// travel only as JSON data, never on a command line.
bool start(Operation operation, const QStringList &paths);

/// The status emblem a cache state shows as, or an empty string for none.
QString emblem(int state);

/// The path hashes and states in a status cache file's bytes; nothing for a file that isn't one.
QHash<quint64, int> readStates(const QByteArray &data);

/// The states the companion recorded, read again whenever it replaces the cache file.
class StatusCache : public QObject
{
    Q_OBJECT
public:
    explicit StatusCache(QObject *parent = nullptr);

    /// The emblem for a local path, or an empty string outside a working copy and for files without
    /// a state. A working copy's root without one shows as clean, as on Windows.
    QString emblemFor(const QString &path, bool isFolder);

    /// Whether path is in a working copy, from what the cache remembers of its folders.
    bool inWorkingCopy(const QString &path, bool isFolder);

Q_SIGNALS:
    void changed();

private:
    void load();
    QString rootOf(const QString &folder);

    QString m_path;
    QHash<quint64, int> m_states;
    QHash<QString, QString> m_roots;
    QByteArray m_loaded;
    QFileSystemWatcher *m_watcher = nullptr;
};
}
