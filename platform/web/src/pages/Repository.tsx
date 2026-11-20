import { useCallback, useEffect, useRef, useState } from "react";
import { Link, NavLink, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { ApiError, api } from "../api";
import { useApp } from "../App";
import { useLocale } from "../i18n";
import type { ChangeKind, ChangeSummary, LastChange, Page, Repository, RepositoryLock, RepositoryTree, Revision, RevisionChange } from "../types";
import { Avatar, Badge, CopyButton, Dialog, Empty, ErrorNote, Icon, Loading, RepoTile, formatSize, useToast, useTopBar } from "../ui";
import { AccessTab } from "./Access";

type Tab = "files" | "history" | "locks" | "access" | "settings";

export function RepositoryPage() {
  const { id = "", tab = "files" } = useParams();
  const { t, ago, error } = useLocale();
  const { repositoryUrl, isAdmin } = useApp();
  const [repository, setRepository] = useState<Repository | null>(null);
  const [missing, setMissing] = useState(false);
  const [problem, setProblem] = useState("");
  const [canManage, setCanManage] = useState(false);

  const load = useCallback(async () => {
    try {
      setRepository(await api.repository(id));
      setMissing(false);
      setProblem("");
      // Only an owner may read the grants; the answer tells the page whether to offer them.
      api.grants(id).then(() => setCanManage(true)).catch(() => setCanManage(false));
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 404) setMissing(true);
      else setProblem(error(cause));
    }
  }, [id, error]);
  useEffect(() => { setRepository(null); setCanManage(false); void load(); }, [load]);

  const url = repository ? repositoryUrl(repository) : "";
  useTopBar({
    title: <span className="top-path"><Link to="/">{t("nav.repositories")}</Link>{repository && <><span className="sep">/</span>
      <span className="clone"><code title={url}>{url}</code><CopyButton small text={url} label={t("repo.copy")} message={t("repo.copied", { url })} /></span></>}</span>,
  }, [repository?.id, url, t]);

  if (missing) return <Empty icon="lock" title={t("repo.notFound")} text={t("repo.notFoundText")}><Link className="btn" to="/">{t("repo.toList")}</Link></Empty>;
  if (problem) return <ErrorNote message={problem} onRetry={() => void load()} />;
  if (!repository) return <Loading />;

  const tabs: [Tab, string, Parameters<typeof Icon>[0]["name"]][] = [
    ["files", t("repo.files"), "folder"], ["history", t("repo.history"), "history"], ["locks", t("repo.locks"), "lock"],
    ...(canManage ? [["access", t("repo.access"), "shield"] as [Tab, string, "shield"]] : []),
    ...(isAdmin ? [["settings", t("repo.settings"), "settings"] as [Tab, string, "settings"]] : []),
  ];
  return <>
    <div className="repo-hero">
      <RepoTile name={repository.name} large />
      <div className="grow">
        <div className="row wrap"><h1>{repository.name}</h1>
          {repository.public ? <Badge kind="ok" icon="globe">{t("list.public")}</Badge> : <Badge icon="lock">{t("list.private")}</Badge>}
          {canManage && <Badge kind="accent">{t("permission.owner")}</Badge>}
        </div>
        <p className="muted">{repository.description || t("repo.noDescription")}</p>
      </div>
      {/* Owner and last update sit at the right of the header, so the tabs follow the title. */}
      <div className="facts">
        {repository.ownerName && <span><Icon name="user" />{t("repo.owner", { name: repository.ownerName })}</span>}
        {repository.updatedAt && <span><Icon name="clock" />{t("repo.updated", { time: ago(repository.updatedAt) })}</span>}
      </div>
    </div>
    <nav className="tabs">{tabs.map(([key, label, icon]) => <NavLink key={key} to={`/repositories/${id}/${key}`} className={() => (tab === key ? "on" : "")}><Icon name={icon} />{label}</NavLink>)}</nav>
    {tab === "files" && <FilesTab id={id} />}
    {tab === "history" && <HistoryTab id={id} />}
    {tab === "locks" && <LocksTab id={id} />}
    {tab === "access" && canManage && <AccessTab repository={repository} onChanged={() => void load()} />}
    {tab === "settings" && isAdmin && <SettingsTab key={repository.id} repository={repository} onSaved={setRepository} />}
  </>;
}

