import { useEffect, useRef, useState } from "react";
import cloneIcon from "../../shared/svg/clone.svg?url";
import commitIcon from "../../shared/svg/commit.svg?url";
import errorIcon from "../../shared/svg/error.svg?url";
import folderIcon from "../../shared/svg/folder-open.svg?url";
import historyIcon from "../../shared/svg/history.svg?url";
import lockIcon from "../../shared/svg/lock.svg?url";
import loreIcon from "../../shared/svg/app.svg?url";
import accountsIcon from "../../shared/svg/accounts.svg?url";
import stageIcon from "../../shared/svg/stage.svg?url";
import statusIcon from "../../shared/svg/status.svg?url";
import successIcon from "../../shared/svg/success.svg?url";
import syncIcon from "../../shared/svg/sync.svg?url";
import unlockIcon from "../../shared/svg/unlock.svg?url";
import unstageIcon from "../../shared/svg/unstage.svg?url";
import revertIcon from "../../shared/svg/revert.svg?url";
import resolveIcon from "../../shared/svg/resolve.svg?url";
import warningIcon from "../../shared/svg/warning.svg?url";

const Operation = { Clone: 0, Sync: 1, Commit: 2, Stage: 3, Status: 4, History: 5, Lock: 6, Unlock: 7, Accounts: 8, Unstage: 9, Revert: 10, Resolve: 11 } as const;
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
// The operation stopped to ask (ChoiceRequired in LoreOperations.cs); nothing it asks about was changed.
type ChoiceMessage = { type: "choice"; kind: "staged" | "both"; files: string[]; text: string; merged: boolean };
type Choice = "commit" | "unstage" | "mine" | "theirs";
// A step that deletes the user's changes waits for this confirmation.
type Confirmation = { files: string[]; text: string; action: string; run: () => void };
// What Commit would record (LoreOperations.CommitCandidates); the user ticks what goes in.
type Change = { path: string; change: "added" | "deleted" | "modified"; staged: boolean; lockedBy: string };
type HostMessage = InitMessage
  | { type: "changes"; items: Change[]; error?: string }
  | { type: "progress"; text: string }
  | { type: "complete"; text: string; warning?: boolean }
  | { type: "error"; text: string; accessDenied?: boolean }
  | ChoiceMessage
  | { type: "accounts"; items: StoredAccount[] }
  | { type: "directory"; path: string }
  | { type: "theme"; theme: string };
type ClientMessage = { type: "ready" } | { type: "browse" } | { type: "cancel" } | { type: "accounts-open" }
  | { type: "accounts-add"; remoteUrl: string } | { type: "accounts-select"; userId: string }
  | { type: "accounts-remove"; authUrl: string; userId: string } | { type: "theme-set"; theme: string }
  | { type: "size"; height: number }
  | { type: "run"; repositoryUrl: string; targetPath: string; message: string; choice?: Choice | ""; files?: string[] };

interface WebViewBridge {
  postMessage(message: ClientMessage): void;
  addEventListener(type: "message", listener: (event: MessageEvent<HostMessage>) => void): void;
  removeEventListener(type: "message", listener: (event: MessageEvent<HostMessage>) => void): void;
}
declare global { interface Window { chrome?: { webview?: WebViewBridge } } }

