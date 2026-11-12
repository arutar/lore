import { useEffect, useRef, useState } from "react";
import cloneIcon from "../../shared/svg/clone.svg?url";
import commitIcon from "../../shared/svg/commit.svg?url";
import errorIcon from "../../shared/svg/error.svg?url";
import folderIcon from "../../shared/svg/folder-open.svg?url";
import historyIcon from "../../shared/svg/history.svg?url";
import lockIcon from "../../shared/svg/lock.svg?url";
import loreIcon from "../../shared/svg/lore.svg?url";
import stageIcon from "../../shared/svg/stage.svg?url";
import statusIcon from "../../shared/svg/status.svg?url";
import successIcon from "../../shared/svg/success.svg?url";
import syncIcon from "../../shared/svg/sync.svg?url";
import unlockIcon from "../../shared/svg/unlock.svg?url";

const Operation = { Clone: 0, Sync: 1, Commit: 2, Stage: 3, Status: 4, History: 5, Lock: 6, Unlock: 7, Accounts: 8 } as const;
type OperationValue = typeof Operation[keyof typeof Operation];
type StoredAccount = { authUrl: string; userId: string; username: string; domains: string; selected: boolean };

type InitMessage = {
  type: "init";
  operation: OperationValue;
  repositoryUrl: string;
  repositoryPath: string;
  targetPath: string;
  message: string;
  paths: string[];
  theme: string;
};
type HostMessage = InitMessage
  | { type: "progress"; text: string }
  | { type: "complete"; text: string }
  | { type: "error"; text: string; accessDenied?: boolean }
  | { type: "accounts"; items: StoredAccount[] }
  | { type: "directory"; path: string };
type ClientMessage = { type: "ready" } | { type: "browse" } | { type: "cancel" } | { type: "accounts-open" }
  | { type: "accounts-add"; remoteUrl: string } | { type: "accounts-select"; userId: string }
  | { type: "accounts-remove"; authUrl: string; userId: string }
  | { type: "run"; repositoryUrl: string; targetPath: string; message: string };

interface WebViewBridge {
  postMessage(message: ClientMessage): void;
  addEventListener(type: "message", listener: (event: MessageEvent<HostMessage>) => void): void;
  removeEventListener(type: "message", listener: (event: MessageEvent<HostMessage>) => void): void;
}
declare global { interface Window { chrome?: { webview?: WebViewBridge } } }

const info: Record<OperationValue, { title: string; subtitle: string; action: string; icon: string }> = {
  [Operation.Clone]: { title: "Clone repository", subtitle: "Create a local copy of a Lore repository.", action: "Clone", icon: cloneIcon },
  [Operation.Sync]: { title: "Sync repository", subtitle: "Update the working copy from the Lore server.", action: "Sync", icon: syncIcon },
  [Operation.Commit]: { title: "Commit changes", subtitle: "Record selected changes in a new revision.", action: "Commit", icon: commitIcon },
  [Operation.Stage]: { title: "Stage paths", subtitle: "Prepare selected files for the next commit.", action: "Stage", icon: stageIcon },
  [Operation.Status]: { title: "Repository status", subtitle: "Inspect the current working copy.", action: "Refresh", icon: statusIcon },
  [Operation.History]: { title: "Revision history", subtitle: "Recent revisions in this repository.", action: "Refresh", icon: historyIcon },
  [Operation.Lock]: { title: "Acquire locks", subtitle: "Prevent conflicting edits to selected files.", action: "Acquire locks", icon: lockIcon },
  [Operation.Unlock]: { title: "Release locks", subtitle: "Release locks held on selected files.", action: "Release locks", icon: unlockIcon },
  [Operation.Accounts]: { title: "Saved accounts", subtitle: "Manage Lore servers and stored sign-ins.", action: "Add account", icon: statusIcon },
};
const post = (message: ClientMessage) => window.chrome?.webview?.postMessage(message);