// Who changed an entry last and when, in one line: the full message and date on hover.
function LastChangeCell({ change }: { change?: LastChange }) {
  const { t, ago, dateTime } = useLocale();
  if (!change) return <td className="change" />;
  const author = change.author || "?";
  const title = `${change.message || t("history.noMessage")}\n${t("files.revision", { number: String(change.number) })} · ${author} · ${dateTime(change.committedAt)}`;
  return <td className="change"><span className="change-cell" title={title}><Avatar small name={author} /><span className="who truncate">{author}</span><span className="when">{ago(change.committedAt)}</span></span></td>;
}

function FilesTab({ id }: { id: string }) {
  const { t, error, language } = useLocale();
  const [path, setPath] = useState("");
  const [tree, setTree] = useState<RepositoryTree | null>(null);
  const [problem, setProblem] = useState("");
  useEffect(() => {
    setTree(null);
    setProblem("");
    api.tree(id, path).then(setTree).catch((cause) => setProblem(error(cause)));
  }, [id, path, error]);
  const segments = path ? path.split("/") : [];
  const items = (tree?.items ?? []).filter((item) => item.path !== path).sort((a, b) => (a.type === b.type ? a.path.localeCompare(b.path) : a.type === "directory" ? -1 : 1));
  return <div className="card">
    <div className="card-head">
      <div className="crumbs grow"><button onClick={() => setPath("")}>{t("files.root")}</button>
        {segments.map((segment, index) => <span key={index}><span>/</span><button onClick={() => setPath(segments.slice(0, index + 1).join("/"))}>{segment}</button></span>)}</div>
      {tree?.revision && <Badge title={tree.revision}><span className="mono">{tree.revision.slice(0, 12)}</span></Badge>}
    </div>
    {problem ? <div className="pad"><ErrorNote message={problem} /></div>
      : !tree ? <Loading />
      : !tree.revision && items.length === 0 ? <Empty icon="folder" title={t("files.noRevisions")} text={t("files.noRevisionsText")} />
      : items.length === 0 ? <Empty icon="folder" title={t("files.empty")} text={t("files.emptyText")} />
      : <div className="table-wrap"><table className="table"><thead><tr><th>{t("files.name")}</th><th className="change">{t("files.lastChange")}</th><th className="num">{t("files.size")}</th><th /></tr></thead><tbody>
        {items.map((item) => {
          const name = item.path.slice(path ? path.length + 1 : 0);
          return item.type === "directory"
            ? <tr key={item.path} className="link" tabIndex={0} onClick={() => setPath(item.path)} onKeyDown={(event) => { if (event.key === "Enter") setPath(item.path); }}>
                <td><div className="cell-main"><Icon name="folder" className="file-ico dir" /><b>{name}</b></div></td><LastChangeCell change={item.lastChange} /><td className="num">—</td><td className="act"><Icon name="chevron" className="file-ico" /></td></tr>
            : <tr key={item.path}><td><div className="cell-main"><Icon name="file" className="file-ico" /><span>{name}</span></div></td>
                <LastChangeCell change={item.lastChange} />
                <td className="num">{formatSize(item.size, language)}</td>
                <td className="act"><a className="btn quiet sm" href={api.downloadUrl(id, item.path, tree.revision)}><Icon name="download" />{t("files.download")}</a></td></tr>;
        })}
      </tbody></table></div>}
  </div>;
}