const info: Record<OperationValue, { title: string; subtitle: string; action: string; icon: string }> = {
  [Operation.Clone]: { title: "Clone repository", subtitle: "Create a local copy of a Lore repository.", action: "Clone", icon: cloneIcon },
  [Operation.Sync]: { title: "Sync repository", subtitle: "Download what others sent to the server and send your commits.", action: "Sync", icon: syncIcon },
  [Operation.Commit]: { title: "Commit changes", subtitle: "Record the selected changes in a new revision and send it to the server.", action: "Commit", icon: commitIcon },
  [Operation.Stage]: { title: "Stage paths", subtitle: "Prepare selected files for the next commit.", action: "Stage", icon: stageIcon },
  [Operation.Status]: { title: "Repository status", subtitle: "Inspect the current working copy.", action: "Refresh", icon: statusIcon },
  [Operation.History]: { title: "Revision history", subtitle: "Recent revisions in this repository.", action: "Refresh", icon: historyIcon },
  [Operation.Lock]: { title: "Acquire locks", subtitle: "Prevent conflicting edits to selected files.", action: "Acquire locks", icon: lockIcon },
  [Operation.Unlock]: { title: "Release locks", subtitle: "Release locks held on selected files.", action: "Release locks", icon: unlockIcon },
  [Operation.Unstage]: { title: "Unstage", subtitle: "Take the selected files out of the next commit. Their changes stay.", action: "Unstage", icon: unstageIcon },
  [Operation.Revert]: { title: "Revert changes", subtitle: "Discard your changes to the selected files and restore the last synced version.", action: "Revert…", icon: revertIcon },
  [Operation.Resolve]: { title: "Resolve conflicts", subtitle: "You and someone else changed the same files. Choose which version to keep.", action: "Resolve", icon: resolveIcon },
  [Operation.Accounts]: { title: "Accounts", subtitle: "One sign-in per Lore server. Signing in as another user signs out the previous one.", action: "Sign in", icon: statusIcon },
};
// The same themes as the WebUI (platform/web/src/themes.css).
const themes: [string, string][] = [["midnight", "Midnight"], ["sky", "Sky"], ["ember", "Ember"], ["sage", "Sage"],
  ["ocean", "Ocean"], ["retro", "Retro"], ["autumn", "Autumn"], ["light", "Light"], ["cyberpunk", "Cyberpunk"]];
const post = (message: ClientMessage) => window.chrome?.webview?.postMessage(message);

// A file's change shows as a stripe at the row's edge and the colour of its name: green new,
// yellow changed, red (and struck out) deleted.
const changeMark: Record<Change["change"], { title: string }> = {
  added: { title: "New file" },
  modified: { title: "Changed" },
  deleted: { title: "Deleted" },
};
// A filter field only where the list is long enough to need one.
const filterFrom = 12;

// The Commit window's list, grouped by folder. A folder with two or more changed files below it
// gets a row of its own: its box takes in or leaves out everything below it, and the arrow folds
// it to that one row. A folder holding only further folders is merged into them ("art\textures"),
// and a lone file stays a single row with its folder after it, dimmed. Files someone else has
// locked can't be committed, so their boxes are off and disabled.
type Folder = { name: string; path: string; folders: Map<string, Folder>; files: Change[] };

function buildTree(changes: Change[]): Folder {
  const root: Folder = { name: "", path: "", folders: new Map(), files: [] };
  for (const change of changes) {
    const parts = change.path.split(/[\\/]/);
    let node = root;
    for (const part of parts.slice(0, -1)) {
      const key = part.toLowerCase();
      let next = node.folders.get(key);
      if (!next) {
        next = { name: part, path: node.path ? `${node.path}\\${part}` : part, folders: new Map(), files: [] };
        node.folders.set(key, next);
      }
      node = next;
    }
    node.files.push(change);
  }
  return root;
}

function filesBelow(folder: Folder): Change[] {
  return [...folder.files, ...[...folder.folders.values()].flatMap(filesBelow)];
}

// "art" holding only "textures" reads as one folder, "art\textures".
function compact(folder: Folder): Folder {
  let current = folder;
  let name = folder.name;
  while (current.files.length === 0 && current.folders.size === 1) {
    current = [...current.folders.values()][0];
    name = `${name}\\${current.name}`;
  }
  return current === folder ? folder : { ...current, name };
}

const byName = (left: string, right: string) => left.localeCompare(right, undefined, { sensitivity: "base", numeric: true });

