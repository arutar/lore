import { useEffect, useState } from "react";
import { api } from "../api";
import { useApp } from "../App";
import { useLocale } from "../i18n";
import type { ClientDownload, ClientDownloads } from "../types";
import { formatSize, Icon, useCopy, useTopBar } from "../ui";

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
    <InstallClient />
    <div className="card">
      <div className="card-head"><span className="os-tile connect"><Icon name="repo" /></span><div className="grow"><h2>{t("home.connect")}</h2><p>{t("home.connectText")}</p></div></div>
      <pre className="code">lore clone {example}</pre>
    </div>
  </div>;
}

/** The client builds this server hands out, one row per system: the release on GitHub or the
 * server's own copies, as its manifest says. Without a manifest the card points to the releases. */
function InstallClient() {
  const { t, language } = useLocale();
  const [downloads, setDownloads] = useState<ClientDownloads | null | undefined>(undefined);
  const [arch, setArch] = useState<ClientDownload["arch"]>(/aarch64|arm64/i.test(navigator.userAgent) ? "aarch64" : "x86_64");
  useEffect(() => { api.downloads().then(setDownloads).catch(() => setDownloads(null)); }, []);
  const find = (platform: ClientDownload["platform"], kind: ClientDownload["kind"], which: ClientDownload["arch"] = "x86_64") =>
    downloads?.items.find((item) => item.platform === platform && item.kind === kind && item.arch === which);
  const size = (item: ClientDownload) => formatSize(item.size, language);
  const msi = find("windows", "msi"), zip = find("windows", "zip"), tar = find("linux", "tar", arch);
  const arches = (["x86_64", "aarch64"] as const).filter((which) => find("linux", "tar", which));
  const command = tar && `curl -fLO ${new URL(tar.url, window.location.href).href} && tar -xzf ${tar.file}`;
  return <div className="card install">
    <div className={`card-head ${downloads ? "with-version" : ""}`}>
      {downloads ? <div className="version-cover"><small>{t("home.version")}</small><b>{downloads.version}</b></div> : <span className="step">1</span>}
      <div className="grow"><h2>{t("home.install")}</h2><p>{downloads ? t("home.installText") : t("home.installAny")}</p></div>
      {downloads && <span className={`badge ${downloads.source === "server" ? "ok" : "accent"}`}><Icon name={downloads.source === "server" ? "shield" : "globe"} />{t(downloads.source === "server" ? "home.fromServer" : "home.fromGithub")}</span>}
    </div>
    {downloads === null && <div className="dl-row"><span className="os-tile"><Icon name="download" /></span>
      <div className="grow"><b>{t("home.noDownloadsTitle")}</b> <small>· {t("home.noDownloads")}</small></div>
      <a className="btn" href="https://github.com/arutar/lore/releases/latest" target="_blank" rel="noreferrer"><Icon name="globe" />{t("home.github")}</a></div>}
    {msi && <div className="dl-row">
      <span className="os-tile win"><Icon name="windows" /></span>
      <div className="grow"><b>Windows</b> <small>· {t("home.msiNote", { size: size(msi) })}</small></div>
      <div className="dl-actions">
        {zip && <a className="btn" href={zip.url} download={zip.file} title={`${zip.file} · ${size(zip)}`}>{t("home.zip")}</a>}
        <a className="btn primary" href={msi.url} download={msi.file}><Icon name="download" />{t("home.windows")}</a>
      </div>
    </div>}
    {tar && <div className="dl-row">
      <span className="os-tile linux"><Icon name="terminal" /></span>
      <div className="grow"><b>Linux</b> <small>· {t("home.linuxNote", { size: size(tar) })}</small></div>
      <div className="dl-actions">
        {arches.length > 1 && <div className="seg" role="group" aria-label={t("home.arch")}>{arches.map((which) =>
          <button key={which} className={arch === which ? "on" : ""} aria-pressed={arch === which} onClick={() => setArch(which)}>{which}</button>)}</div>}
        {command && <CopyTextButton text={command} label={t("home.curl")} message={t("home.commandCopied")} />}
        <a className="btn primary" href={tar.url} download={tar.file}><Icon name="download" />{t("home.linux")}</a>
      </div>
    </div>}
  </div>;
}

/** A labelled button that copies text, as the curl command. */
function CopyTextButton({ text, label, message }: { text: string; label: string; message: string }) {
  const copy = useCopy();
  const [done, setDone] = useState(false);
  return <button type="button" className="btn quiet" title={text} onClick={async () => { if (await copy(text, message)) { setDone(true); window.setTimeout(() => setDone(false), 1500); } }}>
    <Icon name={done ? "check" : "copy"} />{label}</button>;
}