function HistoryTab({ id }: { id: string }) {
  const { t, ago, dateTime, error } = useLocale();
  const [params, setParams] = useSearchParams();
  const selected = params.get("revision") ?? "";
  // Cursors of the newer pages, so the panel's "newer" arrow can step back across a page.
  const [trail, setTrail] = useState<string[]>([]);
  const [cursor, setCursor] = useState("");
  const [page, setPage] = useState<Page<Revision> | null>(null);
  const [latest, setLatest] = useState(0);
  const [problem, setProblem] = useState("");
  const pending = useRef<"first" | "last" | null>(null);
  const open = useCallback((signature: string) => setParams((current) => {
    const next = new URLSearchParams(current);
    if (signature) next.set("revision", signature); else next.delete("revision");
    return next;
  }, { replace: true }), [setParams]);
  const openRef = useRef(open);
  openRef.current = open;
  useEffect(() => {
    setPage(null);
    api.history(id, cursor).then((result) => {
      setPage(result);
      if (!cursor && result.items[0]) setLatest(result.items[0].number);
      const want = pending.current;
      pending.current = null;
      if (want && result.items.length > 0) openRef.current(result.items[want === "first" ? 0 : result.items.length - 1].signature);
    }).catch((cause) => setProblem(error(cause)));
  }, [id, cursor, error]);
  const older = () => { if (!page?.nextCursor) return; setTrail([...trail, cursor]); setCursor(page.nextCursor); };
  const newer = () => { if (trail.length === 0) return; setCursor(trail[trail.length - 1]); setTrail(trail.slice(0, -1)); };
  if (problem) return <ErrorNote message={problem} />;
  if (!page) return <Loading />;
  if (page.items.length === 0 && !cursor) return <div className="card"><Empty icon="history" title={t("history.empty")} text={t("history.emptyText")} /></div>;
  const index = page.items.findIndex((revision) => revision.signature === selected);
  return <>
    <div className="card">
      <div className="timeline">{page.items.map((revision) => {
        const author = revision.author || revision.committer;
        const isSelected = revision.signature === selected;
        return <div className={`rev clickable ${isSelected ? "sel" : ""}`} key={revision.signature} role="button" tabIndex={0} aria-pressed={isSelected}
          title={t("history.open")} onClick={() => open(revision.signature)}
          onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); open(revision.signature); } }}>
          <Avatar name={author || "?"} />
          <div className="grow"><div className="msg truncate">{revision.message || <span className="muted">{t("history.noMessage")}</span>}</div>
            <div className="sub" title={dateTime(revision.committedAt)}>{author} · {ago(revision.committedAt)}</div></div>
          <div className="row"><Badge>#{revision.number}</Badge>
            <span className="sig" title={revision.signature}><code>{revision.signature.slice(0, 12)}</code>
              <CopyButton small text={revision.signature} label={t("history.copyId")} message={t("history.copySignature")} /></span></div>
        </div>;
      })}</div>
      {(cursor || page.nextCursor) && <div className="table-foot">
        <span />{cursor && <button className="btn sm" onClick={() => { setTrail([]); setCursor(""); }}>{t("history.newest")}</button>}
        {page.nextCursor && <button className="btn sm" onClick={older}>{t("history.older")}</button>}
      </div>}
    </div>
    {selected && <RevisionPanel key={selected} id={id} signature={selected} revision={page.items[index]} latest={cursor ? 0 : latest}
      onNewer={index > 0 ? () => open(page.items[index - 1].signature) : index === 0 && trail.length > 0 ? () => { pending.current = "last"; newer(); } : undefined}
      onOlder={index >= 0 && index < page.items.length - 1 ? () => open(page.items[index + 1].signature) : index >= 0 && page.nextCursor ? () => { pending.current = "first"; older(); } : undefined}
      onClose={() => open("")} />}
  </>;
}

const changeLetter: Record<ChangeKind, string> = { added: "A", modified: "M", deleted: "D", moved: "R", copied: "C" };
const changeKinds: ChangeKind[] = ["added", "modified", "deleted", "moved", "copied"];
const changePage = 500;
const changeRow = 34;

/** The files a revision changed, beside the history. The list is read a page at a time as it
 * scrolls and only the rows in view are drawn, so a revision of hundreds of thousands of files
 * opens as fast as a small one. The kind filter and the search ask the server, so they cover the
 * whole revision. */
