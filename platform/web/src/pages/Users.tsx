import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import { useApp } from "../App";
import { useLocale } from "../i18n";
import type { AccessEntry, GrantSubject, Members, Role, User } from "../types";
import { Avatar, Badge, Dialog, Empty, ErrorNote, Icon, Loading, Switch, useToast, useTopBar } from "../ui";

/** One row of the list: a local account, a directory account added personally or a group. */
type Row =
  | { kind: "local"; key: string; name: string; search: string; user: User }
  | { kind: "account"; key: string; name: string; search: string; entry: AccessEntry; user?: User; viaGroups: string[] }
  | { kind: "group"; key: string; name: string; search: string; entry: AccessEntry; members: User[] };

const userName = (user: User) => user.displayName || user.username;
const userDetail = (user: User) => user.email && user.email !== user.username ? `${user.username} · ${user.email}` : user.username;

/** Users and groups: the one list of who may use Lore. Whoever is on it may sign in, with
 *  the role it gives; whoever isn't, can't. A group admits its members (nested groups too). */
export function UsersPage() {
  const { t, error, ago } = useLocale();
  const { session, config } = useApp();
  const toast = useToast();
  const [data, setData] = useState<Members | null>(null);
  const [problem, setProblem] = useState("");
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState<Set<string>>(new Set());
  const [adding, setAdding] = useState(false);

  const load = useCallback(async () => {
    try {
      setData(await api.members());
      setProblem("");
    } catch (cause) {
      setProblem(error(cause));
    }
  }, [error]);
  useEffect(() => { void load(); }, [load]);

  const upload = async (file?: File) => {
    if (!file) return;
    try {
      const result = await api.importUsers(await file.text(), file.name.toLowerCase().endsWith(".csv"));
      toast(t("users.importResult", { imported: result.imported, errors: result.errors.length }), { error: result.errors.length > 0 });
      void load();
    } catch (cause) {
      toast(error(cause), { error: true });
    }
  };
  useTopBar({
    title: t("nav.users"), description: t("users.description"),
    actions: <>
      <a className="btn sm" href={api.usersExportUrl(true)} download><Icon name="download" />{t("users.export")}</a>
      <label className="btn sm" title={t("users.importHint")}><Icon name="upload" />{t("users.import")}<input hidden type="file" accept=".csv,.json" onChange={(event) => { void upload(event.target.files?.[0]); event.target.value = ""; }} /></label>
      <button className="btn primary sm" onClick={() => setAdding(true)}><Icon name="plus" />{t("users.create")}</button>
    </>,
  }, [t]);

  const run = async (action: () => Promise<unknown>, done: string) => {
    try {
      await action();
      toast(done);
      await load();
    } catch (cause) {
      toast(error(cause), { error: true });
    }
  };

  const rows = useMemo<Row[]>(() => {
    if (!data) return [];
    const groupNames = new Map(data.groups.map((group) => [group.subject.toLowerCase(), group.name]));
    const list: Row[] = [
      ...data.groups.map((entry): Row => ({ kind: "group", key: `g:${entry.subject}`, name: entry.name, search: `${entry.name} ${entry.detail ?? ""}`, entry, members: entry.members })),
      ...data.accounts.map((entry): Row => ({
        kind: "account", key: `a:${entry.subject}`, name: entry.user ? userName(entry.user) : entry.name,
        search: `${entry.name} ${entry.detail ?? ""} ${entry.user ? userDetail(entry.user) : ""}`, entry, user: entry.user,
        viaGroups: (entry.user?.groups ?? []).map((id) => groupNames.get(id.toLowerCase())).filter((name): name is string => Boolean(name)),
      })),
      ...data.local.map((user): Row => ({ kind: "local", key: `l:${user.id}`, name: userName(user), search: `${userName(user)} ${userDetail(user)}`, user })),
    ];
    return list.sort((a, b) => (a.kind === "group" ? 0 : 1) - (b.kind === "group" ? 0 : 1) || a.name.localeCompare(b.name));
  }, [data]);

  const needle = query.trim().toLowerCase();
  const matches = (text: string) => !needle || text.toLowerCase().includes(needle);
  const visible = rows.filter((row) => matches(row.search) || (row.kind === "group" && row.members.some((member) => matches(`${userName(member)} ${userDetail(member)}`))));
  const personal = new Set((data?.accounts ?? []).map((entry) => entry.subject.toLowerCase()));
  const listed = new Set([...personal, ...(data?.groups ?? []).map((entry) => entry.subject.toLowerCase())]);
  const toggle = (key: string) => setOpen((current) => { const next = new Set(current); if (next.has(key)) next.delete(key); else next.add(key); return next; });

  const roleSelect = (role: Role, onChange: (role: Role) => void, disabled = false, title?: string) =>
    <select className="select sm" value={role} disabled={disabled} title={title} aria-label={t("users.role")} onChange={(event) => onChange(event.target.value as Role)}>
      <option value="user">{t("users.roleUser")}</option><option value="admin">{t("users.roleAdmin")}</option></select>;
  const remove = (entry: AccessEntry) => <button className="btn quiet icon sm" title={t("members.remove", { name: entry.name })} aria-label={t("members.remove", { name: entry.name })}
    onClick={() => { if (window.confirm(t("members.removeConfirm", { name: entry.name }))) void run(() => api.removeAccess(entry.subjectType, entry.subject), t("access.dirRemoved", { name: entry.name })); }}><Icon name="x" /></button>;
  const entryControls = (entry: AccessEntry) => <>
    <td>{roleSelect(entry.role, (role) => void run(() => api.updateAccess(entry.subjectType, entry.subject, role, entry.disabled), t("users.updated", { name: entry.name })))}</td>
    <td className="act"><span className="row-actions"><Switch checked={!entry.disabled} label={t("users.active")} onChange={(active) => void run(() => api.updateAccess(entry.subjectType, entry.subject, entry.role, !active), t("users.updated", { name: entry.name }))} />{remove(entry)}</span></td>
  </>;

  return <>
    {config.adEnabled && <DirectorySearch listed={listed} onAdded={() => void load()} />}
    <div className="toolbar">
      <label className="search"><Icon name="search" /><input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("members.search")} aria-label={t("members.search")} /></label>
    </div>
    <ErrorNote message={problem} onRetry={() => void load()} />
    {!data ? <Loading /> : <div className="card table-wrap">
      <table className="table members stacked"><thead><tr><th>{t("members.who")}</th><th>{t("users.source")}</th><th>{t("users.role")}</th><th className="act">{t("users.active")}</th></tr></thead>
        <tbody>{visible.length === 0 ? <tr><td colSpan={4}><Empty icon="search" title={t("users.empty")} /></td></tr> : visible.map((row) => {
          if (row.kind === "group") {
            const expanded = open.has(row.key) || (needle !== "" && !matches(row.search));
            const members = row.members.filter((member) => !needle || matches(row.search) || matches(`${userName(member)} ${userDetail(member)}`));
            return [<tr key={row.key} className={row.entry.disabled ? "off" : ""}>
              <td><button className="cell-main expander" onClick={() => toggle(row.key)} aria-expanded={expanded}>
                <Icon name="chevron" className={expanded ? "turned" : ""} />
                <span className="avatar sq" style={{ "--tone": "var(--accent-2)" } as React.CSSProperties}><Icon name="users" /></span>
                <span className="grow"><b>{row.name}</b><small className="truncate">{t("members.groupSignedIn", { count: row.members.length })}</small></span></button></td>
              <td><Badge kind="accent">{t("members.adGroup")}</Badge></td>
              {entryControls(row.entry)}
            </tr>,
            ...(expanded ? (members.length === 0 ? [<tr key={row.key + ":none"} className="member"><td colSpan={4}><small className="muted">{t("members.noneSignedIn")}</small></td></tr>]
              : members.map((member) => {
                const own = member.directoryId ? personal.has(member.directoryId.toLowerCase()) : false;
                return <tr key={row.key + ":" + member.id} className="member">
                  <td><div className="cell-main"><Avatar name={userName(member)} /><div className="grow"><b>{userName(member)}{member.id === session.user?.id && <span className="badge inline">{t("users.you")}</span>}</b>
                    <small className="truncate">{userDetail(member)} · {t("members.lastSeen", { when: ago(member.updatedAt) })}</small></div></div></td>
                  <td colSpan={2}>{own ? <Badge>{t("members.hasPersonal")}</Badge> : member.directoryDisabled ? <Badge kind="danger">{t("users.disabledInAd")}</Badge> : <small className="muted">{t("members.viaGroup", { group: row.name })}</small>}</td>
                  <td className="act">{!own && member.directoryId && <button className="btn sm" title={t("members.personalHint")}
                    onClick={() => void run(() => api.addPersonalAccess(member.directoryId!, "user", false), t("members.personalAdded", { name: userName(member) }))}>{t("members.personal")}</button>}</td>
                </tr>;
              })) : []),
            ];
          }
          if (row.kind === "account") {
            const self = row.user?.id === session.user?.id;
            return <tr key={row.key} className={row.entry.disabled ? "off" : ""}>
              <td><div className="cell-main"><Avatar name={row.name} /><div className="grow"><b>{row.name}{self && <span className="badge inline">{t("users.you")}</span>}</b>
                <small className="truncate">{row.user ? userDetail(row.user) : row.entry.detail}{!row.user && ` · ${t("members.notSignedIn")}`}{row.viaGroups.length > 0 && ` · ${t("members.alsoVia", { groups: row.viaGroups.join(", ") })}`}</small></div></div></td>
              <td><Badge kind="accent">{t("users.sourceAd")}</Badge>{row.user?.directoryDisabled && <div><Badge kind="danger">{t("users.disabledInAd")}</Badge></div>}</td>
              {entryControls(row.entry)}
            </tr>;
          }
          const self = row.user.id === session.user?.id;
          const update = (change: Partial<Pick<User, "role" | "disabled">>) => void run(() => api.updateUser({ id: row.user.id, role: change.role ?? row.user.role, disabled: change.disabled ?? row.user.disabled }), t("users.updated", { name: row.name }));
          return <tr key={row.key} className={row.user.disabled ? "off" : ""}>
            <td><div className="cell-main"><Avatar name={row.name} /><div className="grow"><b>{row.name}{self && <span className="badge inline">{t("users.you")}</span>}</b><small className="truncate">{userDetail(row.user)}</small></div></div></td>
            <td><Badge>{t("users.sourceLocal")}</Badge></td>
            <td>{roleSelect(row.user.role, (role) => update({ role }), self, self ? t("users.selfRole") : undefined)}</td>
            <td className="act"><span className="row-actions"><Switch checked={!row.user.disabled} disabled={self} label={self ? t("users.selfDisable") : t("users.active")} onChange={(active) => update({ disabled: !active })} />
              {self ? <span className="act-spacer" aria-hidden="true" /> : <button className="btn quiet icon sm" title={t("members.deleteLocal", { name: row.name })} aria-label={t("members.deleteLocal", { name: row.name })}
                onClick={() => { if (window.confirm(t("members.deleteLocalConfirm", { name: row.name }))) void run(() => api.deleteUser(row.user.id), t("members.deletedLocal", { name: row.name })); }}><Icon name="x" /></button>}</span></td>
          </tr>;
        })}</tbody></table>
    </div>}
    {adding && <CreateDialog onClose={() => setAdding(false)} onCreated={() => void load()} />}
  </>;
}

