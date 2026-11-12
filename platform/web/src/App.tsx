import { useEffect, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { Link, NavLink, Navigate, Route, Routes, useNavigate, useParams } from "react-router-dom";
import { api, setCSRFToken } from "./api";
import { RepositoryFiles, RepositoryHistory, RepositoryLocks } from "./RepositoryContent";
import type { ADUser, AuditEvent, BulkResult, GlobalSettings, Grant, GrantSubject, Permission, PublicConfig, Repository, Session, SubjectType, User } from "./types";

const emptySession: Session = { authenticated: false };

export default function App() {
  const [session, setSession] = useState<Session>(emptySession);
  const [config, setConfig] = useState<PublicConfig | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  async function refresh() {
    try {
      const [nextSession, nextConfig] = await Promise.all([api.session(), api.config()]);
      setCSRFToken(nextSession.csrfToken);
      setSession(nextSession);
      setConfig(nextConfig);
      setError("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Service is unavailable");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh();
  }, []);

  useEffect(() => {
    document.documentElement.dataset.theme = config?.theme || "midnight";
  }, [config?.theme]);

  async function logout() {
    const result = await api.logout();
    setCSRFToken(undefined);
    setSession(emptySession);
    if (result.logoutUrl) window.location.assign(result.logoutUrl);
  }

  async function devLogin() {
    await api.devLogin();
    await refresh();
  }

  const isAdmin = session.user?.role === "admin";

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <Link to="/" className="brand"><span className="brand-mark"><img src="/icons/lore-mark.svg" alt="" /></span><span>Lore</span></Link>
        <nav>
          <NavLink to="/" end>Repositories</NavLink>
          {isAdmin && <div className="nav-section">Administration</div>}
          {isAdmin && <NavLink to="/admin/repositories">Manage repositories</NavLink>}
          {isAdmin && <NavLink to="/admin/users">Users</NavLink>}
          {isAdmin && <NavLink to="/admin/audit">Audit log</NavLink>}
          {isAdmin && <NavLink to="/admin/settings">Settings</NavLink>}
        </nav>
        <div className="sidebar-foot">Self-hosted version control</div>
      </aside>
      <div className="main-area">
        <header className="topbar">
          <div>
            <span className="status-dot" /> Control plane
          </div>
          <div className="account">
            {session.authenticated ? (
              <>
                <span className="avatar">{session.user?.displayName?.charAt(0).toUpperCase() || "U"}</span>
                <span>{session.user?.displayName || session.user?.username}</span>
                <span className="role-badge">{session.user?.role}</span>
                <button className="button ghost small" onClick={() => void logout()}>Sign out</button>
              </>
            ) : (
              <>
                {config?.devAuthEnabled && <button className="button ghost" onClick={() => void devLogin()}>Development login</button>}
                {config?.oidcEnabled && <a className="button primary" href="/web/v1/login">Sign in</a>}
              </>
            )}
          </div>
        </header>
        <main className="content">
          {error && <div className="alert error">{error}<button onClick={() => void refresh()}>Retry</button></div>}
          {loading ? <div className="loading">Loading…</div> : (
            <Routes>
              <Route path="/" element={<Catalog session={session} config={config} />} />
              <Route path="/repositories/:id" element={<RepositoryAccess nativeRepositoryUrl={config?.nativeRepositoryUrl} />} />
              <Route path="/admin/repositories" element={isAdmin ? <AdminRepositories /> : <Navigate to="/" />} />
              <Route path="/admin/users" element={isAdmin ? <Users currentUser={session.user} adEnabled={Boolean(config?.adEnabled)} /> : <Navigate to="/" />} />
              <Route path="/admin/audit" element={isAdmin ? <Audit /> : <Navigate to="/" />} />
              <Route path="/admin/settings" element={isAdmin ? <Settings config={config} onSaved={(settings) => setConfig((current) => current ? { ...current, ...settings } : current)} /> : <Navigate to="/" />} />
              <Route path="*" element={<Navigate to="/" />} />
            </Routes>
          )}
        </main>
      </div>
    </div>
  );
}

