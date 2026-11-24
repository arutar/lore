// SPDX-License-Identifier: MIT
// The Lore status emblems in Dolphin: the Windows overlays, from the status cache the companion writes
// after every operation. Dolphin asks for each file it shows; when the companion replaces the cache,
// the files asked about are told their new emblems.
#include "lore_common.h"

#include <KOverlayIconPlugin>
#include <QFileInfo>
#include <QSet>
#include <QUrl>

class LoreOverlayIcon : public KOverlayIconPlugin
{
    Q_OBJECT
    Q_PLUGIN_METADATA(IID "org.lore.overlayiconplugin" FILE "lore_overlayicon.json")
public:
    LoreOverlayIcon()
    {
        connect(&m_cache, &Lore::StatusCache::changed, this, [this] {
            const QSet<QUrl> shown = std::exchange(m_shown, {});
            for (const QUrl &url : shown) Q_EMIT overlaysChanged(url, getOverlays(url));
        });
    }

    QStringList getOverlays(const QUrl &url) override
    {
        if (!url.isLocalFile()) return {};
        const QString path = url.toLocalFile();
        const QFileInfo info(path);
        const QString emblem = m_cache.emblemFor(path, info.isDir());
        // Every file in a working copy may gain an emblem with the next operation.
        if (m_shown.size() > MaximumShown) m_shown.clear();
        if (!emblem.isEmpty() || m_cache.inWorkingCopy(path, info.isDir())) m_shown.insert(url);
        return emblem.isEmpty() ? QStringList() : QStringList{emblem};
    }

private:
    static constexpr int MaximumShown = 50000;
    Lore::StatusCache m_cache;
    QSet<QUrl> m_shown;
};

#include "lore_overlayicon.moc"