function ChangeList({ changes, checked, setChecked, disabled }: { changes: Change[]; checked: Set<string>; setChecked: (next: Set<string>) => void; disabled: boolean }) {
  const [filter, setFilter] = useState("");
  const [folded, setFolded] = useState<Set<string>>(new Set());
  const allowed = changes.filter((change) => !change.lockedBy);
  const needle = filter.trim().toLowerCase();
  const shown = needle ? changes.filter((change) => change.path.toLowerCase().includes(needle)) : changes;

  // Puts every file of `files` someone may commit in, or takes them all out.
  const setAll = (files: Change[], on: boolean) => {
    const next = new Set(checked);
    for (const change of files) if (!change.lockedBy) { if (on) next.add(change.path); else next.delete(change.path); }
    setChecked(next);
  };
  const state = (files: Change[]) => {
    const open = files.filter((change) => !change.lockedBy);
    const on = open.filter((change) => checked.has(change.path)).length;
    return { open: open.length, all: open.length > 0 && on === open.length, some: on > 0 && on < open.length };
  };
  const box = (files: Change[], label: string) => {
    const { open, all, some } = state(files);
    return <input type="checkbox" disabled={disabled || open === 0} checked={all} aria-label={label}
      ref={(element) => { if (element) element.indeterminate = some; }} onChange={() => setAll(files, !all)} />;
  };

  const fileRow = (change: Change, depth: number, folder: string) => {
    const name = change.path.split(/[\\/]/).pop() ?? change.path;
    const mark = changeMark[change.change];
    return <li key={change.path} className={`${change.change}${change.lockedBy ? " locked" : ""}`} title={`${mark.title}: ${change.path}`}>
      <label className="check" style={{ paddingLeft: 10 + depth * 18 }}>
        <span className="caret" />
        <input type="checkbox" disabled={disabled || Boolean(change.lockedBy)} checked={checked.has(change.path)} onChange={() => setAll([change], !checked.has(change.path))} />
        <span className={`name ${change.change}`}>{name}</span>
        {folder && <span className="folder">{folder}</span>}
        {change.lockedBy && <span className="lock-note"><img src={lockIcon} alt="" />{change.lockedBy}</span>}
      </label></li>;
  };

  const rows = (node: Folder, depth: number): React.ReactNode[] => {
    const out: React.ReactNode[] = [];
    for (const child of [...node.folders.values()].map(compact).sort((left, right) => byName(left.name, right.name))) {
      const files = filesBelow(child);
      if (files.length === 1) {
        // One file: its row carries the folder, relative to where the list stands.
        const relative = child.path.slice(node.path ? node.path.length + 1 : 0);
        out.push(fileRow(files[0], depth, relative));
        continue;
      }
      const isFolded = folded.has(child.path) && !needle;
      out.push(<li key={`folder:${child.path}`} className="folder-row" title={child.path}>
        <div className="check" style={{ paddingLeft: 10 + depth * 18 }}>
          <button type="button" className="caret" aria-label={isFolded ? `Expand ${child.name}` : `Collapse ${child.name}`} aria-expanded={!isFolded}
            onClick={() => { const next = new Set(folded); if (next.has(child.path)) next.delete(child.path); else next.add(child.path); setFolded(next); }}>{isFolded ? "▸" : "▾"}</button>
          {box(files, `All files in ${child.name}`)}
          <span className="name" onClick={() => setAll(files, !state(files).all)}>{child.name}</span>
          <span className="count">{files.length} files</span>
        </div></li>);
      if (!isFolded) out.push(...rows(child, depth + 1));
    }
    for (const change of [...node.files].sort((left, right) => byName(left.path, right.path))) out.push(fileRow(change, depth, ""));
    return out;
  };

  return <div className="changes">
    <div className="changes-head">
      <label className="check">{box(shown, "Select all")}
        <span>Changes · {checked.size} of {allowed.length}</span></label>
      {changes.length >= filterFrom && <input className="changes-filter" type="search" value={filter} placeholder="Filter"
        aria-label="Filter files" onChange={(event) => setFilter(event.target.value)} />}
    </div>
    <ul className="change-list">{rows(buildTree(shown), 0)}{shown.length === 0 && <li className="none">No files match the filter.</li>}</ul>
    {/* What the colours mean, for the kinds of change the list holds. */}
    <div className="legend">{(Object.keys(changeMark) as Change["change"][]).filter((kind) => changes.some((change) => change.change === kind)).map((kind) =>
      <span key={kind}><i className={`dot ${kind}`} />{changeMark[kind].title.toLowerCase()}</span>)}</div>
  </div>;
}

const discardText = "Your changes to these files will be deleted and can't be restored. If you need them, copy the files somewhere else first.";