function PageHeader({ eyebrow, title, description, actions, titleActions }: { eyebrow?: string; title: string; description: string; actions?: ReactNode; titleActions?: ReactNode }) {
  return (
    <div className="page-header">
      <div>{eyebrow && <div className="eyebrow">{eyebrow}</div>}<div className="page-title"><h1>{title}</h1>{titleActions}</div><p>{description}</p></div>
      {actions && <div className="header-actions">{actions}</div>}
    </div>
  );
}

function ErrorMessage({ message }: { message: string }) {
  return message ? <div className="alert error">{message}</div> : null;
}

function BulkActions({ exportPath, onImport, onDone }: { exportPath: string; onImport: (body: string, csv: boolean) => Promise<BulkResult>; onDone: () => Promise<void> }) {
  const [result, setResult] = useState<BulkResult | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function upload(file?: File) {
    if (!file) return;
    setBusy(true);
    setError("");
    try {
      const imported = await onImport(await file.text(), file.name.toLowerCase().endsWith(".csv"));
      setResult(imported);
      await onDone();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Import failed");
    } finally {
      setBusy(false);
    }
  }

  return <div className="bulk-actions">
    <a className="button ghost small" href={`/web/v1${exportPath}?format=csv`}>Export CSV</a>
    <a className="button ghost small" href={`/web/v1${exportPath}`}>Export JSON</a>
    <label className={`button ghost small ${busy ? "disabled" : ""}`}>Import CSV / JSON<input hidden type="file" accept=".csv,.json" disabled={busy} onChange={(event) => { void upload(event.target.files?.[0]); event.target.value = ""; }} /></label>
    {result && <span className="muted">Imported: {result.imported}; errors: {result.errors.length}</span>}
    {result?.errors.slice(0, 3).map((item) => <div className="muted" key={item.line}>Line {item.line}: {item.message}</div>)}
    <ErrorMessage message={error} />
  </div>;
}

function RepositoryIcon({ name }: { name: string }) {
  const variant = [...name].reduce((hash, character) => (hash * 31 + character.charCodeAt(0)) >>> 0, 0) % 6;
  const shapes = [
    <><path d="M3 7.5v11a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-9a2 2 0 0 0-2-2h-7l-2-3H5a2 2 0 0 0-2 2z" /><path d="M3 10h18" /></>,
    <><path d="m12 3 8 4.5v9L12 21l-8-4.5v-9z" /><path d="m4 7.5 8 4.5 8-4.5M12 12v9" /></>,
    <><path d="m12 3 9 5-9 5-9-5z" /><path d="m3 12 9 5 9-5M3 16l9 5 9-5" /></>,
    <><rect x="4" y="4" width="16" height="16" rx="3" /><path d="M8 9h8M8 13h5M8 17h8" /></>,
    <><path d="m8 5-6 7 6 7M16 5l6 7-6 7M14 3l-4 18" /></>,
    <><ellipse cx="12" cy="5" rx="8" ry="3" /><path d="M4 5v7c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 12v7c0 1.7 3.6 3 8 3s8-1.3 8-3v-7" /></>,
  ];
  return <div className={`repo-icon tone-${variant % 3}`}><svg width="23" height="23" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{shapes[variant]}</svg></div>;
}

