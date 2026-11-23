import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { useApp } from "../App";
import { useLocale } from "../i18n";
import type { MCPToken, Repository } from "../types";
import { Avatar, Badge, CopyButton, Dialog, Empty, ErrorNote, Icon, Loading, useToast, useTopBar } from "../ui";

/** The ways to connect an agent, with the address and a token (or a placeholder) filled in. */
const examples = (url: string, token: string) => ({
  claude: `claude mcp add --transport http lore ${url} --header "Authorization: Bearer ${token}"`,
  json: JSON.stringify({ mcpServers: { lore: { url, headers: { Authorization: `Bearer ${token}` } } } }, null, 2),
  stdio: JSON.stringify({ mcpServers: { lore: { command: "npx", args: ["-y", "mcp-remote", url, "--header", `Authorization:Bearer ${token}`] } } }, null, 2),
});

type ExampleKind = keyof ReturnType<typeof examples>;

/** Connection examples, one at a time, with a copy button. */
function Examples({ url, token }: { url: string; token: string }) {
  const { t } = useLocale();
  const [kind, setKind] = useState<ExampleKind>("claude");
  const text = examples(url, token)[kind];
  const labels: Record<ExampleKind, string> = { claude: t("mcp.exampleClaude"), json: t("mcp.exampleJson"), stdio: t("mcp.exampleStdio") };
  return <div className="mcp-examples">
    <div className="seg" role="group">{(Object.keys(labels) as ExampleKind[]).map((key) =>
      <button key={key} className={kind === key ? "on" : ""} aria-pressed={kind === key} onClick={() => setKind(key)}>{labels[key]}</button>)}</div>
    {kind === "stdio" && <p className="muted small">{t("mcp.exampleStdioText")}</p>}
    <div className="mcp-code"><pre className="code">{text}</pre><CopyButton small text={text} label={t("mcp.copyExample")} message={t("mcp.exampleCopied")} /></div>
  </div>;
}

/** MCP for AI agents: the server's address, connection examples and the account's tokens; an
 *  administrator also sees everyone's tokens. */
export function McpPage() {
  const { t, ago, error } = useLocale();
  const { session, config, isAdmin } = useApp();
  const toast = useToast();
  const [tokens, setTokens] = useState<MCPToken[] | null>(null);
  // Bumped after every change, so the administrator's list of every token reloads too.
  const [version, setVersion] = useState(0);
  const [url, setUrl] = useState(`${window.location.origin}/mcp`);
  const [problem, setProblem] = useState("");
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<{ token: MCPToken; secret: string } | null>(null);
  const [repositories, setRepositories] = useState<Repository[]>([]);
  const allowed = Boolean(config.mcpEnabled && session.mcpAllowed);
  useTopBar({
    title: t("mcp.title"), description: t("mcp.description"),
    actions: allowed ? <button className="btn primary sm" onClick={() => setCreating(true)}><Icon name="plus" />{t("mcp.create")}</button> : null,
  }, [t, allowed]);

  const load = useCallback(async () => {
    try {
      const mine = await api.mcpTokens();
      setTokens(mine.items);
      setUrl(mine.url);
      setVersion((value) => value + 1);
      setProblem("");
    } catch (cause) {
      setProblem(error(cause));
    }
  }, [error]);
  useEffect(() => {
    if (!allowed) return;
    void load();
    api.catalog("", "").then((page) => setRepositories(page.items)).catch(() => setRepositories([]));
  }, [allowed, load]);

  if (!config.mcpEnabled) return <div className="card"><Empty icon="plug" title={t("mcp.off")} text={t("mcp.offText")} /></div>;
  if (!session.mcpAllowed) return <div className="card"><Empty icon="lock" title={t("mcp.denied")} text={t("mcp.deniedText")} /></div>;

  const names = new Map(repositories.map((repository) => [repository.id, repository.name]));
  const scope = (token: MCPToken) => token.repositories.length === 0 ? t("mcp.allRepositories")
    : token.repositories.map((id) => names.get(id) ?? id).join(", ");
  const revoke = async (token: MCPToken) => {
    if (!window.confirm(t("mcp.revokeConfirm", { name: token.name }))) return;
    try {
      await api.revokeMCPToken(token.id);
      toast(t("mcp.revoked", { name: token.name }));
      void load();
    } catch (cause) {
      toast(error(cause), { error: true });
    }
  };
  const tokenRow = (token: MCPToken) => <div className="mcp-token" key={token.id}>
    <span className="avatar sq" style={{ "--tone": "var(--accent-2)" } as React.CSSProperties}><Icon name="plug" /></span>
    <div className="grow">
      <div><b>{token.name}</b> {token.status && token.status !== "active" && <TokenStatus status={token.status} />}</div>
      <small><code>{token.hint}…</code> · {scope(token)}</small>
      <small>{t("mcp.created", { when: ago(token.createdAt) })} · {token.lastUsedAt ? t("mcp.lastUsed", { when: ago(token.lastUsedAt) }) : t("mcp.neverUsed")}
        {token.expiresAt && <> · {t("mcp.expires", { date: new Date(token.expiresAt).toLocaleDateString() })}</>}</small>
    </div>
    <button className="btn danger sm" onClick={() => void revoke(token)}>{t("mcp.revoke")}</button>
  </div>;

  return <div className="stack">
    <div className="card">
      <div className="card-head"><span className="os-tile connect"><Icon name="plug" /></span>
        <div className="grow"><h2>{t("mcp.connect")}</h2><p>{t("mcp.connectText")}</p></div></div>
      <div className="pad stack">
        <div className="clone"><code>{url}</code><CopyButton small text={url} label={t("mcp.copyAddress")} message={t("mcp.addressCopied")} /></div>
        <Examples url={url} token="lore_mcp_…" />
      </div>
    </div>
    <div className="card">
      <div className="card-head"><div className="grow"><h2>{t("mcp.tokens")}</h2><p>{t("mcp.tokensText")}</p></div></div>
      <ErrorNote message={problem} onRetry={() => void load()} />
      {!tokens ? <Loading /> : tokens.length === 0
        ? <Empty icon="plug" title={t("mcp.empty")} text={t("mcp.emptyText")}><button className="btn primary" onClick={() => setCreating(true)}><Icon name="plus" />{t("mcp.create")}</button></Empty>
        : <div className="mcp-tokens">{tokens.map(tokenRow)}</div>}
    </div>
    {isAdmin && <AllTokens scope={scope} version={version} onRevoked={() => void load()} />}
    {creating && <CreateTokenDialog repositories={repositories} onClose={() => setCreating(false)}
      onCreated={(result) => { setCreating(false); setCreated(result); void load(); }} />}
    {created && <Dialog title={t("mcp.secretTitle")} onClose={() => setCreated(null)} width={640}
      footer={<button className="btn primary" onClick={() => setCreated(null)}>{t("mcp.done")}</button>}>
      <div className="note warn"><Icon name="warn" /><span>{t("mcp.secretText")}</span></div>
      <div className="clone"><code>{created.secret}</code><CopyButton small text={created.secret} label={t("mcp.copySecret")} message={t("mcp.secretCopied")} /></div>
      <Examples url={url} token={created.secret} />
    </Dialog>}
  </div>;
}

