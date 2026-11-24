// SPDX-License-Identifier: MIT
#include "lore_common.h"

#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QFileSystemWatcher>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QProcess>
#include <QRandomGenerator>
#include <QStandardPaths>
#include <QtEndian>

#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

namespace Lore
{
namespace
{
// The status cache, as LoreCompanion.FileStatusCache writes it.
constexpr int Capacity = 4096;
constexpr int HeaderSize = 16;
constexpr int EntrySize = 24;
constexpr quint32 Magic = 0x3143534C;
constexpr int MaximumRoots = 50000;

QString runtimeFolder()
{
    const QString runtime = qEnvironmentVariable("XDG_RUNTIME_DIR");
    return runtime.isEmpty() ? QString() : runtime + QStringLiteral("/lore");
}

bool privateFolder(const QString &folder)
{
    if (!QDir().mkpath(folder)) return false;
    return ::chmod(QFile::encodeName(folder).constData(), 0700) == 0;
}
}

quint64 pathHash(const QString &path)
{
    QString trimmed = path;
    while (trimmed.endsWith(QLatin1Char('/'))) trimmed.chop(1);
    quint64 hash = 14695981039346656037ULL;
    for (const QChar unit : std::as_const(trimmed)) {
        hash ^= unit.unicode();
        hash *= 1099511628211ULL;
    }
    return hash;
}

QString repositoryRoot(const QString &path)
{
    QFileInfo info(path);
    QDir directory(info.isDir() ? info.absoluteFilePath() : info.absolutePath());
    while (true) {
        if (QFileInfo(directory.filePath(QStringLiteral(".lore"))).isDir()) return directory.absolutePath();
        if (!directory.cdUp()) return QString();
    }
}

QString companion()
{
    const QStringList candidates = {
        qEnvironmentVariable("LORE_COMPANION"),
        QDir::homePath() + QStringLiteral("/.local/lib/lore/LoreCompanion.Linux"),
        QStringLiteral("/opt/lore/LoreCompanion.Linux"),
    };
    for (const QString &candidate : candidates) {
        if (!candidate.isEmpty() && QFileInfo(candidate).isExecutable()) return candidate;
    }
    return QString();
}

bool start(Operation operation, const QStringList &paths)
{
    const QString executable = companion();
    const QString folder = runtimeFolder();
    if (executable.isEmpty() || folder.isEmpty() || !privateFolder(folder)) return false;

    QJsonObject request;
    request.insert(QStringLiteral("Operation"), static_cast<int>(operation));
    request.insert(QStringLiteral("Paths"), QJsonArray::fromStringList(paths));
    const QByteArray body = QJsonDocument(request).toJson(QJsonDocument::Compact);

    // Created private and new, so nobody else can read it or have placed it.
    const QString file = folder + QStringLiteral("/request-%1.json").arg(QRandomGenerator::system()->generate64(), 16, 16, QLatin1Char('0'));
    const int descriptor = ::open(QFile::encodeName(file).constData(), O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC, 0600);
    if (descriptor < 0) return false;
    const bool written = ::write(descriptor, body.constData(), body.size()) == body.size();
    ::close(descriptor);
    if (!written || !QProcess::startDetached(executable, {QStringLiteral("--request"), file})) {
        QFile::remove(file);
        return false;
    }
    return true;
}

QString emblem(int state)
{
    switch (state) {
    case 1: return QStringLiteral("emblem-lore-modified");
    case 2: return QStringLiteral("emblem-lore-staged");
    case 3: return QStringLiteral("emblem-lore-conflicted");
    case 4: return QStringLiteral("emblem-lore-locked");
    case 5: return QStringLiteral("emblem-lore-ignored");
    case 6: return QStringLiteral("emblem-lore-staged-modified");
    default: return QString();
    }
}

QHash<quint64, int> readStates(const QByteArray &data)
{
    QHash<quint64, int> states;
    if (data.size() != HeaderSize + Capacity * EntrySize) return states;
    const auto *bytes = reinterpret_cast<const uchar *>(data.constData());
    if (qFromLittleEndian<quint32>(bytes + 4) != Magic) return states;
    const int count = qBound(0, qFromLittleEndian<qint32>(bytes), Capacity);
    for (int index = 0; index < count; ++index) {
        const uchar *entry = bytes + HeaderSize + index * EntrySize;
        const quint64 hash = qFromLittleEndian<quint64>(entry);
        const qint32 state = qFromLittleEndian<qint32>(entry + 8);
        if (hash != 0 && !emblem(state).isEmpty()) states.insert(hash, state);
    }
    return states;
}

StatusCache::StatusCache(QObject *parent)
    : QObject(parent)
{
    const QString folder = runtimeFolder();
    if (folder.isEmpty() || !privateFolder(folder)) return;
    m_path = folder + QStringLiteral("/status-cache");
    // The companion replaces the file by a rename, which a watch on the file itself would lose.
    m_watcher = new QFileSystemWatcher({folder}, this);
    connect(m_watcher, &QFileSystemWatcher::directoryChanged, this, [this] {
        const QByteArray before = m_loaded;
        load();
        if (m_loaded == before) return;
        // A working copy may have been cloned or deleted since the folders were looked at.
        m_roots.clear();
        Q_EMIT changed();
    });
    load();
}

void StatusCache::load()
{
    QFile file(m_path);
    m_loaded = file.open(QIODevice::ReadOnly) ? file.readAll() : QByteArray();
    m_states = readStates(m_loaded);
}

QString StatusCache::rootOf(const QString &folder)
{
    const auto known = m_roots.constFind(folder);
    if (known != m_roots.constEnd()) return *known;
    if (m_roots.size() > MaximumRoots) m_roots.clear();
    const QString root = repositoryRoot(folder);
    m_roots.insert(folder, root);
    return root;
}

bool StatusCache::inWorkingCopy(const QString &path, bool isFolder)
{
    return !rootOf(isFolder ? path : QFileInfo(path).absolutePath()).isEmpty();
}

QString StatusCache::emblemFor(const QString &path, bool isFolder)
{
    if (path.contains(QStringLiteral("/.lore"))) return QString();
    const QString root = rootOf(isFolder ? path : QFileInfo(path).absolutePath());
    if (root.isEmpty()) return QString();
    const auto state = m_states.constFind(pathHash(path));
    if (state != m_states.constEnd()) return emblem(*state);
    return isFolder && QDir::cleanPath(path) == root ? QStringLiteral("emblem-lore-clean") : QString();
}
}