// The ways forward for each question, safest first; "danger" ones delete the user's changes and
// are confirmed separately.
function choiceOptions(question: ChoiceMessage): { choice: Choice; label: string; hint: string; danger?: boolean }[] {
  if (question.kind === "staged") return [
    { choice: "commit", label: "Commit and sync", hint: "Commits these files, then syncs and sends the commit to the server." },
    { choice: "unstage", label: "Sync without committing", hint: "Your changes stay in the files; commit them later." },
  ];
  return [
    { choice: "mine", label: "Keep my version", hint: question.merged
      ? "Your version replaces the server's, and the merge is sent to the server."
      : "Commits your version and sends it. If Lore can merge both changes, both are kept; otherwise yours replaces the server's." },
    { choice: "theirs", label: "Take the server's version", hint: "Discards your changes to these files.", danger: true },
  ];
}

// Reports the dialog's natural height to the host, which sizes the window to it. The layout
// normally stretches to the window (footer at the bottom, the account list filling the rest);
// "measuring" switches that off for one synchronous layout pass, so nothing flickers.
function useFitWindow() {
  useEffect(() => {
    let frame = 0;
    let last = 0;
    const measure = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const dialog = document.querySelector<HTMLElement>(".dialog");
        if (!dialog) return;
        document.documentElement.classList.add("measuring");
        const height = Math.ceil(dialog.getBoundingClientRect().height);
        document.documentElement.classList.remove("measuring");
        if (height !== last) {
          last = height;
          post({ type: "size", height });
        }
      });
    };
    const resize = new ResizeObserver(measure);
    resize.observe(document.body);
    const mutation = new MutationObserver(measure);
    mutation.observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true });
    measure();
    return () => { cancelAnimationFrame(frame); resize.disconnect(); mutation.disconnect(); };
  }, []);
}