function Catalog({ session, config }: { session: Session; config: PublicConfig | null }) {
  const [repositories, setRepositories] = useState<Repository[]>([]);
  const [copied, setCopied] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api.catalog().then((page) => setRepositories(page.items)).catch((cause) => setError(cause.message)).finally(() => setLoading(false));
  }, []);

  function repositoryUrl(repository: Repository) {
    const configured = config?.nativeRepositoryUrl || `lore://${window.location.hostname}:41337`;
    const url = new URL(configured.replace(/\/$/, ""));
    if (url.port === "41337") url.port = "";
    return `${url.toString().replace(/\/$/, "")}/${encodeURIComponent(repository.name)}`;
  }

  async function copyRepository(repository: Repository) {
    try {
      await navigator.clipboard.writeText(repositoryUrl(repository));
      setCopied(repository.id);
      window.setTimeout(() => setCopied((current) => current === repository.id ? "" : current), 1500);
    } catch {
      setError("Couldn't copy the repository URL. Allow clipboard access and try again.");
    }
  }

  return (
    <>
      <PageHeader eyebrow="Workspace" title="Repositories" description={session.authenticated ? "Repositories available to your account and public projects." : "Public repositories available without an account."} />
      <ErrorMessage message={error} />
      {loading ? <div className="loading">Loading repositories…</div> : repositories.length === 0 ? (
        <EmptyState title="No repositories available" text="A public repository or an explicit access grant will appear here." />
      ) : (
        <div className="repository-grid">
          {repositories.map((repository) => {
            const url = repositoryUrl(repository);
            return <div className="repository-card" key={repository.id}>
              <Link to={`/repositories/${repository.id}`} className="repository-card-link" aria-label={`Open ${repository.name}`}>
                <RepositoryIcon name={repository.name} />
                <div className="repo-card-body"><div className="repo-title"><h2>{repository.name}</h2><span className={`visibility ${repository.public ? "public" : "private"}`}>{repository.public ? <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18" /></svg> : <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true"><rect x="5" y="10" width="14" height="10" rx="2" /><path d="M8 10V7a4 4 0 0 1 8 0v3" /></svg>}{repository.public ? "Public" : "Private"}</span></div><p>{repository.description || "No description"}</p><span className="mono">{repository.id}</span></div>
                <span className="chevron">›</span>
              </Link>
              <button className="icon-button repository-copy" title={`Copy repository URL: ${url}`} aria-label={copied === repository.id ? "Repository URL copied" : `Copy ${repository.name} repository URL`} onClick={() => void copyRepository(repository)}>
                {copied === repository.id ? <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m5 12 4 4L19 6" /></svg> : <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2" /><path d="M15 9V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h3" /></svg>}
              </button>
            </div>;
          })}
        </div>
      )}
    </>
  );
}