/** Access from Active Directory: find a person or a group and put them on the list. */
function DirectorySearch({ listed, onAdded }: { listed: Set<string>; onAdded: () => void }) {
  const { t, error } = useLocale();
  const toast = useToast();
  const [text, setText] = useState("");
  const [open, setOpen] = useState(false);
  const [found, setFound] = useState<GrantSubject[] | null>(null);
  const [saving, setSaving] = useState(false);
  const box = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const query = text.trim();
    setFound(null);
    if (query.length < 2) return;
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      api.accessSubjects(query, controller.signal).then((result) => setFound(result.items)).catch((cause) => { if (!controller.signal.aborted) toast(error(cause), { error: true }); });
    }, 250);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [text, error, toast]);
  useEffect(() => {
    const close = (event: MouseEvent) => { if (!box.current?.contains(event.target as Node)) setOpen(false); };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, []);

  const add = async (subject: GrantSubject) => {
    setSaving(true);
    try {
      // A person is also linked in the identity provider right away, so repository rights can
      // be given before their first sign-in; a group is simply put on the list.
      if (subject.type === "user") await api.provisionADUser(subject.value);
      else await api.addAccess("group", subject.id);
      toast(t("access.dirAdded", { name: subject.label }));
      setText("");
      setOpen(false);
      onAdded();
    } catch (cause) {
      toast(error(cause), { error: true });
    } finally {
      setSaving(false);
    }
  };
  const groups = (found ?? []).filter((item) => item.type === "group");
  const people = (found ?? []).filter((item) => item.type === "user");
  const choice = (item: GrantSubject) => {
    const already = listed.has(item.id.toLowerCase());
    return <button key={item.type + item.id} type="button" className="combo-item" disabled={saving || already} onMouseDown={(event) => event.preventDefault()} onClick={() => void add(item)}>
      {item.type === "group" ? <span className="avatar sq" style={{ "--tone": "var(--accent-2)" } as React.CSSProperties}><Icon name="users" /></span> : <Avatar name={item.label} />}
      <span className="grow"><b>{item.label}</b><small>{item.detail}</small></span>{already ? <Badge>{t("members.listed")}</Badge> : <Icon name="plus" />}</button>;
  };
  return <section className="card pad dir-access">
    <div><h3>{t("access.dirTitle")}</h3><p className="muted small">{t("access.dirText")}</p></div>
    <div className="combo" ref={box}>
      <label className="search"><Icon name="search" /><input className="input" value={text} autoComplete="off" placeholder={t("access.dirSearch")} aria-label={t("access.dirSearch")}
        onFocus={() => setOpen(true)} onChange={(event) => { setText(event.target.value); setOpen(true); }} onKeyDown={(event) => { if (event.key === "Escape") setOpen(false); }} /></label>
      {open && text.trim().length >= 2 && <div className="combo-list" role="listbox">
        {found === null ? <div className="empty small"><p>{t("app.loading")}</p></div>
          : found.length === 0 ? <div className="empty small"><p>{t("access.dirNoMatches")}</p></div>
          : <>{groups.length > 0 && <div className="combo-group">{t("access.groups")}</div>}{groups.map(choice)}
              {people.length > 0 && <div className="combo-group">{t("access.users")}</div>}{people.map(choice)}</>}
      </div>}
    </div>
  </section>;
}

