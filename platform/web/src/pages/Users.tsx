import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { useApp } from "../App";
import { useLocale } from "../i18n";
import type { ADUser, Role, User } from "../types";
import { Avatar, Badge, Dialog, Empty, ErrorNote, Icon, Loading, Switch, useToast, useTopBar } from "../ui";

export function UsersPage() {
  const { t, error } = useLocale();
  const { session, config } = useApp();
  const toast = useToast();
  const [query, setQuery] = useState("");
  const [users, setUsers] = useState<User[] | null>(null);
  const [cursor, setCursor] = useState("");
  const [problem, setProblem] = useState("");
  const [adding, setAdding] = useState(false);

  const load = useCallback(async (q: string, after: string) => {
    try {
      const page = await api.users(q, after);
      setUsers((current) => (after && current ? [...current, ...page.items] : page.items));
      setCursor(page.nextCursor ?? "");
      setProblem("");
    } catch (cause) {
      setProblem(error(cause));
    }
  }, [error]);
  useEffect(() => {
    const timer = window.setTimeout(() => void load(query.trim(), ""), query ? 250 : 0);
    return () => window.clearTimeout(timer);
  }, [query, load]);

  const upload = async (file?: File) => {
    if (!file) return;
    try {
      const result = await api.importUsers(await file.text(), file.name.toLowerCase().endsWith(".csv"));
      toast(t("users.importResult", { imported: result.imported, errors: result.errors.length }), { error: result.errors.length > 0 });
      void load(query.trim(), "");
    } catch (cause) {
      toast(error(cause), { error: true });
    }
  };
  useTopBar({
    title: t("nav.users"), description: t("users.description"),
    actions: <>
      <a className="btn sm" href={api.usersExportUrl(true)} download><Icon name="download" />{t("users.export")}</a>
      <label className="btn sm" title={t("users.importHint")}><Icon name="upload" />{t("users.import")}<input hidden type="file" accept=".csv,.json" onChange={(event) => { void upload(event.target.files?.[0]); event.target.value = ""; }} /></label>
      <button className="btn primary sm" onClick={() => setAdding(true)}><Icon name="plus" />{t("users.add")}</button>
    </>,
  }, [t, query]);

  const update = async (user: User, change: Partial<Pick<User, "role" | "disabled">>) => {
    try {
      const saved = await api.updateUser({ id: user.id, role: change.role ?? user.role, disabled: change.disabled ?? user.disabled });
      setUsers((items) => items?.map((item) => (item.id === saved.id ? saved : item)) ?? null);
      toast(t("users.updated", { name: saved.displayName || saved.username }));
    } catch (cause) {
      toast(error(cause), { error: true });
    }
  };
  const sourceLabel = (source: string) => ({ ad: t("users.sourceAd"), local: t("users.sourceLocal"), service: t("users.sourceService") } as Record<string, string>)[source] ?? source;

  return <>
    <div className="toolbar"><label className="search"><Icon name="search" /><input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("users.search")} aria-label={t("users.search")} /></label></div>
    <ErrorNote message={problem} onRetry={() => void load(query.trim(), "")} />
    {!users ? <Loading /> : <div className="card table-wrap">
      <table className="table"><thead><tr><th>{t("users.user")}</th><th>{t("users.source")}</th><th className="num opt">{t("users.groups")}</th><th>{t("users.role")}</th><th className="act">{t("users.active")}</th></tr></thead>
        <tbody>{users.length === 0 ? <tr><td colSpan={5}><Empty icon="search" title={t("users.empty")} /></td></tr> : users.map((user) => {
          const self = user.id === session.user?.id;
          const name = user.displayName || user.username;
          return <tr key={user.id}>
            <td><div className="cell-main"><Avatar name={name} /><div className="grow"><b>{name}{self && <span className="badge inline">{t("users.you")}</span>}</b>
              <small className="truncate">{user.username}{user.email && user.email !== user.username ? ` · ${user.email}` : ""}</small></div></div></td>
            <td><Badge kind={user.source === "ad" ? "accent" : ""}>{sourceLabel(user.source)}</Badge>{user.directoryDisabled && <div><Badge kind="danger">{t("users.disabledInAd")}</Badge></div>}</td>
            <td className="num opt">{user.groups?.length || "—"}</td>
            <td><select className="select sm" value={user.role} disabled={self} title={self ? t("users.selfRole") : undefined} aria-label={t("users.role")} onChange={(event) => void update(user, { role: event.target.value as Role })}>
              <option value="user">{t("users.roleUser")}</option><option value="admin">{t("users.roleAdmin")}</option></select></td>
            <td className="act"><Switch checked={!user.disabled} disabled={self} label={self ? t("users.selfDisable") : t("users.active")} onChange={(active) => void update(user, { disabled: !active })} /></td>
          </tr>;
        })}</tbody></table>
      {cursor && <div className="table-foot"><span /><button className="btn sm" onClick={() => void load(query.trim(), cursor)}>{t("list.more")}</button></div>}
    </div>}
    {adding && <AddUserDialog adEnabled={config.adEnabled} onClose={() => setAdding(false)} onAdded={(user) => { setUsers((items) => [user, ...(items ?? []).filter((item) => item.id !== user.id)]); }} />}
  </>;
}

