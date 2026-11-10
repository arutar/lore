import { useEffect, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { Link, NavLink, Navigate, Route, Routes, useParams } from "react-router-dom";
import { api, setCSRFToken } from "./api";
import { RepositoryFiles, RepositoryHistory, RepositoryLocks } from "./RepositoryContent";
import type { AuditEvent, BulkResult, GlobalSettings, Grant, Permission, PublicConfig, Repository, Session, SubjectType, User } from "./types";

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
    await api.logout();
    setCSRFToken(undefined);
    setSession(emptySession);
  }

  async function devLogin() {
    await api.devLogin();
    await refresh();
  }

  const isAdmin = session.user?.role === "admin";

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <Link to="/" className="brand"><span className="brand-mark">L</span><span>Lore</span></Link>
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
              <Route path="/" element={<Catalog session={session} />} />
              <Route path="/repositories/:id" element={<RepositoryAccess />} />
              <Route path="/admin/repositories" element={isAdmin ? <AdminRepositories /> : <Navigate to="/" />} />
              <Route path="/admin/users" element={isAdmin ? <Users currentUser={session.user} /> : <Navigate to="/" />} />
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

function PageHeader({ eyebrow, title, description, actions }: { eyebrow?: string; title: string; description: string; actions?: ReactNode }) {
  return (
    <div className="page-header">
      <div>{eyebrow && <div className="eyebrow">{eyebrow}</div>}<h1>{title}</h1><p>{description}</p></div>
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

function Catalog({ session }: { session: Session }) {
  const [repositories, setRepositories] = useState<Repository[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api.catalog().then((page) => setRepositories(page.items)).catch((cause) => setError(cause.message)).finally(() => setLoading(false));
  }, []);

  return (
    <>
      <PageHeader eyebrow="Workspace" title="Repositories" description={session.authenticated ? "Repositories available to your account and public projects." : "Public repositories available without an account."} />
      <ErrorMessage message={error} />
      {loading ? <div className="loading">Loading repositories…</div> : repositories.length === 0 ? (
        <EmptyState title="No repositories available" text="A public repository or an explicit access grant will appear here." />
      ) : (
        <div className="repository-grid">
          {repositories.map((repository) => (
            <Link to={`/repositories/${repository.id}`} className="repository-card" key={repository.id}>
              <div className="repo-icon">{repository.name.charAt(0).toUpperCase()}</div>
              <div className="repo-card-body"><h2>{repository.name}</h2><p>{repository.description || "No description"}</p><span className="mono">{repository.id}</span></div>
              <span className="chevron">›</span>
            </Link>
          ))}
        </div>
      )}
    </>
  );
}

function AdminRepositories() {
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
        <table><thead><tr><th>Name</th><th>Description</th><th>Owner</th><th /></tr></thead>
          <tbody>{repositories.map((repository) => <tr key={repository.id}><td className="strong">{repository.name}</td><td>{repository.description}</td><td className="mono">{repository.ownerId}</td><td><Link className="button ghost small" to={`/repositories/${repository.id}`}>Access</Link></td></tr>)}</tbody>
        </table>
      </section>
    </>
  );
}