/** Creates a local account (email and password); it's on the list as soon as it exists. */
function CreateDialog({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { t, error } = useLocale();
  const toast = useToast();
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("user");
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState("");
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setSaving(true);
    setProblem("");
    try {
      const user = await api.createUser({ email: email.trim(), displayName: displayName.trim(), password, role });
      toast(t("users.added", { name: user.displayName || user.username }));
      onCreated();
      onClose();
    } catch (cause) {
      setProblem(error(cause));
    } finally {
      setSaving(false);
    }
  };
  return <form onSubmit={(event) => void submit(event)}>
    <Dialog title={t("members.createTitle")} onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>{t("common.cancel")}</button>
      <button className="btn primary" disabled={saving || !email || !displayName || password.length < 8}>{t("users.create")}</button>
    </>}>
      <ErrorNote message={problem} />
      <label className="field">{t("users.email")}<input className="input" type="email" required autoFocus value={email} onChange={(event) => setEmail(event.target.value)} placeholder="user@example.com" /></label>
      <label className="field">{t("users.displayName")}<input className="input" required value={displayName} onChange={(event) => setDisplayName(event.target.value)} /></label>
      <label className="field">{t("users.password")}<input className="input" type="password" required minLength={8} autoComplete="new-password" value={password} onChange={(event) => setPassword(event.target.value)} /><span className="hint">{t("users.passwordHint")}</span></label>
      <label className="field">{t("users.role")}<select className="select" value={role} onChange={(event) => setRole(event.target.value as Role)}><option value="user">{t("users.roleUser")}</option><option value="admin">{t("users.roleAdmin")}</option></select></label>
    </Dialog>
  </form>;
}
