import { useEffect, useState } from "react";
import { api } from "../api";
import { useApp } from "../App";
import { useLocale } from "../i18n";
import { Icon, useTopBar } from "../ui";

export function Home() {
  const { t } = useLocale();
  const { session, isAdmin, openNewRepository, config } = useApp();
  const [counts, setCounts] = useState<{ total: number; more: boolean; public: number } | null>(null);
  const name = session.user?.displayName?.split(" ")[0] || session.user?.username;
  useTopBar({
    title: session.authenticated ? t("home.hello", { name: name ?? "" }) : t("home.helloGuest"),
    description: session.authenticated ? t("home.hint") : t("home.hintGuest"),
    actions: isAdmin ? <button className="btn primary sm" onClick={openNewRepository}><Icon name="plus" />{t("home.newRepository")}</button> : null,
  }, [session.authenticated, name, isAdmin, t]);
  useEffect(() => {
    api.catalog("", "").then((page) => setCounts({ total: page.items.length, more: Boolean(page.nextCursor), public: page.items.filter((item) => item.public).length })).catch(() => setCounts(null));
  }, []);
  const example = (config.nativeRepositoryUrl || `lore://${window.location.hostname}`).replace(/:41337$/, "") + "/team/project";
  return <div className="stack">
    <div className="stats">
      <div className="stat"><b>{counts ? `${counts.total}${counts.more ? "+" : ""}` : "—"}</b><span>{t("home.available")}</span></div>
      <div className="stat"><b>{counts ? counts.public : "—"}</b><span>{t("home.public")}</span></div>
    </div>
    <div className="card">
      <div className="card-head"><Icon name="terminal" className="file-ico" /><div className="grow"><h2>{t("home.connect")}</h2><p>{t("home.connectText")}</p></div></div>
      <pre className="code">lore clone {example}</pre>
    </div>
  </div>;
}