function AdminRepositories() {
  const navigate = useNavigate();
  const [repositories, setRepositories] = useState<Repository[]>([]);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [operationID, setOperationID] = useState("");
  const [isPublic, setPublic] = useState(false);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  async function load() {
    try {
      const page = await api.repositories();
      setRepositories(page.items);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Couldn't load repositories");
    }
  }
  useEffect(() => { void load(); }, []);

  async function create(event: FormEvent) {
    event.preventDefault();
    setSaving(true);
    setError("");
    const id = operationID || crypto.randomUUID();
    setOperationID(id);
    try {
      await api.createRepository({ id, name, description, public: isPublic });
      setName(""); setDescription(""); setPublic(false); setOperationID("");
      await load();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Couldn't create repository");
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <PageHeader eyebrow="Administration" title="Repositories" description="Register repositories and configure their access policy." />
      <ErrorMessage message={error} />
      <section className="panel">
        <h2>New repository</h2>
        <form className="form-grid" onSubmit={(event) => void create(event)}>
          <label>Name<input required value={name} onChange={(event) => setName(event.target.value)} placeholder="team/project" /></label>
          <label>Description<input value={description} onChange={(event) => setDescription(event.target.value)} placeholder="What does this repository contain?" /></label>
          <label className="checkbox"><input type="checkbox" checked={isPublic} onChange={(event) => setPublic(event.target.checked)} /> Public read access</label>
          <button className="button primary" disabled={saving}>{saving ? "Creating…" : "Create repository"}</button>
        </form>
      </section>
      <section className="panel table-panel">
        <table><thead><tr><th>Name</th><th>Description</th><th>Owner</th></tr></thead>
          <tbody>{repositories.map((repository) => <tr className="clickable-row" key={repository.id} role="link" tabIndex={0} onClick={() => navigate(`/repositories/${repository.id}`)} onKeyDown={(event) => { if (event.key === "Enter") navigate(`/repositories/${repository.id}`); }}><td className="strong">{repository.name}</td><td>{repository.description}</td><td title={repository.ownerId}>{repository.ownerName || "Former user"}</td></tr>)}</tbody>
        </table>
      </section>
    </>
  );
}

function Users({ currentUser, adEnabled }: { currentUser?: User; adEnabled: boolean }) {
  const [users, setUsers] = useState<User[]>([]);
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<User["role"]>("user");
  const [adUsername, setAdUsername] = useState("");
  const [adSuggestions, setAdSuggestions] = useState<ADUser[]>([]);
  const [saving, setSaving] = useState(false);
  const [provisioning, setProvisioning] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const query = adUsername.trim();
    setAdSuggestions([]);
    if (!adEnabled || query.length < 2) return;
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      api.searchADUsers(query, controller.signal)
        .then((result) => setAdSuggestions(result.items))
        .catch(() => {
          if (!controller.signal.aborted) setAdSuggestions([]);
        });
    }, 250);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [adUsername, adEnabled]);

  async function load() {
    try {
      const page = await api.users();
      setUsers(page.items);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Couldn't load users");
    }
  }
  useEffect(() => { void load(); }, []);

  function addUser(user: User) {
    setUsers((items) => [...items.filter((item) => item.id !== user.id), user].sort((a, b) => a.username.localeCompare(b.username)));
  }

  async function create(event: FormEvent) {
    event.preventDefault();
    setSaving(true);
    setError("");
    try {
      addUser(await api.createUser({ email, displayName, password, role }));
      setEmail(""); setDisplayName(""); setPassword(""); setRole("user");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Couldn't create user");
    } finally {
      setSaving(false);
    }
  }

  async function provision(event: FormEvent) {
    event.preventDefault();
    setProvisioning(true);
    setError("");
    try {
      addUser(await api.provisionADUser(adUsername.trim()));
      setAdUsername("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Couldn't add the Active Directory user");
    } finally {
      setProvisioning(false);
    }
  }

  async function update(user: User) {
    try {
      const updated = await api.updateUser(user);
      setUsers((items) => items.map((item) => item.id === updated.id ? updated : item));
      setError("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Couldn't update user");
    }
  }

  return (
    <>
      <PageHeader eyebrow="Administration" title="Users" description="Create local accounts and manage global roles. Active Directory attributes remain read-only." actions={<BulkActions exportPath="/admin/users/export" onImport={api.importUsers} onDone={load} />} />
      <ErrorMessage message={error} />
      {adEnabled && <section className="panel">
        <h2>Add Active Directory user</h2>
        <p>Enter at least 2 characters to find an AD account, then select it to assign repository access before its first sign in.</p>
        <form className="form-grid" onSubmit={(event) => void provision(event)}>
          <label>AD username<input required autoComplete="off" list="ad-user-suggestions" value={adUsername} onChange={(event) => setAdUsername(event.target.value)} placeholder="ad_user" /></label>
          <datalist id="ad-user-suggestions">
            {adSuggestions.map((user) => <option key={user.username} value={user.username} label={[user.displayName, user.email].filter(Boolean).join(" · ")} />)}
          </datalist>
          <button className="button primary" disabled={provisioning}>{provisioning ? "Adding…" : "Add AD user"}</button>
        </form>
      </section>}
      <section className="panel">
        <h2>New local user</h2>
        <p>The user must change the temporary password at the next sign in.</p>
        <form className="form-grid" onSubmit={(event) => void create(event)}>
          <label>Email<input required type="email" autoComplete="off" value={email} onChange={(event) => setEmail(event.target.value)} placeholder="user@example.com" /></label>
          <label>Display name<input required value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder="User name" /></label>
          <label>Temporary password<input required type="password" autoComplete="new-password" minLength={8} value={password} onChange={(event) => setPassword(event.target.value)} /></label>
          <label>Role<select value={role} onChange={(event) => setRole(event.target.value as User["role"])}><option value="user">User</option><option value="admin">Admin</option></select></label>
          <button className="button primary" disabled={saving}>{saving ? "Creating…" : "Create user"}</button>
        </form>
      </section>
      <section className="panel table-panel">
        <table><thead><tr><th>User</th><th>Source</th><th>Groups</th><th>Role</th><th>Status</th></tr></thead><tbody>
          {users.map((user) => <tr key={user.id}>
            <td><div className="strong">{user.displayName || user.username}</div><div className="muted">{user.email || user.id}</div></td>
            <td><span className="tag">{user.source}</span></td>
            <td>{user.groups?.length || 0}</td>
            <td><select value={user.role} disabled={user.id === currentUser?.id} onChange={(event) => void update({ ...user, role: event.target.value as User["role"] })}><option value="user">User</option><option value="admin">Admin</option></select></td>
            <td><label className="inline-check"><input type="checkbox" checked={!user.disabled} disabled={user.id === currentUser?.id} onChange={(event) => void update({ ...user, disabled: !event.target.checked })} /> Active</label></td>
          </tr>)}
        </tbody></table>
      </section>
    </>
  );
}