/** Whether a token works now, and if not, why. */
function TokenStatus({ status }: { status: NonNullable<MCPToken["status"]> }) {
  const { t } = useLocale();
  const kind = status === "active" ? "ok" : status === "expired" ? "" : "warn";
  return <Badge kind={kind} title={t(`mcp.statusHint.${status}` as "mcp.statusHint.active")}>{t(`mcp.status.${status}` as "mcp.status.active")}</Badge>;
}

/** Every account's tokens for an administrator: searched by the token's name or its owner, a page
 *  at a time, with whether each works now and a revoke button. */
function AllTokens({ scope, version, onRevoked }: { scope: (token: MCPToken) => string; version: number; onRevoked: () => void }) {
  const { t, ago, error } = useLocale();
  const toast = useToast();
  const [query, setQuery] = useState("");
  const [items, setItems] = useState<MCPToken[] | null>(null);
  const [cursor, setCursor] = useState("");
  const [loading, setLoading] = useState(false);
  const [problem, setProblem] = useState("");
  const load = useCallback(async (q: string, after: string) => {
    setLoading(true);
    try {
      const page = await api.allMCPTokens(q, after);
      setItems((current) => (after && current ? [...current, ...page.items] : page.items));
      setCursor(page.nextCursor ?? "");
      setProblem("");
    } catch (cause) {
      setProblem(error(cause));
    } finally {
      setLoading(false);
    }
  }, [error]);
  useEffect(() => {
    const timer = window.setTimeout(() => void load(query.trim(), ""), 250);
    return () => window.clearTimeout(timer);
  }, [query, version, load]);
  const revoke = async (token: MCPToken) => {
    if (!window.confirm(t("mcp.revokeConfirmOf", { name: token.name, user: token.userName || token.userLogin || "" }))) return;
    try {
      await api.adminRevokeMCPToken(token.id);
      toast(t("mcp.revoked", { name: token.name }));
      onRevoked();
    } catch (cause) {
      toast(error(cause), { error: true });
    }
  };
  return <div className="card">
    <div className="card-head"><div className="grow"><h2>{t("mcp.allTokens")}</h2><p>{t("mcp.allTokensText")}</p></div></div>
    <div className="pad mcp-search">
      <label className="search"><Icon name="search" /><input className="input" value={query} onChange={(event) => setQuery(event.target.value)}
        placeholder={t("mcp.searchTokens")} aria-label={t("mcp.searchTokens")} /></label>
    </div>
    <ErrorNote message={problem} onRetry={() => void load(query.trim(), "")} />
    {!items ? <Loading /> : items.length === 0 ? <Empty icon="search" title={query ? t("mcp.noMatch") : t("mcp.empty")} />
      : <div className="table-wrap"><table className="table stacked mcp-all">
        <thead><tr><th>{t("mcp.colUser")}</th><th>{t("mcp.colToken")}</th><th>{t("mcp.scope")}</th><th>{t("mcp.colUsed")}</th><th>{t("mcp.colStatus")}</th><th className="act" /></tr></thead>
        <tbody>{items.map((token) => <tr key={token.id}>
          <td><div className="cell-main"><Avatar name={token.userName || token.userLogin || "?"} /><div className="grow">
            <b>{token.userName || token.userLogin}</b><small className="truncate">{token.userLogin}</small></div></div></td>
          <td><b>{token.name}</b><small className="mono"> {token.hint}…</small></td>
          <td className="muted">{scope(token)}</td>
          <td className="muted nowrap" title={t("mcp.created", { when: ago(token.createdAt) })}>{token.lastUsedAt ? ago(token.lastUsedAt) : t("mcp.neverUsed")}
            {token.expiresAt && <small className="muted"> · {t("mcp.expires", { date: new Date(token.expiresAt).toLocaleDateString() })}</small>}</td>
          <td>{token.status && <TokenStatus status={token.status} />}</td>
          <td className="act"><button className="btn danger sm" title={t("mcp.revoke")} onClick={() => void revoke(token)}><Icon name="x" />{t("mcp.revoke")}</button></td>
        </tr>)}</tbody>
      </table></div>}
    {cursor && <div className="table-foot"><span /><button className="btn sm" disabled={loading} onClick={() => void load(query.trim(), cursor)}>{t("mcp.more")}</button></div>}
  </div>;
}