function RevisionPanel({ id, signature, revision, latest, onNewer, onOlder, onClose }: {
  id: string; signature: string; revision?: Revision; latest: number;
  onNewer?: () => void; onOlder?: () => void; onClose: () => void;
}) {
  const { t, ago, dateTime, error } = useLocale();
  const [summary, setSummary] = useState<ChangeSummary | null>(null);
  const [kind, setKind] = useState<ChangeKind | "">("");
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [items, setItems] = useState<RevisionChange[]>([]);
  const [next, setNext] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState("");
  const [scroll, setScroll] = useState(0);
  const [height, setHeight] = useState(600);
  const box = useRef<HTMLDivElement>(null);
  const loadingMore = useRef(false);

  useEffect(() => {
    const controller = new AbortController();
    api.changeSummary(id, signature, controller.signal).then(setSummary).catch(() => undefined);
    return () => controller.abort();
  }, [id, signature]);
  useEffect(() => {
    const timer = window.setTimeout(() => setQuery(search.trim()), 300);
    return () => window.clearTimeout(timer);
  }, [search]);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setProblem("");
    setItems([]);
    setNext(undefined);
    if (box.current) box.current.scrollTop = 0;
    setScroll(0);
    api.changes(id, signature, { kind: kind || undefined, q: query, limit: changePage }, controller.signal)
      .then((result) => { setItems(result.items); setNext(result.nextCursor); setLoading(false); })
      .catch((cause) => { if (!controller.signal.aborted) { setProblem(error(cause)); setLoading(false); } });
    return () => controller.abort();
  }, [id, signature, kind, query, error]);
  const more = useCallback(() => {
    if (!next || loadingMore.current) return;
    loadingMore.current = true;
    api.changes(id, signature, { kind: kind || undefined, q: query, cursor: next, limit: changePage })
      .then((result) => { setItems((current) => [...current, ...result.items]); setNext(result.nextCursor); })
      .catch((cause) => setProblem(error(cause)))
      .finally(() => { loadingMore.current = false; });
  }, [id, signature, kind, query, next, error]);
  useEffect(() => {
    const element = box.current;
    if (!element) return;
    const observer = new ResizeObserver(() => setHeight(element.clientHeight));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  // Keep reading while the loaded rows don't reach the bottom of the view.
  useEffect(() => {
    if (!loading && next && (items.length - 20) * changeRow < scroll + height) more();
  }, [loading, next, items.length, scroll, height, more]);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (event.key === "Escape") { onClose(); return; }
      if (target?.closest("input, textarea, select, [contenteditable=true]")) return;
      if (event.key === "ArrowLeft" && onNewer) { event.preventDefault(); onNewer(); }
      if (event.key === "ArrowRight" && onOlder) { event.preventDefault(); onOlder(); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onNewer, onOlder, onClose]);

  const kindName: Record<ChangeKind | "", string> = { "": t("changes.all"), added: t("changes.added"), modified: t("changes.modified"), deleted: t("changes.deleted"), moved: t("changes.moved"), copied: t("changes.copied") };
  const kindTitle: Record<ChangeKind, string> = { added: t("changes.kind.added"), modified: t("changes.kind.modified"), deleted: t("changes.kind.deleted"), moved: t("changes.kind.moved"), copied: t("changes.kind.copied") };
  const author = revision ? revision.author || revision.committer : "";
  const count = (value: ChangeKind | "") => summary ? (value ? summary[value] : summary.total) : undefined;
  const total = count(kind);
  const first = Math.max(0, Math.floor(scroll / changeRow) - 10);
  const last = Math.min(items.length, Math.ceil((scroll + height) / changeRow) + 10);
  return <aside className="rev-panel" aria-label={revision ? t("changes.title", { number: String(revision.number) }) : signature}>
    <div className="rev-panel-bar">
      <b>{revision ? t("changes.title", { number: String(revision.number) }) : <span className="mono">{signature.slice(0, 12)}</span>}</b>
      {revision && latest > 0 && <span className="muted small">{t("changes.of", { number: String(revision.number), total: String(latest) })}</span>}
      <span className="grow" />
      <span className="nav-pair">
        <button className="btn quiet icon sm" disabled={!onNewer} title={t("changes.newer")} aria-label={t("changes.newer")} onClick={onNewer}><Icon name="chevron" className="flip" /></button>
        <button className="btn quiet icon sm" disabled={!onOlder} title={t("changes.older")} aria-label={t("changes.older")} onClick={onOlder}><Icon name="chevron" /></button>
      </span>
      <CopyButton small text={window.location.href} label={t("changes.copyLink")} message={t("changes.linkCopied")} />
      <button className="btn quiet icon sm" title={t("changes.close")} aria-label={t("changes.close")} onClick={onClose}><Icon name="x" /></button>
    </div>
    <div className="rev-panel-head">
      {revision && <Avatar name={author || "?"} />}
      <div className="grow">
        {revision && <div className="msg">{revision.message || <span className="muted">{t("history.noMessage")}</span>}</div>}
        {revision && <div className="sub" title={dateTime(revision.committedAt)}>{author} · {ago(revision.committedAt)}</div>}
        <div className="sub">{summary && (summary.parent ? <>{t("changes.parent")} <code className="mono">{summary.parent.slice(0, 12)}</code></> : t("changes.first"))}</div>
      </div>
      <span className="sig" title={signature}><code>{signature.slice(0, 12)}</code>
        <CopyButton small text={signature} label={t("history.copyId")} message={t("history.copySignature")} /></span>
    </div>
    <div className="rev-panel-tools">
      <div className="chips">{(["", ...changeKinds] as (ChangeKind | "")[]).filter((value) => !value || !summary || summary[value] > 0 || kind === value).map((value) =>
        <button key={value || "all"} className={`chip ${value} ${kind === value ? "on" : ""}`} onClick={() => setKind(value)}>
          {kindName[value]}{count(value) !== undefined && <span className="n">{count(value)!.toLocaleString()}</span>}
        </button>)}</div>
      <label className="search sm"><Icon name="search" /><input className="input" value={search} placeholder={t("changes.filter")} aria-label={t("changes.filter")} onChange={(event) => setSearch(event.target.value)} /></label>
    </div>
    <div className="chg-list" ref={box} onScroll={(event) => setScroll(event.currentTarget.scrollTop)}>
      {problem ? <ErrorNote message={problem} />
        : loading ? <Loading />
        : items.length === 0 ? <Empty icon="file" title={query || kind ? t("changes.noMatch") : t("changes.empty")} />
        : <div style={{ height: items.length * changeRow, position: "relative" }}>
          {items.slice(first, last).map((change, offset) => {
            const slash = change.path.lastIndexOf("/");
            return <div className="chg-row" key={first + offset} style={{ top: (first + offset) * changeRow }}>
              <span className={`kind ${change.kind}`} title={kindTitle[change.kind]}>{changeLetter[change.kind]}</span>
              <span className="path truncate" title={change.from ? `${change.from} → ${change.path}` : change.path}>
                {change.from && <><span className="dir">{change.from}</span> <span className="muted">→</span> </>}
                <span className="dir">{change.path.slice(0, slash + 1)}</span><span className={`name ${change.kind}`}>{change.path.slice(slash + 1)}</span>
              </span>
              {change.kind !== "deleted" && <a className="btn quiet icon sm" href={api.downloadUrl(id, change.path, signature)} title={t("changes.download")} aria-label={t("changes.download")}><Icon name="download" /></a>}
            </div>;
          })}
        </div>}
    </div>
    {!loading && !problem && items.length > 0 && <div className="table-foot">
      <span>{t("changes.shown", { shown: items.length.toLocaleString(), total: (total ?? items.length).toLocaleString() })}</span>
    </div>}
  </aside>;
}