export default function App() {
  useFitWindow();
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
  const [warning, setWarning] = useState(false);
  const [statuses, setStatuses] = useState<Record<string, number>>({});
  const [revisions, setRevisions] = useState<{ number: string; id: string }[]>([]);
  const [accounts, setAccounts] = useState<StoredAccount[]>([]);
  const [theme, setTheme] = useState("midnight");
  const [question, setQuestion] = useState<ChoiceMessage | null>(null);
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  // Commit: null while the files are being looked up.
  const [changes, setChanges] = useState<Change[] | null>(null);
  const [changesError, setChangesError] = useState("");
  const [checked, setChecked] = useState<Set<string>>(new Set());

  const start = (current: InitMessage, answer?: { choice: Choice | ""; files: string[] }) => {
    setRunning(true);
    setQuestion(null);
    setConfirmation(null);
    setLogs([]);
    setError("");
    setAccessDenied(false);
    setSuccess("");
    setWarning(false);
    setStatuses({});
    setRevisions([]);
    post({ type: "run", repositoryUrl: current.repositoryUrl, targetPath: current.targetPath, message: current.message, ...answer });
  };

  useEffect(() => {
    const receive = (event: MessageEvent<HostMessage>) => {
      const data = event.data;
      if (data.type === "init") {
        document.documentElement.dataset.theme = data.theme;
        setTheme(data.theme);
        operationRef.current = data.operation;
        setConfig(data);
        setRepositoryUrl(data.repositoryUrl);
        setTargetPath(data.targetPath);
        setMessage(data.message);
        // Operations that change or discard work wait for the user; the rest start at once.
        const waits: OperationValue[] = [Operation.Clone, Operation.Commit, Operation.Accounts, Operation.Revert, Operation.Resolve];
        if (!waits.includes(data.operation)) start(data);
      } else if (data.type === "changes") {
        setChanges(data.items);
        setChangesError(data.error ?? "");
        setChecked(new Set(data.items.filter((change) => !change.lockedBy).map((change) => change.path)));
      } else if (data.type === "theme") {
        document.documentElement.dataset.theme = data.theme;
        setTheme(data.theme);
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
        setWarning(Boolean(data.warning));
      } else if (data.type === "choice") {
        setRunning(false);
        setQuestion(data);
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
  if (config.operation === Operation.Accounts) return <main className="dialog accounts">
    <header className="header"><img className="operation-icon" src={accountsIcon} alt="" /><div><h1>{current.title}</h1><p>{current.subtitle}</p></div></header>
    <form className="account-form" onSubmit={addAccount}>
      <label><span>Lore server</span><span className="directory-row"><input required disabled={running} value={repositoryUrl}
        onChange={(event) => setRepositoryUrl(event.target.value)} placeholder="lore://localhost" />
        <button className="button primary" disabled={running}>{running ? "Signing in…" : "Sign in"}</button></span></label>
    </form>
    <div className="account-list">
      {accounts.length === 0 ? <div className="summary">Not signed in. Enter a Lore server URL to sign in.</div> : accounts.map((account) => <div className="account-row" key={`${account.authUrl}:${account.userId}`}>
        <div className="account-info"><strong>{account.username || "Unknown user"}</strong><span>{account.domains || account.authUrl.replace(/^oidc\+/, "")}</span></div>
        {account.selected ? <span className="selected-account">In use</span> : <button className="button secondary" disabled={running} onClick={() => { setRunning(true); setError(""); setSuccess(""); post({ type: "accounts-select", userId: account.userId }); }}>Use</button>}
        <button className="button danger" disabled={running} title="Sign out and delete this sign-in from the computer" onClick={() => { setRunning(true); setError(""); setSuccess(""); post({ type: "accounts-remove", authUrl: account.authUrl, userId: account.userId }); }}>Sign out</button>
      </div>)}
    </div>
    {running && <div className="progress" role="progressbar" aria-label="Waiting for browser sign-in"><span /></div>}
    {error && <div className="alert error" role="alert"><img src={errorIcon} alt="" /><span>{error}</span></div>}
    {success && <div className="alert success" role="status"><img src={successIcon} alt="" /><span>{success}</span></div>}
    {logs.length > 0 && <pre className="output" aria-live="polite">{logs.join("\n")}</pre>}
    <footer className="actions">
      <label className="theme-row">Window theme
        <select value={theme} onChange={(event) => post({ type: "theme-set", theme: event.target.value })}>
          {themes.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
        </select></label>
      <button className="button secondary" disabled={running} onClick={() => post({ type: "cancel" })}>Close</button>
    </footer>
  </main>;
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!window.chrome?.webview) {
      setError("The WebView2 bridge is unavailable.");
      return;
    }
    // With a list, exactly the ticked files are committed; without one (it failed, or nothing
    // changed and Commit only sends earlier commits) the selection is.
    const files = config.operation === Operation.Commit && changes && changes.length > 0 ? [...checked] : undefined;
    start({ ...config, repositoryUrl, targetPath, message }, files ? { choice: "", files } : undefined);
  };
  const hasPaths = ([Operation.Commit, Operation.Stage, Operation.Lock, Operation.Unlock, Operation.Unstage, Operation.Revert, Operation.Resolve] as OperationValue[])
    .includes(config.operation);
  const resolve = (choice: "mine" | "theirs") => {
    if (!window.chrome?.webview) {
      setError("The WebView2 bridge is unavailable.");
      return;
    }
    start({ ...config, repositoryUrl, targetPath, message: choice });
  };
  const statusCount = (name: string) => statuses[name] ?? 0;
  const answer = (choice: Choice, files: string[], danger?: boolean) => {
    const run = () => start({ ...config, repositoryUrl, targetPath, message }, { choice, files });
    if (danger) setConfirmation({ files, text: discardText, action: "Discard and take the server's version", run });
    else run();
  };

  if (confirmation) return <main className="dialog">
    <header className="header"><img className="operation-icon" src={warningIcon} alt="" /><div><h1>Discard your changes?</h1><p>{current.title}</p></div></header>
    <div className="form">
      <div className="alert danger" role="alert"><img src={errorIcon} alt="" /><span>{confirmation.text}</span></div>
      <label><span>Files · {confirmation.files.length}</span><ul className="path-list">{confirmation.files.map((path) => <li key={path}>{path}</li>)}</ul></label>
      <footer className="actions">
        <button className="button secondary" type="button" autoFocus onClick={() => setConfirmation(null)}>Cancel</button>
        <button className="button danger solid" type="button" onClick={confirmation.run}>{confirmation.action}</button>
      </footer>
    </div>
  </main>;

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

        {config.operation === Operation.Sync && !question && <div className="summary">If files are staged, or changed both here and on the server, Sync asks what to do before it changes them.</div>}
        {config.operation === Operation.Revert && !question && <div className="summary">Your changes to the selected files are deleted and can't be restored. You'll be asked to confirm. New files the server doesn't have yet are left in place.</div>}
        {config.operation === Operation.Resolve && !question && <div className="summary">Choose your version or the server's for the selected files. When no conflict is left, the merge is committed and sent to the server.</div>}
        {config.operation === Operation.Commit && !question && !success && (changes === null
          ? <div className="summary">Looking for changes…</div>
          : changes.length > 0 ? <ChangeList changes={changes} checked={checked} setChecked={setChecked} disabled={running} />
          : <div className="summary">{changesError ? `The list of changes isn't available (${changesError}). Commit records the selected paths.` : "No changes in the selected paths. Commit sends earlier commits that weren't sent yet, if there are any."}</div>)}
        {hasPaths && config.operation !== Operation.Commit && !question && <label><span>Selected paths · {config.paths.length}</span><ul className="path-list">{config.paths.map((path) => <li key={path}>{path}</li>)}</ul></label>}
        {config.operation === Operation.Commit && !question && <label><span>Commit message (optional)</span><textarea disabled={running} value={message}
          onChange={(event) => setMessage(event.target.value)} placeholder="Describe the changes" /></label>}
        {(config.operation === Operation.Stage || config.operation === Operation.Unstage || config.operation === Operation.Lock || config.operation === Operation.Unlock)
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

        {question && !running && <div className="choice" role="group" aria-label="Choose how to go on">
          <div className="alert warning"><img src={warningIcon} alt="" /><span>{question.text}</span></div>
          <label><span>Files · {question.files.length}</span><ul className="path-list">{question.files.map((path) => <li key={path}>{path}</li>)}</ul></label>
          <div className="options">{choiceOptions(question).map((option) =>
            <button key={option.choice} type="button" className={`option${option.danger ? " danger" : ""}`} onClick={() => answer(option.choice, question.files, option.danger)}>
              <strong>{option.label}{option.danger ? "…" : ""}</strong><span>{option.hint}</span></button>)}</div>
          {question.merged && <div className="summary">If you cancel, these files stay in conflict: Sync and Commit wait until you choose (Lore &gt; Resolve conflicts).</div>}
        </div>}
        {running && <div className="progress" role="progressbar" aria-label="Running operation"><span /></div>}
        {error && <div className="alert error" role="alert"><img src={errorIcon} alt="" /><div className="alert-content"><span>{error}</span>{accessDenied && <button className="button secondary error-action" type="button" onClick={() => post({ type: "accounts-open" })}>Sign in with another account</button>}</div></div>}
        {success && <div className={`alert ${warning ? "warning" : "success"}`} role="status"><img src={warning ? warningIcon : successIcon} alt="" /><span>{success}</span></div>}
        {logs.length > 0 && config.operation !== Operation.History && <pre className="output" aria-live="polite">{logs.join("\n")}</pre>}

        <footer className="actions">
          {question && !running ? <button className="button secondary" type="button" autoFocus onClick={() => post({ type: "cancel" })}>Cancel</button>
          : success && config.operation !== Operation.Status && config.operation !== Operation.History ? <button className="button primary" type="button" onClick={() => post({ type: "cancel" })}>OK</button> : <>
            <button className="button secondary" type="button" disabled={running} onClick={() => post({ type: "cancel" })}>{error ? "Close" : "Cancel"}</button>
            {config.operation === Operation.Resolve ? <>
              <button className="button danger" type="button" disabled={running} onClick={() => setConfirmation({ files: config.paths, text: discardText, action: "Discard and take the server's version", run: () => resolve("theirs") })}>Use the server's version…</button>
              <button className="button primary" type="button" disabled={running} onClick={() => resolve("mine")}>{running ? "Working…" : "Keep my version"}</button>
            </> : config.operation === Operation.Revert && !error
              ? <button className="button danger solid" type="button" disabled={running} onClick={() => setConfirmation({ files: config.paths, text: discardText, action: "Discard changes", run: () => start({ ...config, repositoryUrl, targetPath, message }) })}>{running ? "Working…" : current.action}</button>
              : <button className="button primary" type="submit" disabled={running || config.operation === Operation.Commit && (changes === null || changes.length > 0 && checked.size === 0)}>{running ? "Working…" : error ? "Retry" : current.action}</button>}
          </>}
        </footer>
      </form>
    </main>
  );
}
