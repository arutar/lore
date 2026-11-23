import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { Link, NavLink, Navigate, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { api, isDemo, onUnauthorized, setCSRFToken } from "./api";
import { LocaleContext, makeLocale, useLocale } from "./i18n";
import type { GlobalSettings, PublicConfig, Repository, Session } from "./types";
import { CopyButton, Icon, RepoTile, Avatar, ToastProvider, TopBarContext, useToast } from "./ui";
import type { TopBarState } from "./ui";
import { Home } from "./pages/Home";
import { RepositoryPage } from "./pages/Repository";
import { UsersPage } from "./pages/Users";
import { AuditPage } from "./pages/Audit";
import { SettingsPage, themes } from "./pages/Settings";
import { McpPage } from "./pages/Mcp";
import { NewRepositoryDialog } from "./pages/NewRepository";

export interface AppState {
  session: Session;
  config: PublicConfig;
  isAdmin: boolean;
  /** The lore:// address a client clones this repository from. */
  repositoryUrl: (repository: Repository) => string;
  /** Reloads the repository list, after a change the list should show. */
  refreshRepositories: () => void;
  openNewRepository: () => void;
  setConfig: (config: PublicConfig) => void;
}

const AppContext = createContext<AppState | null>(null);
export const useApp = () => useContext(AppContext)!;

export default function App() {
  const [session, setSession] = useState<Session>({ authenticated: false });
  const [config, setConfig] = useState<PublicConfig | null>(null);
  const [failed, setFailed] = useState(false);
  // The session ended while the page was open: every request is now refused until a new sign-in.
  const [expired, setExpired] = useState(false);
  useEffect(() => {
    onUnauthorized(() => {
      setCSRFToken(undefined);
      setSession({ authenticated: false });
      setExpired(true);
    });
    return () => onUnauthorized(null);
  }, []);

  const load = useCallback(async () => {
    try {
      const [nextSession, nextConfig] = await Promise.all([api.session(), api.config()]);
      setCSRFToken(nextSession.csrfToken);
      setSession(nextSession);
      setConfig(nextConfig);
      setFailed(false);
    } catch {
      setFailed(true);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  const language = config?.language ?? (navigator.language.startsWith("ru") ? "ru" : "en");
  const locale = useMemo(() => makeLocale(language), [language]);
  useEffect(() => {
    document.documentElement.dataset.theme = config?.theme || "midnight";
    document.documentElement.lang = language;
  }, [config?.theme, language]);

  return <LocaleContext.Provider value={locale}>
    <ToastProvider>
      {config ? <Shell session={session} config={config} setConfig={setConfig} expired={expired} />
        : <div className="boot">{failed ? <><p>{locale.t("app.unavailable")}</p><button className="btn" onClick={() => void load()}>{locale.t("app.retry")}</button></> : <p>{locale.t("app.loading")}</p>}</div>}
    </ToastProvider>
  </LocaleContext.Provider>;
}

function repositoryUrlFor(config: PublicConfig, repository: Repository) {
  const configured = config.nativeRepositoryUrl || `lore://${window.location.hostname}:41337`;
  const url = new URL(configured.replace(/\/$/, ""));
  if (url.port === "41337") url.port = "";
  // Encode each segment: the "/" between segments is part of the name (team/project).
  return `${url.toString().replace(/\/$/, "")}/${repository.name.split("/").map(encodeURIComponent).join("/")}`;
}

function Shell({ session, config, setConfig, expired }: { session: Session; config: PublicConfig; setConfig: (config: PublicConfig) => void; expired: boolean }) {
  const { t } = useLocale();
  const location = useLocation();
  const [topBar, setTopBar] = useState<TopBarState>({});
  const [listVersion, setListVersion] = useState(0);
  const [creating, setCreating] = useState(false);
  const [palette, setPalette] = useState(false);
  const isAdmin = session.user?.role === "admin";
  const state: AppState = useMemo(() => ({
    session, config, isAdmin, setConfig,
    repositoryUrl: (repository) => repositoryUrlFor(config, repository),
    refreshRepositories: () => setListVersion((version) => version + 1),
    openNewRepository: () => setCreating(true),
  }), [session, config, isAdmin, setConfig]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") { event.preventDefault(); setPalette(true); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Set by the sign-in callback when the account may not use Lore (no access, disabled).
  // After a refused sign-in the identity provider's logout returns the reason as state=signin:<reason>.
  const [signinProblem, setSigninProblem] = useState(() => {
    const params = new URLSearchParams(window.location.search);
    const state = params.get("state") ?? "";
    return params.get("signin") ?? (state.startsWith("signin:") ? state.slice("signin:".length) : null);
  });
  useEffect(() => {
    if (!signinProblem) return;
    const url = new URL(window.location.href);
    url.searchParams.delete("signin");
    url.searchParams.delete("state");
    window.history.replaceState(window.history.state, "", url.pathname + url.search + url.hash);
  }, [signinProblem]);

  const wide = (isAdmin && /^\/admin\//.test(location.pathname)) || location.pathname === "/mcp-tokens";
  const home = location.pathname === "/";
  const nav = [{ to: "/", label: t("nav.repositories"), icon: "repo" as const, end: false },
    ...(isAdmin ? [{ to: "/admin/users", label: t("nav.users"), icon: "users" as const, end: true },
      { to: "/admin/audit", label: t("nav.audit"), icon: "audit" as const, end: true },
      { to: "/admin/settings", label: t("nav.settings"), icon: "settings" as const, end: true }] : []),
    ...(config.mcpEnabled && session.mcpAllowed ? [{ to: "/mcp-tokens", label: t("nav.mcp"), icon: "plug" as const, end: true }] : [])];

  return <AppContext.Provider value={state}>
    <TopBarContext.Provider value={setTopBar}>
      <div className={`shell ${wide ? "wide" : ""} ${home ? "home" : ""}`}>
        <nav className="rail" aria-label="Lore">
          <Link className="brand" to="/" aria-label="Lore"><img src={`${import.meta.env.BASE_URL}icons/lore-mark.svg`} alt="" /></Link>
          {nav.map((item) => <NavLink key={item.to} to={item.to} end={item.end}
            className={({ isActive }) => (isActive || (item.to === "/" && location.pathname.startsWith("/repositories/")) ? "on" : "")}
            data-tip={item.label} aria-label={item.label}><Icon name={item.icon} /></NavLink>)}
          <span className="sp" />
          <button data-tip={t("app.search")} aria-label={t("app.search")} onClick={() => setPalette(true)}><Icon name="search" /></button>
        </nav>
        {!wide && <RepositoryList version={listVersion} />}
        <div className="detail">
          {isDemo && <DemoBar />}
          <div className="top">
            <span className="grow top-title">{topBar.title && <b>{topBar.title}</b>}{topBar.description && <span className="truncate">{topBar.description}</span>}</span>
            {topBar.actions}
            <Account session={session} config={config} />
          </div>
          <main className="content">
            {signinProblem && (signinProblem === "no_access" || signinProblem === "disabled") && <div className="note warn signin-note" role="alert">
              <Icon name="lock" /><span className="grow">{t(`signin.${signinProblem}` as "signin.no_access")}</span>
              <button className="btn quiet icon sm" onClick={() => setSigninProblem(null)} aria-label={t("common.close")}><Icon name="x" /></button></div>}
            {expired ? <SessionEnded config={config} /> : <Routes>
              <Route path="/" element={<Home />} />
              <Route path="/repositories/:id" element={<RepositoryPage />} />
              <Route path="/repositories/:id/:tab" element={<RepositoryPage />} />
              <Route path="/admin/users" element={isAdmin ? <UsersPage /> : <Navigate to="/" />} />
              <Route path="/admin/audit" element={isAdmin ? <AuditPage /> : <Navigate to="/" />} />
              <Route path="/admin/settings" element={isAdmin ? <SettingsPage /> : <Navigate to="/" />} />
              <Route path="/mcp-tokens" element={session.authenticated ? <McpPage /> : <Navigate to="/" />} />
              <Route path="*" element={<Navigate to="/" />} />
            </Routes>}
          </main>
        </div>
      </div>
      {creating && <NewRepositoryDialog onClose={() => setCreating(false)} />}
      {palette && <Palette onClose={() => setPalette(false)} />}
    </TopBarContext.Provider>
  </AppContext.Provider>;
}

// The demo's strip above the top bar: what this is, every theme one click away, the other
// language, and the source. Theme and language are kept in this browser (demo/api.ts).
function DemoBar() {
  const { t } = useLocale();
  const { config, setConfig } = useApp();
  const apply = (change: Partial<GlobalSettings>) => {
    const next: GlobalSettings = { registrationEnabled: config.registrationEnabled, theme: config.theme, language: config.language, ...change };
    setConfig({ ...config, ...next });
    void api.saveSettings(next);
  };
  return <div className="demo-bar" role="note">
    <span className="demo-badge">{t("demo.badge")}</span>
    <span className="grow truncate demo-text">{t("demo.text")}</span>
    <span className="demo-themes" role="group" aria-label={t("demo.themes")}>{themes.map(([id, name, background, , accent]) =>
      <button key={id} title={name} aria-label={name} aria-pressed={config.theme === id} className={config.theme === id ? "on" : ""}
        style={{ background: `linear-gradient(135deg, ${background} 50%, ${accent} 50%)` }} onClick={() => apply({ theme: id })} />)}</span>
    <button className="demo-lang" onClick={() => apply({ language: config.language === "ru" ? "en" : "ru" })}>{config.language === "ru" ? "EN" : "RU"}</button>
    <a className="demo-link" href="https://github.com/arutar/lore" target="_blank" rel="noreferrer">GitHub</a>
  </div>;
}

/** The sign-in address that comes back to the page open now. */
function signInHref() {
  const back = window.location.pathname + window.location.search;
  return back === "/" ? "/web/v1/login" : `/web/v1/login?return=${encodeURIComponent(back)}`;
}

// Shown in place of the page once the session has ended: signing in again comes back here.
function SessionEnded({ config }: { config: PublicConfig }) {
  const { t } = useLocale();
  return <div className="note warn" role="alert"><Icon name="lock" /><span className="grow">{t("session.ended")}</span>
    {config.oidcEnabled && <a className="btn primary sm" href={signInHref()}>{t("session.signInAgain")}</a>}
    {!config.oidcEnabled && config.devAuthEnabled && <button className="btn primary sm" onClick={() => void api.devLogin().then(() => window.location.reload())}>{t("session.signInAgain")}</button>}
  </div>;
}

function Account({ session, config }: { session: Session; config: PublicConfig }) {
  const { t } = useLocale();
  const toast = useToast();
  if (!session.authenticated || !session.user) {
    return <div className="row">
      {config.devAuthEnabled && <button className="btn sm" onClick={() => void api.devLogin().then(() => window.location.reload())}>{t("app.devSignIn")}</button>}
      {config.oidcEnabled && <a className="btn primary sm" href={signInHref()}>{t("app.signIn")}</a>}
    </div>;
  }
  const user = session.user;
  const name = user.displayName || user.username;
  const signOut = async () => {
    try {
      const result = await api.logout();
      setCSRFToken(undefined);
      if (result.logoutUrl) window.location.assign(result.logoutUrl);
      else window.location.assign("/");
    } catch (cause) {
      toast(String(cause), { error: true });
    }
  };
  return <>
    <div className="user-chip"><Avatar name={name} /><span>{name} <small>· {user.role === "admin" ? t("app.admin") : t("app.user")}</small></span></div>
    <button className="btn quiet icon" onClick={() => void signOut()} title={t("app.signOut")} aria-label={t("app.signOut")}><Icon name="out" /></button>
  </>;
}

/** The repository list beside every page: server-side filter, paging, copy of the address. */
function RepositoryList({ version }: { version: number }) {
  const { t, ago, error } = useLocale();
  const { repositoryUrl } = useApp();
  const location = useLocation();
  const [query, setQuery] = useState("");
  const [items, setItems] = useState<Repository[]>([]);
  const [cursor, setCursor] = useState("");
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState("");

  const load = useCallback(async (q: string, after: string) => {
    setLoading(true);
    try {
      const page = await api.catalog(q, after);
      setItems((current) => (after ? [...current, ...page.items] : page.items));
      setCursor(page.nextCursor ?? "");
      setProblem("");
    } catch (cause) {
      setProblem(error(cause));
    } finally {
      setLoading(false);
    }
  }, [error]);

  useEffect(() => {
    const timer = window.setTimeout(() => void load(query.trim(), ""), query ? 250 : 0);
    return () => window.clearTimeout(timer);
  }, [query, version, load]);

  const current = location.pathname.match(/^\/repositories\/([^/]+)/)?.[1];
  return <section className="list-pane" aria-label={t("list.title")}>
    <header>
      <div className="row"><h2 className="grow">{t("list.title")}</h2><span className="badge">{items.length}{cursor ? "+" : ""}</span></div>
      <label className="search"><Icon name="search" /><input className="input" value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("list.filter")} aria-label={t("list.filter")} /></label>
    </header>
    <div className="items">
      {problem && <div className="note warn"><Icon name="warn" /><span>{problem}</span></div>}
      {items.map((repository) => <div key={repository.id} className={`list-item ${current === repository.id ? "on" : ""}`}>
        <Link className="li-link" to={`/repositories/${repository.id}`}>
          <RepoTile name={repository.name} />
          <span className="grow">
            <b className="truncate">{repository.name}</b>
            <small className={`sub ${repository.public ? "pub" : ""}`}>
              <span className="v"><Icon name={repository.public ? "globe" : "lock"} />{repository.public ? t("list.public") : t("list.private")}</span>
              {repository.updatedAt && <span className="truncate">· {ago(repository.updatedAt)}</span>}
            </small>
          </span>
        </Link>
        <span className="li-side"><CopyButton small text={repositoryUrl(repository)} label={t("list.copy", { url: repositoryUrl(repository) })} message={t("repo.copied", { url: repositoryUrl(repository) })} /></span>
      </div>)}
      {!loading && !problem && items.length === 0 && <div className="empty"><p>{query ? t("list.empty") : t("list.none")}</p></div>}
      {cursor && <button className="btn sm more" disabled={loading} onClick={() => void load(query.trim(), cursor)}>{t("list.more")}</button>}
    </div>
  </section>;
}

/** Ctrl K: jump to a repository or a section. */
function Palette({ onClose }: { onClose: () => void }) {
  const { t } = useLocale();
  const { isAdmin } = useApp();
  const navigate = useNavigate();
  const [query, setQuery] = useState("");
  const [repositories, setRepositories] = useState<Repository[]>([]);
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => { input.current?.focus(); }, []);
  useEffect(() => {
    const timer = window.setTimeout(() => { api.catalog(query.trim()).then((page) => setRepositories(page.items.slice(0, 8))).catch(() => setRepositories([])); }, 150);
    return () => window.clearTimeout(timer);
  }, [query]);
  const sections = isAdmin ? [["/admin/users", t("nav.users"), "users"], ["/admin/audit", t("nav.audit"), "audit"], ["/admin/settings", t("nav.settings"), "settings"]] as const : [];
  const results = [
    ...repositories.map((repository) => ({ to: `/repositories/${repository.id}`, label: repository.name, detail: repository.description, icon: "repo" as const })),
    ...sections.filter(([, label]) => !query || label.toLowerCase().includes(query.toLowerCase())).map(([to, label, icon]) => ({ to, label, detail: "", icon })),
  ];
  const go = (to: string) => { onClose(); navigate(to); };
  return <div className="overlay" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <div className="dialog palette" role="dialog" aria-label={t("app.search")}>
      <label className="search"><Icon name="search" /><input ref={input} className="input" value={query} placeholder={t("app.search")}
        onChange={(event) => { setQuery(event.target.value); setActive(0); }}
        onKeyDown={(event) => {
          if (event.key === "Escape") onClose();
          if (event.key === "ArrowDown") { event.preventDefault(); setActive((index) => Math.min(index + 1, results.length - 1)); }
          if (event.key === "ArrowUp") { event.preventDefault(); setActive((index) => Math.max(index - 1, 0)); }
          if (event.key === "Enter" && results[active]) go(results[active].to);
        }} /></label>
      <div className="results">
        {results.length === 0 ? <div className="empty"><p>{t("list.empty")}</p></div> : results.map((result, index) =>
          <button key={result.to} className={`combo-item ${index === active ? "on" : ""}`} onMouseEnter={() => setActive(index)} onClick={() => go(result.to)}>
            <Icon name={result.icon} className="file-ico" /><span className="grow"><b>{result.label}</b>{result.detail && <small className="truncate">{result.detail}</small>}</span>{index === active && <kbd>Enter</kbd>}
          </button>)}
      </div>
    </div>
  </div>;
}
