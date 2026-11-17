import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../api";
import { useLocale } from "../i18n";
import type { GrantSubject, LabeledGrant, Permission, Repository, SubjectType } from "../types";
import { Avatar, ErrorNote, Icon, Loading, Switch, useToast } from "../ui";

const permissions: Permission[] = ["read", "write", "owner"];

export function AccessTab({ repository, onChanged }: { repository: Repository; onChanged: () => void }) {
  const { t, error } = useLocale();
  const toast = useToast();
  const [grants, setGrants] = useState<LabeledGrant[] | null>(null);
  const [problem, setProblem] = useState("");

  const load = useCallback(async () => {
    try {
      setGrants((await api.grants(repository.id)).items);
      setProblem("");
    } catch (cause) {
      setProblem(error(cause));
    }
  }, [repository.id, error]);
  useEffect(() => { void load(); }, [load]);

  const run = async (action: () => Promise<unknown>, message?: string, undo?: () => Promise<unknown>) => {
    try {
      await action();
      await load();
      onChanged();
      if (message) toast(message, undo ? { undo: () => void run(undo) } : undefined);
    } catch (cause) {
      toast(error(cause), { error: true });
    }
  };

  if (problem) return <ErrorNote message={problem} onRetry={() => void load()} />;
  if (!grants) return <Loading />;

  const general = (type: SubjectType) => grants.some((grant) => grant.subjectType === type);
  const setGeneral = (type: "anonymous" | "authenticated", on: boolean) => {
    const grant = { repositoryId: repository.id, subjectType: type, subject: "", permission: "read" as Permission };
    void run(() => (on ? api.setGrant(grant) : api.removeGrant(grant)));
  };
  const named = grants.filter((grant) => grant.subjectType === "user" || grant.subjectType === "group");
  const owners = named.filter((grant) => grant.permission === "owner").length;
  const permissionLabel = (permission: Permission) => t(`permission.${permission}`);

  return <div className="access-grid">
    <div className="stack">
      <div className="card">
        <div className="card-head"><div className="grow"><h2>{t("access.general")}</h2><p>{t("access.generalText")}</p></div></div>
        <div className="setting-row"><span className="repo-tile" style={{ "--tone": "var(--ok)" } as React.CSSProperties}><Icon name="globe" /></span>
          <div className="grow"><h3>{t("access.public")}</h3><p>{t("access.publicText")}</p></div>
          <Switch checked={general("anonymous")} onChange={(on) => setGeneral("anonymous", on)} label={t("access.public")} /></div>
        <div className="setting-row"><span className="repo-tile" style={{ "--tone": "var(--accent)" } as React.CSSProperties}><Icon name="users" /></span>
          {/* Public read covers signed-in users too, so the switch shows that and can't be changed
              while the repository is public; its own grant is kept as it was for when it isn't. */}
          <div className="grow"><h3>{t("access.authenticated")}</h3><p>{t(general("anonymous") ? "access.authenticatedByPublic" : "access.authenticatedText")}</p></div>
          <Switch checked={general("anonymous") || general("authenticated")} disabled={general("anonymous")} onChange={(on) => setGeneral("authenticated", on)} label={t("access.authenticated")} /></div>
      </div>
      <div className="card">
        <div className="card-head"><div className="grow"><h2>{t("access.subjects")}</h2><p>{t("access.subjectsText")}</p></div>
          <BulkButtons repository={repository} onImported={() => void load()} /></div>
        <SubjectPicker repository={repository} onGrant={(type, subject, label, permission) =>
          run(() => api.setGrant({ repositoryId: repository.id, subjectType: type, subject, permission }), t("access.granted", { name: label, permission: permissionLabel(permission) }))} />
        <div className="table-wrap"><table className="table"><tbody>
          {named.map((grant) => {
            const lastOwner = grant.permission === "owner" && owners === 1;
            return <tr key={`${grant.subjectType}:${grant.subject}`}>
              <td><div className="cell-main">
                {grant.subjectType === "user" ? <Avatar name={grant.label} /> : <span className="avatar sq" style={{ "--tone": "var(--accent-2)" } as React.CSSProperties}><Icon name="users" /></span>}
                <div className="grow"><b>{grant.label}</b><small className="truncate">{grant.detail}</small></div></div></td>
              <td className="act"><div className="row">
                <select className="select sm" value={grant.permission} disabled={lastOwner} title={lastOwner ? t("access.lastOwner") : undefined} aria-label={grant.label}
                  onChange={(event) => { const permission = event.target.value as Permission; void run(() => api.setGrant({ ...grant, permission }), t("access.granted", { name: grant.label, permission: permissionLabel(permission) })); }}>
                  {permissions.map((permission) => <option key={permission} value={permission}>{permissionLabel(permission)}</option>)}
                </select>
                <button className="btn quiet icon sm" disabled={lastOwner} title={lastOwner ? t("access.lastOwner") : t("access.revoke")} aria-label={`${t("access.revoke")}: ${grant.label}`}
                  onClick={() => void run(() => api.removeGrant(grant), t("access.revoked", { name: grant.label }), () => api.setGrant(grant))}><Icon name="x" /></button>
              </div></td>
            </tr>;
          })}
        </tbody></table></div>
      </div>
    </div>
    <aside className="stack">
      <div className="card pad"><h2>{t("access.legend")}</h2><div className="legend">
        <div><b>{t("permission.read")}</b><span>{t("access.read")}</span></div>
        <div><b>{t("permission.write")}</b><span>{t("access.write")}</span></div>
        <div><b>{t("permission.owner")}</b><span>{t("access.ownerText")}</span></div></div></div>
      <div className="note"><Icon name="info" /><span>{t("access.note")}</span></div>
    </aside>
  </div>;
}