function Users({ currentUser }: { currentUser?: User }) {
  const [users, setUsers] = useState<User[]>([]);
  const [error, setError] = useState("");
  async function load() {
    try {
      const page = await api.users();
      setUsers(page.items);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Couldn't load users");
    }
  }
  useEffect(() => { void load(); }, []);

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
      <PageHeader eyebrow="Administration" title="Users" description="Manage global roles and account access. Active Directory attributes remain read-only." actions={<BulkActions exportPath="/admin/users/export" onImport={api.importUsers} onDone={load} />} />
      <ErrorMessage message={error} />
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

function RepositoryAccess() {
  const { id = "" } = useParams();
  const [repository, setRepository] = useState<Repository | null>(null);
  const [grants, setGrants] = useState<Grant[]>([]);
  const [subjectType, setSubjectType] = useState<SubjectType>("user");
  const [subject, setSubject] = useState("");
  const [permission, setPermission] = useState<Permission>("read");
  const [tab, setTab] = useState<"files" | "history" | "locks" | "access">("files");
  const [error, setError] = useState("");
  const [forbidden, setForbidden] = useState(false);

  async function load() {
    try {
      const repo = await api.repository(id);
      setRepository(repo);
      try {
        const result = await api.grants(id);
        setGrants(result.items);
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
      await api.setGrant({ repositoryId: id, subjectType, subject: ["anonymous", "authenticated"].includes(subjectType) ? "" : subject, permission });
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
      <PageHeader eyebrow="Repository" title={repository.name} description={repository.description || "Browse content, history and repository access."} />
      <section className="panel detail-panel"><div><span className="muted">Repository ID</span><div className="mono copy-value">{repository.id}</div></div><button className="button ghost" onClick={() => void navigator.clipboard.writeText(repository.id)}>Copy ID</button></section>
      <div className="repository-tabs">{(["files", "history", "locks", "access"] as const).map((item) => <button className={tab === item ? "active" : ""} key={item} onClick={() => setTab(item)}>{item.charAt(0).toUpperCase() + item.slice(1)}</button>)}</div>
      {tab === "files" && <RepositoryFiles id={id} />}
      {tab === "history" && <RepositoryHistory id={id} />}
      {tab === "locks" && <RepositoryLocks id={id} />}
      {tab === "access" && <><ErrorMessage message={error} />{forbidden ? <EmptyState title="Read access" text="Only a repository owner or administrator can manage grants." /> : <>
        <section className="panel"><h2>Access grants</h2><p className="muted">Users receive the highest permission from their direct and group grants. Public read never includes write access.</p>
          <form className="grant-form" onSubmit={(event) => void addGrant(event)}>
            <select value={subjectType} onChange={(event) => setSubjectType(event.target.value as SubjectType)}><option value="user">User</option><option value="group">AD / local group</option><option value="authenticated">All authenticated users</option><option value="anonymous">Public (anonymous)</option></select>
            {!["anonymous", "authenticated"].includes(subjectType) && <input required value={subject} onChange={(event) => setSubject(event.target.value)} placeholder={subjectType === "group" ? "Group objectGUID or SID" : "User subject ID"} />}
            <select value={permission} onChange={(event) => setPermission(event.target.value as Permission)}><option value="read">Read</option><option value="write">Write</option>{!["anonymous", "authenticated"].includes(subjectType) && <option value="owner">Owner</option>}</select>
            <button className="button primary">Add or update</button>
          </form>
          <BulkActions exportPath={`/repositories/${id}/grants/export`} onImport={(body, csv) => api.importGrants(id, body, csv)} onDone={load} />
          <div className="grant-list">{grants.map((grant) => <div className="grant-row" key={`${grant.subjectType}:${grant.subject}`}><span className="tag">{grant.subjectType}</span><span className="grant-subject">{grant.subject || (grant.subjectType === "anonymous" ? "Anyone" : "Any signed-in user")}</span><span className="permission">{grant.permission}</span><button className="button danger small" onClick={() => void remove(grant)}>Remove</button></div>)}</div>
        </section>
      </>}</>}
    </>
  );
}

function Audit() {
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [error, setError] = useState("");
  useEffect(() => { api.audit().then((page) => setEvents(page.items)).catch((cause) => setError(cause.message)); }, []);
  return <><PageHeader eyebrow="Administration" title="Audit log" description="Policy, account, repository, and settings changes." /><ErrorMessage message={error} /><section className="panel table-panel"><table><thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Target</th><th>Change</th></tr></thead><tbody>{events.map((event) => <tr key={event.id}><td>{new Date(event.createdAt).toLocaleString()}</td><td className="mono">{event.actorId}</td><td><span className="tag">{event.action}</span></td><td>{event.targetType}: <span className="mono">{event.targetId}</span></td><td>{event.before && <span>{event.before} → </span>}{event.after}</td></tr>)}</tbody></table></section></>;
}

const themeOptions = [
  { id: "midnight", name: "Midnight", description: "Graphite surfaces and a calm blue accent.", colors: ["#0d1117", "#161b22", "#21262d", "#58a6ff", "#f778ba"] },
  { id: "aurora", name: "Aurora", description: "Violet accents and sharp geometry.", colors: ["#0a0a12", "#14141f", "#8b5cf6", "#22d3ee"] },
  { id: "ocean", name: "Ocean", description: "Deep blue with a warm sand accent.", colors: ["#14293a", "#00508f", "#addbff", "#ffcd82"] },
  { id: "retro", name: "Retro", description: "Neutral grey, soft gold and rounded panels.", colors: ["#1c1c1c", "#262626", "#f2d399", "#72817e"] },
  { id: "autumn", name: "Autumn", description: "Warm terracotta on neutral dark surfaces.", colors: ["#242424", "#303030", "#d9734e", "#96a680"] },
  { id: "light", name: "Light", description: "Cream background with an orange accent.", colors: ["#f2edd5", "#ffffff", "#f26716", "#084a24"] },
  { id: "cyberpunk", name: "Cyberpunk", description: "Neon cyan and magenta on dark violet.", colors: ["#05010d", "#1a0b38", "#00f0ff", "#ff2ea6"] },
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
