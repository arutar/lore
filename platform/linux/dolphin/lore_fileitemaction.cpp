// SPDX-License-Identifier: MIT
// The Lore menu in Dolphin, as in Windows Explorer: outside a working copy "Lore Clone...", inside one
// "Lore Sync" and "Lore Commit..." and a "Lore" submenu with the other commands.
#include "lore_common.h"

#include <KAbstractFileItemActionPlugin>
#include <KFileItemListProperties>
#include <KPluginFactory>
#include <QAction>
#include <QIcon>
#include <QMenu>
#include <QUrl>

namespace
{
constexpr int MaximumPaths = 256;

struct Entry {
    Lore::Operation operation;
    const char *label;
    const char *icon;
};

// The icons are the Windows menu's, installed in the hicolor theme as lore-<name>.
constexpr Entry Submenu[] = {
    {Lore::Stage, "Stage selected paths", "lore-stage"},
    {Lore::Unstage, "Unstage", "lore-unstage"},
    {Lore::Revert, "Revert changes...", "lore-revert"},
    {Lore::Resolve, "Resolve conflicts...", "lore-resolve"},
    {Lore::Status, "Status", "lore-status"},
    {Lore::History, "History", "lore-history"},
    {Lore::Lock, "Acquire locks", "lore-lock"},
    {Lore::Unlock, "Release locks", "lore-unlock"},
    {Lore::Accounts, "Accounts", "lore-accounts"},
};
}

class LoreFileItemAction : public KAbstractFileItemActionPlugin
{
    Q_OBJECT
public:
    LoreFileItemAction(QObject *parent, const QVariantList &)
        : KAbstractFileItemActionPlugin(parent)
    {
    }

    QList<QAction *> actions(const KFileItemListProperties &items, QWidget *parentWidget) override
    {
        QStringList paths;
        const QList<QUrl> urls = items.urlList();
        for (const QUrl &url : urls) {
            if (!url.isLocalFile()) return {};
            if (paths.size() < MaximumPaths) paths.append(url.toLocalFile());
        }
        if (paths.isEmpty() || Lore::companion().isEmpty()) return {};

        auto action = [&](const QString &label, const char *icon, Lore::Operation operation, QObject *owner) {
            auto *entry = new QAction(QIcon::fromTheme(QString::fromLatin1(icon)), label, owner);
            connect(entry, &QAction::triggered, this, [this, operation, paths] {
                if (!Lore::start(operation, paths)) Q_EMIT error(QStringLiteral("The Lore companion could not be started."));
            });
            return entry;
        };

        if (Lore::repositoryRoot(paths.first()).isEmpty()) return {action(QStringLiteral("Lore Clone..."), "lore-clone", Lore::Clone, parentWidget)};

        auto *submenu = new QMenu(QStringLiteral("Lore"), parentWidget);
        submenu->setIcon(QIcon::fromTheme(QStringLiteral("lore-menu")));
        for (const Entry &entry : Submenu) submenu->addAction(action(QString::fromLatin1(entry.label), entry.icon, entry.operation, submenu));
        return {
            action(QStringLiteral("Lore Sync"), "lore-sync", Lore::Sync, parentWidget),
            action(QStringLiteral("Lore Commit..."), "lore-commit", Lore::Commit, parentWidget),
            submenu->menuAction(),
        };
    }
};

K_PLUGIN_CLASS_WITH_JSON(LoreFileItemAction, "lore_fileitemaction.json")

#include "lore_fileitemaction.moc"