function SubjectIcon({ type }: { type: SubjectType }) {
  return <svg className="subject-icon" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{type === "group" ? <><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" /><circle cx="9" cy="7" r="4" /><path d="M22 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75" /></> : <><path d="M20 21a8 8 0 0 0-16 0" /><circle cx="12" cy="7" r="4" /></>}</svg>;
}

function RepositoryAccess({ nativeRepositoryUrl }: { nativeRepositoryUrl?: string }) {
  const { id = "" } = useParams();
  const [repository, setRepository] = useState<Repository | null>(null);
  const [grants, setGrants] = useState<Grant[]>([]);
  const [subjects, setSubjects] = useState<GrantSubject[]>([]);
  const [subjectType, setSubjectType] = useState<SubjectType>("user");
  const [subject, setSubject] = useState("");
  const [permission, setPermission] = useState<Permission>("read");
  const [tab, setTab] = useState<"files" | "history" | "locks" | "access">("files");
  const [error, setError] = useState("");
  const [forbidden, setForbidden] = useState(false);
  const [copied, setCopied] = useState(false);

  function repositoryUrl() {
    if (!repository) return "";
    const configured = nativeRepositoryUrl || `lore://${window.location.hostname}:41337`;
    const url = new URL(configured.replace(/\/$/, ""));
    if (url.port === "41337") url.port = "";
    return `${url.toString().replace(/\/$/, "")}/${encodeURIComponent(repository.name)}`;
  }

  async function copyRepositoryUrl() {
    try {
      await navigator.clipboard.writeText(repositoryUrl());
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setError("Couldn't copy the repository URL. Allow clipboard access and try again.");
    }
  }

  async function load() {
    try {
      const repo = await api.repository(id);
      setRepository(repo);
      try {
        const result = await api.grants(id);
        setGrants(result.items);
        const subjectResult = await api.grantSubjects(id);
        setSubjects(subjectResult.items);
        setForbidden(false);
      } catch (cause) {
        const message = cause instanceof Error ? cause.message : "";
        if (!message.includes("owner access")) throw cause;
        setForbidden(true);
      }
      setError("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Couldn't load repository");
    }
  }
  useEffect(() => { void load(); }, [id]);

  async function addGrant(event: FormEvent) {
    event.preventDefault();
    try {
      const entered = subject.trim().toLocaleLowerCase();
      const selected = subjects.find((item) => item.type === subjectType && [item.id, item.value, item.label, item.detail].some((value) => value.toLocaleLowerCase() === entered));
      await api.setGrant({ repositoryId: id, subjectType, subject: ["anonymous", "authenticated"].includes(subjectType) ? "" : selected?.id || subject.trim(), permission });
      setSubject("");
      await load();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Couldn't save grant");
    }
  }

  async function remove(grant: Grant) {
    try {
      await api.removeGrant(grant);
      await load();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Couldn't remove grant");
    }
  }

  if (!repository) return <><ErrorMessage message={error} /><div className="loading">Loading repository…</div></>;
  return (
    <>
      <PageHeader eyebrow="Repository" title={repository.name} description={repository.description || "Browse content, history and repository access."} titleActions={<button className="icon-button" title={`Copy repository URL: ${repositoryUrl()}`} aria-label={copied ? "Repository URL copied" : "Copy repository URL"} onClick={() => void copyRepositoryUrl()}>{copied ? <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m5 12 4 4L19 6" /></svg> : <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2" /><path d="M15 9V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h3" /></svg>}</button>} />
      <section className="panel detail-panel"><div><span className="muted">Repository ID</span><div className="mono copy-value">{repository.id}</div></div><button className="button ghost" onClick={() => void navigator.clipboard.writeText(repository.id)}>Copy ID</button></section>
      <div className="repository-tabs">{(["files", "history", "locks", "access"] as const).map((item) => <button className={tab === item ? "active" : ""} key={item} onClick={() => setTab(item)}>{item.charAt(0).toUpperCase() + item.slice(1)}</button>)}</div>
      {tab === "files" && <RepositoryFiles id={id} />}
      {tab === "history" && <RepositoryHistory id={id} />}
      {tab === "locks" && <RepositoryLocks id={id} />}
      {tab === "access" && <><ErrorMessage message={error} />{forbidden ? <EmptyState title="Read access" text="Only a repository owner or administrator can manage grants." /> : <>
        <section className="panel"><h2>Access grants</h2><p className="muted">Users receive the highest permission from their direct and group grants. Public read never includes write access.</p>
          <form className="grant-form" onSubmit={(event) => void addGrant(event)}>
            <select aria-label="Subject type" value={subjectType} onChange={(event) => { setSubjectType(event.target.value as SubjectType); setSubject(""); }}><option value="user">User</option><option value="group">Group</option><option value="authenticated">All authenticated users</option><option value="anonymous">Public (anonymous)</option></select>
            {!["anonymous", "authenticated"].includes(subjectType) && <>
              <input required autoComplete="off" aria-label="User or group" list={`grant-subjects-${id}`} value={subject} onChange={(event) => setSubject(event.target.value)} placeholder={subjectType === "user" ? "ad_user or user@example.com" : "Active Directory group"} />
              <datalist id={`grant-subjects-${id}`}>{subjects.filter((item) => item.type === subjectType && !item.automated).map((item) => <option key={item.id} value={item.value || item.id} label={[item.label, item.detail].filter(Boolean).join(" · ")} />)}</datalist>
            </>}
            <select aria-label="Permission" value={permission} onChange={(event) => setPermission(event.target.value as Permission)}><option value="read">Read</option><option value="write">Write</option>{!["anonymous", "authenticated"].includes(subjectType) && <option value="owner">Owner</option>}</select>
            <button className="button primary">Add or update</button>
          </form>
          <BulkActions exportPath={`/repositories/${id}/grants/export`} onImport={(body, csv) => api.importGrants(id, body, csv)} onDone={load} />
          <div className="grant-list">{grants.map((grant) => {
            const info = subjects.find((item) => item.type === grant.subjectType && item.id === grant.subject);
            const fallback = grant.subjectType === "anonymous" ? "Anyone" : grant.subjectType === "authenticated" ? "Any signed-in user" : `Unknown ${grant.subjectType} (${grant.subject})`;
            return <div className="grant-row" key={`${grant.subjectType}:${grant.subject}`}><span className="tag"><SubjectIcon type={grant.subjectType} />{grant.subjectType}</span><span className="grant-subject" title={grant.subject}><span>{info?.label || fallback}</span>{info?.detail && <small className="muted">{info.detail}</small>}</span><span className="permission">{grant.permission}</span><button className="button danger small" onClick={() => void remove(grant)}>Remove</button></div>;
          })}</div>
        </section>
      </>}</>}
    </>
  );
}

function Audit() {
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [error, setError] = useState("");
  const actionLabels: Record<string, string> = {
    "repository.create": "Repository created",
    "repository.update": "Repository updated",
    "repository.delete": "Repository deleted",
    "grant.set": "Access changed",
    "grant.remove": "Access removed",
    "user.create": "User created",
    "user.update": "User updated",
    "settings.update": "Settings updated",
  };
  const valueLabel = (event: AuditEvent, value: string) => {
    if (event.action === "settings.update") {
      const [registration, theme] = value.split(":");
      const registrationLabel = registration === "true" ? "Registration enabled" : registration === "false" ? "Registration disabled" : registration;
      return theme ? `${registrationLabel} · ${theme.charAt(0).toUpperCase()}${theme.slice(1)}` : registrationLabel;
    }
    return ({ none: "No access", read: "Read", write: "Write", owner: "Owner", user: "User", admin: "Admin" } as Record<string, string>)[value] || value;
  };
  const changeLabel = (event: AuditEvent) => {
    if (["repository.create", "repository.delete"].includes(event.action)) return "—";
    if (event.action === "repository.update") return "Repository details";
    const before = event.before ? valueLabel(event, event.before) : "";
    const after = event.after ? valueLabel(event, event.after) : event.action === "grant.remove" ? "No access" : "";
    return before && after ? `${before} → ${after}` : before || after || "—";
  };
  useEffect(() => { api.audit().then((page) => setEvents(page.items)).catch((cause) => setError(cause.message)); }, []);
  return <><PageHeader eyebrow="Administration" title="Audit log" description="Policy, account, repository, and settings changes." /><ErrorMessage message={error} /><section className="panel table-panel"><table><thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Target</th><th>Change</th></tr></thead><tbody>{events.map((event) => <tr key={event.id}><td>{new Date(event.createdAt).toLocaleString()}</td><td title={`User ID: ${event.actorId}`}>{event.actorName || "Former user"}</td><td><span className="tag">{actionLabels[event.action] || event.action}</span></td><td title={`${event.targetType} ID: ${event.targetId}`}><span className="strong">{event.targetName || (event.targetType === "repository" ? "Former repository" : "Former user")}</span></td><td>{changeLabel(event)}</td></tr>)}</tbody></table></section></>;
}

const themeOptions = [
  { id: "midnight", name: "Midnight", description: "Graphite surfaces and a calm blue accent.", colors: ["#0d1117", "#161b22", "#21262d", "#58a6ff", "#f778ba"] },
  { id: "aurora", name: "Aurora", description: "Violet accents and sharp geometry.", colors: ["#100e1c", "#1c1830", "#292342", "#b7a0ff", "#7dd3e8"] },
  { id: "ocean", name: "Ocean", description: "Deep blue with a warm sand accent.", colors: ["#0d2033", "#123452", "#1a4465", "#8fc9ff", "#f0cf9a"] },
  { id: "retro", name: "Retro", description: "Warm charcoal, soft gold and rounded panels.", colors: ["#181714", "#24221e", "#322f28", "#e8ca8d", "#a9b8a3"] },
  { id: "autumn", name: "Autumn", description: "Warm terracotta on neutral dark surfaces.", colors: ["#1b1816", "#292420", "#39312b", "#e99a78", "#b5c49c"] },
  { id: "light", name: "Light", description: "Cream background with a warm orange accent.", colors: ["#f4f1e8", "#fffdf7", "#eee9db", "#a94312", "#245b49"] },
  { id: "cyberpunk", name: "Cyberpunk", description: "Soft cyan and magenta on dark violet.", colors: ["#0b0915", "#171128", "#241b3b", "#76dce8", "#e995c5"] },
];

function Settings({ config, onSaved }: { config: PublicConfig | null; onSaved: (settings: GlobalSettings) => void }) {
  const [enabled, setEnabled] = useState(false);
  const [theme, setTheme] = useState("midnight");
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    api.settings().then((settings) => {
      setEnabled(settings.registrationEnabled);
      setTheme(settings.theme);
    }).catch((cause) => setError(cause.message));
  }, []);
  async function save() {
    setSaving(true);
    try {
      const settings = await api.saveSettings({ registrationEnabled: enabled, theme });
      setEnabled(settings.registrationEnabled);
      setTheme(settings.theme);
      onSaved(settings);
      setSaved(true);
      setError("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Couldn't save settings");
    } finally {
      setSaving(false);
    }
  }
  return <>
    <PageHeader eyebrow="Administration" title="Settings" description="Deployment-wide appearance, identity and registration controls." />
    <ErrorMessage message={error} />
    {saved && <div className="alert success">Settings saved and applied globally.</div>}
    <section className="panel settings-panel">
      <div><h2>Global theme</h2><p>The selected palette is used by every user and by the signed-out catalog.</p></div>
      <div className="theme-grid">{themeOptions.map((option) => <button type="button" className={`theme-card ${theme === option.id ? "selected" : ""}`} key={option.id} onClick={() => { setTheme(option.id); setSaved(false); }}>
        <span className="theme-palette">{option.colors.map((color) => <span key={color} style={{ background: color }} />)}</span>
        <span className="strong">{option.name}</span><span className="muted">{option.description}</span>
      </button>)}</div>
    </section>
    <section className="panel settings-panel">
      <div><h2>Self-registration</h2><p>New users register through Keycloak and receive the ordinary user role. Repository access still requires a grant.</p><p className="muted">This also updates the Keycloak realm. Current OIDC status: {config?.oidcEnabled ? "configured" : "not configured"}.</p></div>
      <label className="switch"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} /><span>Registration enabled</span></label>
      <button className="button primary" disabled={saving} onClick={() => void save()}>{saving ? "Saving…" : "Save settings"}</button>
    </section>
  </>;
}

function EmptyState({ title, text }: { title: string; text: string }) {
  return <div className="empty-state"><div className="empty-icon">◇</div><h2>{title}</h2><p>{text}</p></div>;
}