function CreateTokenDialog({ repositories, onClose, onCreated }: { repositories: Repository[]; onClose: () => void; onCreated: (result: { token: MCPToken; secret: string }) => void }) {
  const { t, error } = useLocale();
  const [name, setName] = useState("");
  const [days, setDays] = useState(0);
  const [limited, setLimited] = useState(false);
  const [chosen, setChosen] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");
  const submit = async () => {
    if (limited && chosen.size === 0) {
      setProblem(t("mcp.selectRepositories"));
      return;
    }
    setBusy(true);
    try {
      onCreated(await api.createMCPToken({ name: name.trim(), repositories: limited ? [...chosen] : [], expiresInDays: days }));
    } catch (cause) {
      setProblem(error(cause));
    } finally {
      setBusy(false);
    }
  };
  const toggle = (id: string) => setChosen((current) => {
    const next = new Set(current);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });
  return <Dialog title={t("mcp.create")} onClose={onClose} footer={<>
    <button type="button" className="btn" onClick={onClose}>{t("common.cancel")}</button>
    <button type="button" className="btn primary" disabled={busy || !name.trim()} onClick={() => void submit()}>{t("mcp.create")}</button>
  </>}>
    <ErrorNote message={problem} />
    <label className="field"><span>{t("mcp.name")}</span>
      <input className="input" value={name} maxLength={80} placeholder={t("mcp.namePlaceholder")} onChange={(event) => setName(event.target.value)}
        onKeyDown={(event) => { if (event.key === "Enter" && name.trim()) void submit(); }} /></label>
    <label className="field"><span>{t("mcp.expiry")}</span>
      <select className="select" value={days} onChange={(event) => setDays(Number(event.target.value))}>
        <option value={0}>{t("mcp.never")}</option>
        {[30, 90, 365].map((count) => <option key={count} value={count}>{t("mcp.days", { count })}</option>)}
      </select></label>
    <div className="field"><span>{t("mcp.scope")}</span>
      <div className="seg" role="group">
        <button type="button" className={!limited ? "on" : ""} aria-pressed={!limited} onClick={() => setLimited(false)}>{t("mcp.scopeAll")}</button>
        <button type="button" className={limited ? "on" : ""} aria-pressed={limited} onClick={() => setLimited(true)}>{t("mcp.scopeSome")}</button>
      </div>
      {limited && <div className="mcp-repositories">{repositories.map((repository) =>
        <label key={repository.id} className="check"><input type="checkbox" checked={chosen.has(repository.id)} onChange={() => toggle(repository.id)} />{repository.name}</label>)}</div>}
    </div>
  </Dialog>;
}
