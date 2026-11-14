import { useCallback, useEffect, useState } from "react";
import { Link, NavLink, useParams } from "react-router-dom";
import { ApiError, api } from "../api";
import { useApp } from "../App";
import { useLocale } from "../i18n";
import type { LastChange, Repository, RepositoryLock, RepositoryTree, Revision } from "../types";
import { Avatar, Badge, CopyButton, Dialog, Empty, ErrorNote, Icon, Loading, RepoTile, formatSize, useCopy, useToast, useTopBar } from "../ui";
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
    {tab === "settings" && isAdmin && <SettingsTab repository={repository} onSaved={setRepository} />}
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
  const copy = useCopy();
  const [cursor, setCursor] = useState("");
  const [page, setPage] = useState<{ items: Revision[]; nextCursor?: string } | null>(null);
  const [problem, setProblem] = useState("");
  useEffect(() => {
    setPage(null);
    api.history(id, cursor).then(setPage).catch((cause) => setProblem(error(cause)));
  }, [id, cursor, error]);
  if (problem) return <ErrorNote message={problem} />;
  if (!page) return <Loading />;
  if (page.items.length === 0 && !cursor) return <div className="card"><Empty icon="history" title={t("history.empty")} text={t("history.emptyText")} /></div>;
  return <div className="card">
    <div className="timeline">{page.items.map((revision) => {
      const author = revision.author || revision.committer;
      return <div className="rev" key={revision.signature}>
        <Avatar name={author || "?"} />
        <div className="grow"><div className="msg truncate">{revision.message || <span className="muted">{t("history.noMessage")}</span>}</div>
          <div className="sub" title={dateTime(revision.committedAt)}>{author} · {ago(revision.committedAt)}</div></div>
        <div className="row"><Badge>#{revision.number}</Badge>
          <button className="btn quiet sm mono" title={revision.signature} onClick={() => void copy(revision.signature, t("history.copySignature"))}>{revision.signature.slice(0, 12)}</button></div>
      </div>;
    })}</div>
    {(cursor || page.nextCursor) && <div className="table-foot">
      <span />{cursor && <button className="btn sm" onClick={() => setCursor("")}>{t("history.newest")}</button>}
      {page.nextCursor && <button className="btn sm" onClick={() => setCursor(page.nextCursor!)}>{t("history.older")}</button>}
    </div>}
  </div>;
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
  return <form className="card narrow" onSubmit={(event) => void save(event)}>
    <div className="card-head"><h2 className="grow">{t("repoSettings.main")}</h2></div>
    <div className="form">
      <ErrorNote message={problem} />
      <label className="field">{t("repoSettings.name")}<input className="input mono" required value={name} onChange={(event) => setName(event.target.value)} /><span className="hint">{t("repoSettings.nameHint")}</span></label>
      <label className="field">{t("repoSettings.description")}<input className="input" value={description} onChange={(event) => setDescription(event.target.value)} /></label>
      <div><button className="btn primary" disabled={saving || (name === repository.name && description === repository.description)}>{t("common.save")}</button></div>
    </div>
  </form>;
}