export default function App() {
  const [config, setConfig] = useState<InitMessage | null>(null);
  const operationRef = useRef<OperationValue>(Operation.Clone);
  const [repositoryUrl, setRepositoryUrl] = useState("");
  const [targetPath, setTargetPath] = useState("");
  const [message, setMessage] = useState("");
  const [running, setRunning] = useState(false);
  const [logs, setLogs] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [accessDenied, setAccessDenied] = useState(false);
  const [success, setSuccess] = useState("");
  const [statuses, setStatuses] = useState<Record<string, number>>({});
  const [revisions, setRevisions] = useState<{ number: string; id: string }[]>([]);
  const [accounts, setAccounts] = useState<StoredAccount[]>([]);

  const start = (current: InitMessage) => {
    setRunning(true);
    setLogs([]);
    setError("");
    setAccessDenied(false);
    setSuccess("");
    setStatuses({});
    setRevisions([]);
    post({ type: "run", repositoryUrl: current.repositoryUrl, targetPath: current.targetPath, message: current.message });
  };

  useEffect(() => {
    const receive = (event: MessageEvent<HostMessage>) => {
      const data = event.data;
      if (data.type === "init") {
        document.documentElement.dataset.theme = data.theme;
        operationRef.current = data.operation;
        setConfig(data);
        setRepositoryUrl(data.repositoryUrl);
        setTargetPath(data.targetPath);
        setMessage(data.message);
        if (data.operation !== Operation.Clone && data.operation !== Operation.Commit && data.operation !== Operation.Accounts) start(data);
      } else if (data.type === "accounts") {
        setAccounts(data.items);
      } else if (data.type === "progress") {
        setLogs((current) => [...current, data.text]);
        if (operationRef.current === Operation.Status) {
          const match = data.text.match(/:\s*(Modified|StagedModified|Staged|Conflicted|Locked|Ignored)\s*$/);
          if (match) setStatuses((current) => ({ ...current, [match[1]]: (current[match[1]] ?? 0) + 1 }));
        }
        if (operationRef.current === Operation.History) {
          const match = data.text.match(/^Revision\s+(\d+):\s*(.+)$/);
          if (match) setRevisions((current) => [...current, { number: match[1], id: match[2] }]);
        }
      } else if (data.type === "directory") {
        setTargetPath(data.path);
      } else if (data.type === "complete") {
        setRunning(false);
        setSuccess(data.text);
      } else if (data.type === "error") {
        setRunning(false);
        setError(data.text);
        setAccessDenied(Boolean(data.accessDenied));
      }
    };
    window.chrome?.webview?.addEventListener("message", receive);
    post({ type: "ready" });
    return () => window.chrome?.webview?.removeEventListener("message", receive);
  }, []);

  if (!config && error) return <main className="dialog">
    <header className="header"><img className="operation-icon" src={loreIcon} alt="" /><div><h1>Lore Companion</h1><p>The operation could not be initialized.</p></div></header>
    <div className="alert error" role="alert"><img src={errorIcon} alt="" /><span>{error}</span></div>
    <footer className="actions"><button className="button secondary" onClick={() => post({ type: "cancel" })}>Close</button></footer>
  </main>;
  if (!config) return <main className="loading"><img src={loreIcon} alt="" /><span>Loading Lore Companion…</span></main>;

  const current = info[config.operation];
  const addAccount = (event: React.FormEvent) => {
    event.preventDefault();
    setRunning(true);
    setError("");
    setSuccess("");
    setLogs([]);
    post({ type: "accounts-add", remoteUrl: repositoryUrl });
  };
  if (config.operation === Operation.Accounts) return <main className="dialog">
    <header className="header"><img className="operation-icon" src={loreIcon} alt="" /><div><h1>{current.title}</h1><p>{current.subtitle}</p></div></header>
    <form className="account-form" onSubmit={addAccount}>
      <label><span>Lore server</span><span className="directory-row"><input required disabled={running} value={repositoryUrl}
        onChange={(event) => setRepositoryUrl(event.target.value)} placeholder="lore://localhost" />
        <button className="button primary" disabled={running}>{running ? "Signing in…" : "Add account"}</button></span></label>
    </form>
    <div className="account-list">
      {accounts.length === 0 ? <div className="summary">No saved accounts. Enter a Lore server URL to sign in.</div> : accounts.map((account) => <div className="account-row" key={`${account.authUrl}:${account.userId}`}>
        <div className="account-info"><strong>{account.username || "Unknown user"}</strong><span>{account.domains || account.authUrl.replace(/^oidc\+/, "")}</span></div>
        {account.selected ? <span className="selected-account">In use</span> : <button className="button secondary" disabled={running} onClick={() => { setRunning(true); setError(""); setSuccess(""); post({ type: "accounts-select", userId: account.userId }); }}>Use</button>}
        <button className="button danger" disabled={running} title="Remove this saved login and its repository tokens" onClick={() => { setRunning(true); setError(""); setSuccess(""); post({ type: "accounts-remove", authUrl: account.authUrl, userId: account.userId }); }}>Remove</button>
      </div>)}
    </div>
    {running && <div className="progress" role="progressbar" aria-label="Waiting for browser sign-in"><span /></div>}
    {error && <div className="alert error" role="alert"><img src={errorIcon} alt="" /><span>{error}</span></div>}
    {success && <div className="alert success" role="status"><img src={successIcon} alt="" /><span>{success}</span></div>}
    {logs.length > 0 && <pre className="output" aria-live="polite">{logs.join("\n")}</pre>}
    <footer className="actions"><button className="button secondary" disabled={running} onClick={() => post({ type: "cancel" })}>Close</button></footer>
  </main>;
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!window.chrome?.webview) {
      setError("The WebView2 bridge is unavailable.");
      return;
    }
    start({ ...config, repositoryUrl, targetPath, message });
  };
  const hasPaths = config.operation === Operation.Commit || config.operation === Operation.Stage
    || config.operation === Operation.Lock || config.operation === Operation.Unlock;
  const statusCount = (name: string) => statuses[name] ?? 0;

  return (
    <main className="dialog">
      <header className="header">
        <img className="operation-icon" src={current.icon} alt="" />
        <div><h1>{current.title}</h1><p>{current.subtitle}</p></div>
      </header>
      <form onSubmit={submit}>
        {config.operation === Operation.Clone ? <>
          <label><span>Repository URL</span><input autoFocus required disabled={running || Boolean(success)} value={repositoryUrl}
            onChange={(event) => setRepositoryUrl(event.target.value)} placeholder="https://lore.example.com/repository" /></label>
          <label><span>Target folder</span><span className="directory-row"><input required disabled={running || Boolean(success)} value={targetPath}
            onChange={(event) => setTargetPath(event.target.value)} placeholder="D:\work\repository" />
            <button className="button browse" type="button" disabled={running || Boolean(success)} onClick={() => post({ type: "browse" })}>
              <img src={folderIcon} alt="" />Browse</button></span></label>
        </> : <label><span>Repository</span><input value={config.repositoryPath} readOnly /></label>}

        {config.operation === Operation.Sync && <div className="summary">The server revision will be downloaded and integrated into this working copy. Local staged changes are preserved whenever possible.</div>}
        {hasPaths && <label><span>Selected paths · {config.paths.length}</span><ul className="path-list">{config.paths.map((path) => <li key={path}>{path}</li>)}</ul></label>}
        {config.operation === Operation.Commit && <label><span>Commit message (optional)</span><textarea disabled={running} value={message}
          onChange={(event) => setMessage(event.target.value)} placeholder="Describe the changes" /></label>}
        {(config.operation === Operation.Stage || config.operation === Operation.Lock || config.operation === Operation.Unlock)
          && <div className="summary">Only the selected paths will be changed. This operation does not create a revision.</div>}

        {config.operation === Operation.Status && <>
          <div className="status-grid">
            <div className="status-card"><strong>{statusCount("Modified") + statusCount("StagedModified")}</strong><span>Modified</span></div>
            <div className="status-card"><strong>{statusCount("Staged") + statusCount("StagedModified")}</strong><span>Staged</span></div>
            <div className="status-card"><strong>{statusCount("Locked")}</strong><span>Locked</span></div>
          </div>
          {!running && !error && !success && logs.length === 0 && <div className="summary">Select Refresh to scan the working copy.</div>}
        </>}
        {config.operation === Operation.History && (revisions.length > 0
          ? <ul className="history-list">{revisions.map((revision) => <li key={revision.number}><span className="revision">#{revision.number}</span><div className="revision-id">{revision.id}</div></li>)}</ul>
          : <div className="summary">{running ? "Loading revision history…" : "Select Refresh to load revision history."}</div>)}

        {running && <div className="progress" role="progressbar" aria-label="Running operation"><span /></div>}
        {error && <div className="alert error" role="alert"><img src={errorIcon} alt="" /><div className="alert-content"><span>{error}</span>{accessDenied && <button className="button secondary error-action" type="button" onClick={() => post({ type: "accounts-open" })}>Sign in with another account</button>}</div></div>}
        {success && <div className="alert success" role="status"><img src={successIcon} alt="" /><span>{success}</span></div>}
        {logs.length > 0 && config.operation !== Operation.History && <pre className="output" aria-live="polite">{logs.join("\n")}</pre>}

        <footer className="actions">
          {success && config.operation !== Operation.Status && config.operation !== Operation.History ? <button className="button primary" type="button" onClick={() => post({ type: "cancel" })}>OK</button> : <>
            <button className="button secondary" type="button" disabled={running} onClick={() => post({ type: "cancel" })}>{error ? "Close" : "Cancel"}</button>
            <button className="button primary" type="submit" disabled={running}>{running ? "Working…" : error ? "Retry" : current.action}</button>
          </>}
        </footer>
      </form>
    </main>
  );
}