function LocksTab({ id }: { id: string }) {
  const { t, ago, dateTime, error } = useLocale();
  const toast = useToast();
  const [locks, setLocks] = useState<RepositoryLock[] | null>(null);
  const [canRelease, setCanRelease] = useState(false);
  const [problem, setProblem] = useState("");
  const [releasing, setReleasing] = useState<RepositoryLock | null>(null);
  const [busy, setBusy] = useState(false);
  const [releaseProblem, setReleaseProblem] = useState("");
  const load = useCallback(() => {
    api.locks(id).then((result) => { setLocks(result.items); setCanRelease(result.canRelease); }).catch((cause) => setProblem(error(cause)));
  }, [id, error]);
  useEffect(load, [load]);
  const release = async () => {
    if (!releasing) return;
    setBusy(true);
    try {
      await api.releaseLock(id, releasing);
      toast(t("locks.released", { path: releasing.path }));
      setReleasing(null);
      setReleaseProblem("");
      load();
    } catch (cause) {
      setReleaseProblem(error(cause));
    } finally {
      setBusy(false);
    }
  };
  if (problem) return <ErrorNote message={problem} />;
  if (!locks) return <Loading />;
  return <div className="card">{locks.length === 0 ? <Empty icon="lock" title={t("locks.empty")} text={t("locks.emptyText")} />
    : <div className="table-wrap"><table className="table"><thead><tr><th>{t("locks.path")}</th><th>{t("locks.owner")}</th><th className="num">{t("locks.when")}</th>{canRelease && <th />}</tr></thead><tbody>
      {locks.map((lock) => <tr key={`${lock.branchId}:${lock.path}`}><td><div className="cell-main"><Icon name="lock" className="file-ico" /><span className="mono">{lock.path}</span></div></td>
        <td><div className="row"><Avatar name={lock.owner} />{lock.owner}{lock.ownerDisabled && <Badge kind="warn" title={t("locks.ownerGoneHint")}>{t("locks.ownerGone")}</Badge>}</div></td>
        <td className="num" title={dateTime(lock.lockedAt)}>{ago(lock.lockedAt)}</td>
        {canRelease && <td className="act"><button className="btn danger sm" onClick={() => { setReleaseProblem(""); setReleasing(lock); }}><Icon name="unlock" />{t("locks.release")}</button></td>}</tr>)}
    </tbody></table></div>}
    {releasing && <Dialog title={t("locks.releaseTitle")} onClose={() => setReleasing(null)} footer={<>
      <button type="button" className="btn" autoFocus onClick={() => setReleasing(null)}>{t("common.cancel")}</button>
      <button type="button" className="btn danger solid" disabled={busy} onClick={() => void release()}>{t("locks.releaseConfirm")}</button>
    </>}>
      <ErrorNote message={releaseProblem} />
      <p>{t("locks.releaseText", { path: releasing.path, owner: releasing.owner })}</p>
      <p className="muted">{t("locks.releaseWarning", { owner: releasing.owner })}</p>
    </Dialog>}
  </div>;
}