/** Search-as-you-type for users and groups; an exact username, email or group name can be
 *  typed and granted as is, the server resolves it. */
function SubjectPicker({ repository, onGrant }: { repository: Repository; onGrant: (type: SubjectType, subject: string, label: string, permission: Permission) => Promise<void> }) {
  const { t } = useLocale();
  const [text, setText] = useState("");
  const [permission, setPermission] = useState<Permission>("read");
  const [open, setOpen] = useState(false);
  const [suggestions, setSuggestions] = useState<GrantSubject[]>([]);
  const [active, setActive] = useState(0);
  const box = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const query = text.trim();
    if (query.length < 2) { setSuggestions([]); return; }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      api.grantSubjects(repository.id, query, controller.signal).then((result) => { setSuggestions(result.items); setActive(0); }).catch(() => {});
    }, 200);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [text, repository.id]);
  useEffect(() => {
    const close = (event: MouseEvent) => { if (!box.current?.contains(event.target as Node)) setOpen(false); };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, []);

  const pick = async (subject: GrantSubject) => {
    setOpen(false);
    setText("");
    await onGrant(subject.type, subject.id, subject.label, permission);
  };
  const grantTyped = async () => {
    const typed = text.trim();
    if (!typed) return;
    const exact = suggestions.find((item) => [item.value, item.label, item.id].some((value) => value.toLowerCase() === typed.toLowerCase()));
    if (exact) return pick(exact);
    setOpen(false);
    setText("");
    // A group name without spaces or "@" is ambiguous; users are the common case.
    await onGrant("user", typed, typed, permission);
  };
  const users = suggestions.filter((item) => item.type === "user");
  const groups = suggestions.filter((item) => item.type === "group");
  const ordered = [...users, ...groups];
  return <div className="setting-row picker" ref={box}>
    <div className="combo">
      <label className="search"><Icon name="search" /><input className="input" value={text} autoComplete="off" placeholder={t("access.searchPlaceholder")} aria-label={t("access.searchPlaceholder")}
        onFocus={() => setOpen(true)} onChange={(event) => { setText(event.target.value); setOpen(true); }}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown") { event.preventDefault(); setActive((index) => Math.min(index + 1, ordered.length - 1)); }
          if (event.key === "ArrowUp") { event.preventDefault(); setActive((index) => Math.max(index - 1, 0)); }
          if (event.key === "Escape") setOpen(false);
          if (event.key === "Enter") { event.preventDefault(); void (open && ordered[active] ? pick(ordered[active]) : grantTyped()); }
        }} /></label>
      {open && text.trim().length >= 2 && <div className="combo-list" role="listbox">
        {ordered.length === 0 ? <div className="empty small"><p>{t("access.noMatches")}</p></div> : <>
          {users.length > 0 && <div className="combo-group">{t("access.users")}</div>}
          {users.map((item) => <button key={item.id} className={`combo-item ${ordered[active] === item ? "on" : ""}`} onMouseDown={(event) => event.preventDefault()} onClick={() => void pick(item)}>
            <Avatar name={item.label} /><span className="grow"><b>{item.label}</b><small>{item.detail}</small></span></button>)}
          {groups.length > 0 && <div className="combo-group">{t("access.groups")}</div>}
          {groups.map((item) => <button key={item.id} className={`combo-item ${ordered[active] === item ? "on" : ""}`} onMouseDown={(event) => event.preventDefault()} onClick={() => void pick(item)}>
            <span className="avatar sq" style={{ "--tone": "var(--accent-2)" } as React.CSSProperties}><Icon name="users" /></span><span className="grow"><b>{item.label}</b><small>{item.detail}</small></span></button>)}
        </>}
      </div>}
    </div>
    <select className="select" style={{ width: 140 }} value={permission} onChange={(event) => setPermission(event.target.value as Permission)} aria-label={t("users.role")}>
      {permissions.map((value) => <option key={value} value={value}>{t(`permission.${value}`)}</option>)}
    </select>
    <button className="btn primary" disabled={!text.trim()} onClick={() => void grantTyped()}>{t("access.add")}</button>
  </div>;
}

function BulkButtons({ repository, onImported }: { repository: Repository; onImported: () => void }) {
  const { t, error } = useLocale();
  const toast = useToast();
  const upload = async (file?: File) => {
    if (!file) return;
    try {
      const result = await api.importGrants(repository.id, await file.text(), file.name.toLowerCase().endsWith(".csv"));
      toast(t("users.importResult", { imported: result.imported, errors: result.errors.length }), { error: result.errors.length > 0 });
      onImported();
    } catch (cause) {
      toast(error(cause), { error: true });
    }
  };
  return <div className="row">
    <a className="btn sm" href={api.grantsExportUrl(repository.id, true)} download><Icon name="download" />{t("users.export")}</a>
    <label className="btn sm"><Icon name="upload" />{t("users.import")}<input hidden type="file" accept=".csv,.json" onChange={(event) => { void upload(event.target.files?.[0]); event.target.value = ""; }} /></label>
  </div>;
}