function AddUserDialog({ adEnabled, onClose, onAdded }: { adEnabled: boolean; onClose: () => void; onAdded: (user: User) => void }) {
  const { t, error } = useLocale();
  const toast = useToast();
  const [mode, setMode] = useState<"ad" | "local">(adEnabled ? "ad" : "local");
  const [adQuery, setAdQuery] = useState("");
  const [found, setFound] = useState<ADUser[] | null>(null);
  const [selected, setSelected] = useState("");
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("user");
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState("");

  useEffect(() => {
    const query = adQuery.trim();
    setFound(null);
    if (mode !== "ad" || query.length < 2) return;
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      api.searchADUsers(query, controller.signal).then((result) => { setFound(result.items); setSelected(result.items[0]?.username ?? ""); }).catch((cause) => { if (!controller.signal.aborted) setProblem(error(cause)); });
    }, 250);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [adQuery, mode, error]);

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setSaving(true);
    setProblem("");
    try {
      const user = mode === "ad" ? await api.provisionADUser(selected) : await api.createUser({ email: email.trim(), displayName: displayName.trim(), password, role });
      onAdded(user);
      toast(t("users.added", { name: user.displayName || user.username }));
      onClose();
    } catch (cause) {
      setProblem(error(cause));
    } finally {
      setSaving(false);
    }
  };
  return <form onSubmit={(event) => void submit(event)}>
    <Dialog title={t("users.add")} onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>{t("common.cancel")}</button>
      <button className="btn primary" disabled={saving || (mode === "ad" ? !selected : !email || !displayName || password.length < 8)}>{mode === "ad" ? t("users.addFromAd") : t("users.create")}</button>
    </>}>
      {adEnabled && <div className="seg" role="tablist">
        <button type="button" className={mode === "ad" ? "on" : ""} onClick={() => setMode("ad")}>{t("users.fromAd")}</button>
        <button type="button" className={mode === "local" ? "on" : ""} onClick={() => setMode("local")}>{t("users.local")}</button>
      </div>}
      <ErrorNote message={problem} />
      {mode === "ad" ? <>
        <label className="field">{t("users.adSearch")}<span className="search"><Icon name="search" /><input className="input" value={adQuery} autoComplete="off" onChange={(event) => setAdQuery(event.target.value)} placeholder={t("users.adSearchPlaceholder")} /></span><span className="hint">{t("users.adHint")}</span></label>
        <div className="card choices">{adQuery.trim().length < 2 ? <div className="empty small"><p>{t("users.adTooShort")}</p></div>
          : found === null ? <div className="empty small"><p>{t("app.loading")}</p></div>
          : found.length === 0 ? <div className="empty small"><p>{t("users.adNone")}</p></div>
          : found.map((user) => <label key={user.username} className={`combo-item ${selected === user.username ? "on" : ""}`}>
              <input type="radio" name="ad-user" checked={selected === user.username} onChange={() => setSelected(user.username)} />
              <Avatar name={user.displayName || user.username} /><span className="grow"><b>{user.displayName || user.username}</b><small>{user.username} · {user.email || t("users.noEmail")}</small></span></label>)}</div>
      </> : <>
        <label className="field">{t("users.email")}<input className="input" type="email" required value={email} onChange={(event) => setEmail(event.target.value)} placeholder="user@example.com" /></label>
        <label className="field">{t("users.displayName")}<input className="input" required value={displayName} onChange={(event) => setDisplayName(event.target.value)} /></label>
        <label className="field">{t("users.password")}<input className="input" type="password" required minLength={8} autoComplete="new-password" value={password} onChange={(event) => setPassword(event.target.value)} /><span className="hint">{t("users.passwordHint")}</span></label>
        <label className="field">{t("users.role")}<select className="select" value={role} onChange={(event) => setRole(event.target.value as Role)}><option value="user">{t("users.roleUser")}</option><option value="admin">{t("users.roleAdmin")}</option></select></label>
      </>}
    </Dialog>
  </form>;
}