function SettingsTab({ repository, onSaved }: { repository: Repository; onSaved: (repository: Repository) => void }) {
  const { t, error } = useLocale();
  const { refreshRepositories } = useApp();
  const toast = useToast();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const closeDelete = useCallback(() => { if (!deleting) setConfirming(false); }, [deleting]);
  const remove = async () => {
    if (confirmation !== repository.name || deleting) return;
    setDeleting(true);
    setDeleteError("");
    try {
      await api.deleteRepository(repository.id, confirmation);
      refreshRepositories();
      navigate("/", { replace: true });
      toast(t("repoSettings.deleted", { name: repository.name }));
    } catch (cause) {
      setDeleteError(error(cause));
      setDeleting(false);
    }
  };
  const [name, setName] = useState(repository.name);
  const [description, setDescription] = useState(repository.description);
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState("");
  const save = async (event: React.FormEvent) => {
    event.preventDefault();
    setSaving(true);
    try {
      // Visibility is left out on purpose: it's managed on the Access tab.
      const saved = await api.updateRepository(repository.id, { name: name.trim(), description: description.trim() });
      onSaved(saved);
      refreshRepositories();
      toast(t("repoSettings.saved"));
      setProblem("");
    } catch (cause) {
      setProblem(error(cause));
    } finally {
      setSaving(false);
    }
  };
  return <div className="stack"><form className="card narrow" onSubmit={(event) => void save(event)}>
    <div className="card-head"><h2 className="grow">{t("repoSettings.main")}</h2></div>
    <div className="form">
      <ErrorNote message={problem} />
      <label className="field">{t("repoSettings.name")}<input className="input mono" required value={name} onChange={(event) => setName(event.target.value)} /><span className="hint">{t("repoSettings.nameHint")}</span></label>
      <label className="field">{t("repoSettings.description")}<input className="input" value={description} onChange={(event) => setDescription(event.target.value)} /></label>
      <div><button className="btn primary" disabled={saving || (name === repository.name && description === repository.description)}>{t("common.save")}</button></div>
    </div>
  </form>
    <section className="card narrow danger-zone">
      <div className="card-head"><h2>{t("repoSettings.danger")}</h2></div>
      <div className="setting-row"><div className="grow"><h3>{t("repoSettings.remove")}</h3><p>{t("repoSettings.consequence")}</p></div>
        <button className="btn danger" disabled={saving} onClick={() => { setConfirmation(""); setDeleteError(""); setConfirming(true); }}>{t("repoSettings.remove")}</button></div>
    </section>
    {confirming && <Dialog title={t("repoSettings.confirmTitle", { name: repository.name })} onClose={closeDelete} footer={<>
      <button className="btn" disabled={deleting} onClick={closeDelete}>{t("common.cancel")}</button>
      <button className="btn danger solid" disabled={deleting || confirmation !== repository.name} onClick={() => void remove()}>{t("repoSettings.remove")}</button>
    </>}>
      <ErrorNote message={deleteError} />
      <p>{t("repoSettings.consequence")}</p>
      <label className="field">{t("repoSettings.typeName", { name: repository.name })}<input className="input mono" value={confirmation} disabled={deleting} onChange={event => setConfirmation(event.target.value)} autoComplete="off" /></label>
    </Dialog>}
  </div>;
}
