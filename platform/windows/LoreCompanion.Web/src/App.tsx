import { memo, useEffect, useRef, useState } from "react";
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
type ChoiceMessage = { type: "choice"; kind: "staged" | "both"; files: string[]; total?: number; text: string; merged: boolean };
type Choice = "commit" | "unstage" | "mine" | "theirs";
// A step that deletes the user's changes waits for this confirmation.
type Confirmation = { files: string[]; total?: number; text: string; action: string; run: () => void };
// What Commit would record (LoreOperations.CommitCandidates); the user ticks what goes in.
type Change = { path: string; change: "added" | "deleted" | "modified"; staged: boolean; lockedBy: string; selected?: boolean };
// One folder row of the Commit list: everything below it, split by kind, and what is ticked.
type ChangeFolder = { path: string; name: string; depth: number; files: number; added: number; modified: number; deleted: number; locked: number; selected: number };
type ChangeSummary = { total: number; selected: number; eligible: number; folders: ChangeFolder[]; whole: boolean };
type ChangePage = { folder: string; filter: string; offset: number; total: number; selected: number; items: Change[] };
type CommitProgress = { phase: "prepare" | "commit" | "push" | "sync"; complete: number; total: number; bytes: number };
type HostMessage = InitMessage
  | { type: "commit-progress"; update: CommitProgress }
  | ({ type: "changes"; items?: Change[] | null; error?: string } & Partial<ChangeSummary>)
  | ({ type: "changes-page" } & Partial<ChangePage>)
  | ({ type: "selection" } & Partial<ChangeSummary>)
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
  | { type: "changes-page"; folder: string; filter: string; offset: number; limit: number; search: boolean }
  | { type: "changes-select"; scope: "all" | "folder" | "file"; path: string; on: boolean }
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
// Files per page of one folder, as CommitList.PageSize in the host.
const pageSize = 200;

// The folder a file sits in, the way the host names folders in its tree.
const folderOf = (path: string) => path.split(/[\\/]/).slice(0, -1).join("\\");
const nameOf = (path: string) => path.split(/[\\/]/).pop() ?? path;

function CommitActivity({ update, selected }: { update: CommitProgress | null; selected: number }) {
  const phase = update?.phase ?? "prepare";
  const label = { prepare: "Preparing files", commit: "Creating revision", push: "Sending revision", sync: "Synchronizing with server" }[phase];
  const percent = update && update.total > 0 ? Math.min(100, Math.floor(100 * update.complete / update.total)) : null;
  return <section className="commit-activity" aria-label="Commit progress">
    <div className="summary">{selected.toLocaleString()} selected {selected === 1 ? "file" : "files"}</div>
    <strong>{label}{percent !== null && ` \u00b7 ${percent}%`}</strong>
    {percent !== null && <progress aria-label={label} max={100} value={percent} />}
    {phase === "commit" && update && update.complete > 0 && <span>{update.complete.toLocaleString()} files processed</span>}
    {update && update.bytes > 0 && <span>{(update.bytes / 1048576).toLocaleString(undefined, { maximumFractionDigits: 1 })} MiB {phase === "push" ? "sent" : "processed"}</span>}
  </section>;
}

function groupPages(items: Change[] | null): Record<string, ChangePage> {
  const grouped: Record<string, ChangePage> = Object.create(null);
  for (const item of items ?? []) {
    const folder = folderOf(item.path);
    const page = grouped[folder] ?? { folder, filter: "", offset: 0, total: 0, selected: 0, items: [] };
    page.items.push(item);
    page.total += 1;
    if (!item.lockedBy && item.selected !== false) page.selected += 1;
    grouped[folder] = page;
  }
  return grouped;
}

// The Commit window's list, grouped by folder. A folder row's box takes in or leaves out
// everything below it, and its arrow folds it to that one row. The host sends the folder tree and
// then one page of files per folder as it opens, so a working copy with hundreds of thousands of
// changes costs the page a few rows at a time. Files someone else has locked can't be committed,
// so their boxes are off and disabled.
const ChangeList = memo(function ChangeList({ summary, items, disabled, onSelected }: {
  summary: ChangeSummary;
  items: Change[] | null;
  disabled: boolean;
  onSelected: (selected: number) => void;
}) {
  const [folders, setFolders] = useState<ChangeFolder[]>(summary.folders);
  const [folderLimit, setFolderLimit] = useState(summary.whole ? Infinity : pageSize);
  const [open, setOpen] = useState<Set<string>>(() => new Set(summary.whole ? summary.folders.map((folder) => folder.path) : []));
  const [pages, setPages] = useState<Record<string, ChangePage>>(() => groupPages(items));
  const [filter, setFilter] = useState("");
  const [found, setFound] = useState<ChangePage | null>(null);
  const [waiting, setWaiting] = useState<Set<string>>(new Set());
  const scope = useRef<"all" | "folder" | "file">("file");
  const activeFilter = useRef("");
  const pagesRef = useRef(pages);
  pagesRef.current = pages;

  const merge = (current: ChangePage | undefined, page: ChangePage): ChangePage => {
    if (!current || page.offset === 0) return page;
    if (page.offset !== current.items.length) return current;
    return { ...page, items: current.items.concat(page.items) };
  };

  const request = (folder: string, offset: number, limit = pageSize, search = "") => {
    post({ type: "changes-page", folder, filter: search, offset, limit, search: Boolean(search) });
  };

  useEffect(() => {
    if (!summary.whole) request("", 0);
  }, [summary.whole]);

  useEffect(() => {
    const bridge = window.chrome?.webview;
    if (!bridge) return;
    const receive = (event: MessageEvent<HostMessage>) => {
      const data = event.data;
      if (data.type === "changes-page") {
        const page = data as ChangePage;
        if (page.filter) {
          if (page.filter === activeFilter.current) setFound(current => merge(current ?? undefined, page));
          return;
        }
        setPages((current) => ({ ...current, [page.folder]: merge(current[page.folder], page) }));
        setWaiting((current) => {
          const next = new Set(current);
          next.delete(page.folder);
          return next;
        });
      } else if (data.type === "selection") {
        const next = data as Partial<ChangeSummary>;
        if (next.folders) setFolders(next.folders);
        if (typeof next.selected === "number") onSelected(next.selected);
        // A folder's box changes counts the rows at hand cannot work out: ask for the pages that are
        // open again. A single file's box is already right here.
        if (scope.current !== "file") {
          for (const folder of Object.keys(pagesRef.current)) request(folder, 0, Math.max(pageSize, pagesRef.current[folder]?.items.length ?? 0));
          if (found) request("", 0, Math.max(pageSize, found.items.length), filter);
        }
      }
    };
    bridge.addEventListener("message", receive);
    return () => bridge.removeEventListener("message", receive);
  });

  const toggleFolder = (folder: ChangeFolder) => {
    scope.current = "folder";
    const on = folder.selected < folder.files - folder.locked;
    post({ type: "changes-select", scope: "folder", path: folder.path, on });
  };

  const toggleAll = () => {
    scope.current = "all";
    post({ type: "changes-select", scope: "all", path: "", on: summary.selected < summary.eligible });
  };

  const toggleFile = (change: Change) => {
    scope.current = "file";
    const on = change.selected === false;
    setPages((current) => mark(current, change.path, on));
    setFound((current) => current ? { ...current, items: current.items.map((item) => item.path === change.path ? { ...item, selected: on } : item) } : current);
    post({ type: "changes-select", scope: "file", path: change.path, on });
  };

  const fold = (folder: ChangeFolder) => {
    const next = new Set(open);
    if (next.has(folder.path)) next.delete(folder.path);
    else {
      next.add(folder.path);
      if (!pages[folder.path] && !summary.whole) {
        setWaiting((current) => new Set(current).add(folder.path));
        request(folder.path, 0);
      }
    }
    setOpen(next);
  };

  const search = (needle: string) => {
    activeFilter.current = needle.trim();
    setFound(null);
    setFilter(needle);
    if (!needle.trim()) {
      setFound(null);
      return;
    }
    request("", 0, Math.max(pageSize, found?.items.length ?? 0), needle);
  };

  const fileRow = (change: Change, level: number, folder: string) => {
    const mark = changeMark[change.change];
    return <li key={change.path} className={`${change.change}${change.lockedBy ? " locked" : ""}`} title={`${mark.title}: ${change.path}`}>
      <label className="check" style={{ paddingLeft: 10 + level * 18 }}>
        <span className="caret" />
        <input type="checkbox" disabled={disabled || Boolean(change.lockedBy)} checked={change.selected !== false} onChange={() => toggleFile(change)} />
        <span className={`name ${change.change}`}>{nameOf(change.path)}</span>
        {folder && <span className="folder">{folder}</span>}
        {change.lockedBy && <span className="lock-note"><img src={lockIcon} alt="" />{change.lockedBy}</span>}
      </label></li>;
  };

  const known = new Set(folders.map((folder) => folder.path));
  const visible = (folder: ChangeFolder) => {
    let parent = folderOf(folder.path);
    while (parent) {
      if (known.has(parent) && !open.has(parent)) return false;
      parent = folderOf(parent);
    }
    return true;
  };

  const rows: React.ReactNode[] = [];
  if (found) {
    // A filter searches every change, so the list becomes the matches, whatever folder they sit in.
    rows.push(...found.items.map((change) => fileRow(change, 0, folderOf(change.path))));
    if (found.items.length < found.total) rows.push(<li key="more-found" className="more">
      <button type="button" className="button secondary" disabled={disabled}
        onClick={() => request("", found.items.length, pageSize, filter)}>Show more · {found.items.length} of {found.total}</button></li>);
    if (found.total === 0) rows.push(<li key="none" className="none">No files match the filter.</li>);
  } else {
    const top = pages[""];
    if (top) rows.push(...top.items.map((change) => fileRow(change, 0, "")));
    if (top && top.items.length < top.total) rows.push(<li key="more-root" className="more">
      <button type="button" className="button secondary" disabled={disabled}
        onClick={() => request("", top.items.length)}>Show more � {top.items.length} of {top.total}</button></li>);
    let displayed = 0;
    for (const folder of folders) {
      if (!visible(folder)) continue;
      if (displayed === folderLimit) {
        rows.push(<li key="more-folders" className="more"><button type="button" className="button secondary"
          onClick={() => setFolderLimit(limit => limit + pageSize)}>Show more folders</button></li>);
        break;
      }
      displayed++;
      const isOpen = open.has(folder.path);
      const allowed = folder.files - folder.locked;
      rows.push(<li key={`folder:${folder.path}`} className="folder-row" title={folder.path}>
        <div className="check" style={{ paddingLeft: 10 + folder.depth * 18 }}>
          <button type="button" className="caret" aria-label={isOpen ? `Collapse ${folder.name}` : `Expand ${folder.name}`} aria-expanded={isOpen}
            onClick={() => fold(folder)}>{isOpen ? "▾" : "▸"}</button>
          <input type="checkbox" disabled={disabled || allowed === 0} checked={allowed > 0 && folder.selected >= allowed}
            aria-label={`All files in ${folder.name}`} ref={(element) => { if (element) element.indeterminate = folder.selected > 0 && folder.selected < allowed; }}
            onChange={() => toggleFolder(folder)} />
          <span className="name" onClick={() => toggleFolder(folder)}>{folder.name}</span>
          <span className="count">{folder.files === 1 ? "1 file" : `${folder.files} files`}</span>
        </div></li>);
      if (!isOpen) continue;
      const page = pages[folder.path];
      if (!page) {
        rows.push(<li key={`loading:${folder.path}`} className="none">{waiting.has(folder.path) ? "Loading files…" : "Open to load the files."}</li>);
        continue;
      }
      rows.push(...page.items.map((change) => fileRow(change, folder.depth + 1, "")));
      if (page.items.length < page.total) rows.push(<li key={`more:${folder.path}`} className="more">
        <button type="button" className="button secondary" disabled={disabled}
          onClick={() => request(folder.path, page.items.length)}>Show more · {page.items.length} of {page.total}</button></li>);
    }
  }

  return <div className="changes">
    <div className="changes-head">
      <label className="check">
        <input type="checkbox" disabled={disabled || summary.eligible === 0} checked={summary.eligible > 0 && summary.selected >= summary.eligible}
          aria-label="Select all" ref={(element) => { if (element) element.indeterminate = summary.selected > 0 && summary.selected < summary.eligible; }}
          onChange={toggleAll} />
        <span>Changes · {summary.selected} of {summary.eligible}</span></label>
      {summary.total >= filterFrom && <input className="changes-filter" type="search" value={filter} placeholder="Filter"
        aria-label="Filter files" onChange={(event) => search(event.target.value)} />}
    </div>
    <ul className="change-list">{rows}</ul>
    {/* What the colours mean, for the kinds of change the list holds. */}
    <div className="legend">{(Object.keys(changeMark) as Change["change"][]).filter((kind) => folders.some((folder) => folder[kind] > 0)).map((kind) =>
      <span key={kind}><i className={`dot ${kind}`} />{changeMark[kind].title.toLowerCase()}</span>)}</div>
  </div>;
});

// Takes a file in or out of the pages at hand, so its box answers at once.
function mark(pages: Record<string, ChangePage>, path: string, on: boolean): Record<string, ChangePage> {
  const folder = folderOf(path);
  const page = pages[folder];
  if (!page) return pages;
  const items = page.items.map((item) => item.path === path ? { ...item, selected: on } : item);
  const change = page.items.find((item) => item.path === path);
  const selected = page.selected + (change && !change.lockedBy ? (on ? 1 : -1) : 0);
  return { ...pages, [folder]: { ...page, items, selected } };
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
  // Commit: null while the files are being looked up; then the tree and a count of what is ticked.
  const [changes, setChanges] = useState<ChangeSummary | null>(null);
  const [changeItems, setChangeItems] = useState<Change[] | null>(null);
  const [commitProgress, setCommitProgress] = useState<CommitProgress | null>(null);
  const [changesError, setChangesError] = useState("");
  const [selected, setSelected] = useState(0);

  const start = (current: InitMessage, answer?: { choice: Choice | ""; files: string[] }) => {
    setRunning(true);
    setCommitProgress(answer?.choice ? { phase: "sync", complete: 0, total: 0, bytes: 0 } : null);
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
        setChangesError(data.error ?? "");
        setChangeItems(data.items ?? null);
        setSelected(data.selected ?? 0);
        setChanges(typeof data.total === "number"
          ? { total: data.total, selected: data.selected ?? 0, eligible: data.eligible ?? 0, folders: data.folders ?? [], whole: Boolean(data.whole) }
          : null);
      } else if (data.type === "selection") {
        setSelected(data.selected ?? 0);
        setChanges((current) => current ? { ...current, selected: data.selected ?? current.selected, folders: data.folders ?? current.folders } : current);
      } else if (data.type === "theme") {
        document.documentElement.dataset.theme = data.theme;
        setTheme(data.theme);
      } else if (data.type === "accounts") {
        setAccounts(data.items);
      } else if (data.type === "commit-progress") {
        setCommitProgress(data.update);
      } else if (data.type === "progress") {
        // The host batches the lines Lore reports per file, so one message can hold many of them;
        // the log keeps the tail, which is what the window shows anyway.
        const lines = data.text.split("\n").filter((line) => line.length > 0);
        setLogs((current) => {
          const next = [...current, ...lines];
          return next.length > 200 ? next.slice(next.length - 200) : next;
        });
        if (operationRef.current === Operation.Status) {
          setStatuses((current) => {
            const next = { ...current };
            for (const line of lines) {
              const match = line.match(/:\s*(Modified|StagedModified|Staged|Conflicted|Locked|Ignored)\s*$/);
              if (match) next[match[1]] = (next[match[1]] ?? 0) + 1;
            }
            return next;
          });
        }
        if (operationRef.current === Operation.History) {
          // A long history would grow the list without bound; the newest few hundred are enough.
          setRevisions((current) => {
            const next = [...current];
            for (const line of lines) {
              const match = line.match(/^Revision\s+(\d+):\s*(.+)$/);
              if (match) next.push({ number: match[1], id: match[2] });
            }
            return next.length > 500 ? next.slice(0, 500) : next;
          });
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
    // The host keeps the ticked files itself (they never fit a page message for a large working
    // copy), so Commit takes the selection from there; without a list it commits the paths.
    start({ ...config, repositoryUrl, targetPath, message });
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
  // The question's files stay on the host: it asked about them and answers with the choice alone,
  // because a working copy's worth of paths would not fit a message.
  const answer = (choice: Choice, files: string[], danger?: boolean) => {
    const run = () => start({ ...config, repositoryUrl, targetPath, message }, { choice, files: [] });
    if (danger) setConfirmation({ files, total: question?.total, text: discardText, action: "Discard and take the server's version", run });
    else run();
  };

  if (confirmation) return <main className="dialog">
    <header className="header"><img className="operation-icon" src={warningIcon} alt="" /><div><h1>Discard your changes?</h1><p>{current.title}</p></div></header>
    <div className="form">
      <div className="alert danger" role="alert"><img src={errorIcon} alt="" /><span>{confirmation.text}</span></div>
      <label><span>Files · {confirmation.total ?? confirmation.files.length}</span><ul className="path-list">{confirmation.files.map((path) => <li key={path}>{path}</li>)}</ul></label>
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
        <div><h1>{current.title}</h1>{!(config.operation === Operation.Commit && running) && <p>{current.subtitle}</p>}</div>
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
        {config.operation === Operation.Commit && !question && !success && <div hidden={running}>{changes === null
          ? <div className="summary">{changesError ? `The list of changes isn't available (${changesError}). Commit records the selected paths.` : "Looking for changes…"}</div>
          : changes.total > 0 ? <ChangeList summary={changes} items={changeItems} disabled={running} onSelected={setSelected} />
          : <div className="summary">{changesError ? `The list of changes isn't available (${changesError}). Commit records the selected paths.` : "No changes in the selected paths. Commit sends earlier commits that weren't sent yet, if there are any."}</div>}</div>}
        {hasPaths && config.operation !== Operation.Commit && !question && <label><span>Selected paths · {config.paths.length}</span><ul className="path-list">{config.paths.map((path) => <li key={path}>{path}</li>)}</ul></label>}
        {config.operation === Operation.Commit && !question && !running && !success && <label><span>Commit message (optional)</span><textarea disabled={running} value={message}
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
          <label><span>Files · {question.total ?? question.files.length}</span><ul className="path-list">{question.files.map((path) => <li key={path}>{path}</li>)}</ul>
            {(question.total ?? question.files.length) > question.files.length && <span className="summary">…and {(question.total ?? 0) - question.files.length} more. Your choice applies to all of them.</span>}</label>
          <div className="options">{choiceOptions(question).map((option) =>
            <button key={option.choice} type="button" className={`option${option.danger ? " danger" : ""}`} onClick={() => answer(option.choice, question.files, option.danger)}>
              <strong>{option.label}{option.danger ? "…" : ""}</strong><span>{option.hint}</span></button>)}</div>
          {question.merged && <div className="summary">If you cancel, these files stay in conflict: Sync and Commit wait until you choose (Lore &gt; Resolve conflicts).</div>}
        </div>}
        {running && config.operation === Operation.Commit && <CommitActivity update={commitProgress} selected={selected} />}
        {running && config.operation !== Operation.Commit && <div className="progress" role="progressbar" aria-label="Running operation"><span /></div>}
        {error && <div className="alert error" role="alert"><img src={errorIcon} alt="" /><div className="alert-content"><span>{error}</span>{accessDenied && <button className="button secondary error-action" type="button" onClick={() => post({ type: "accounts-open" })}>Sign in with another account</button>}</div></div>}
        {success && <div className={`alert ${warning ? "warning" : "success"}`} role="status"><img src={warning ? warningIcon : successIcon} alt="" /><span>{success}</span></div>}
        {config.operation === Operation.Commit && error && logs.length > 0 && <details><summary>Operation log</summary><pre className="output">{logs.join("\n")}</pre></details>}
        {logs.length > 0 && config.operation !== Operation.Commit && config.operation !== Operation.History && <pre className="output" aria-live="polite">{logs.join("\n")}</pre>}

        <footer className="actions">
          {question && !running ? <button className="button secondary" type="button" autoFocus onClick={() => post({ type: "cancel" })}>Cancel</button>
          : success && config.operation !== Operation.Status && config.operation !== Operation.History ? <button className="button primary" type="button" onClick={() => post({ type: "cancel" })}>OK</button> : <>
            <button className="button secondary" type="button" disabled={running} onClick={() => post({ type: "cancel" })}>{error ? "Close" : "Cancel"}</button>
            {config.operation === Operation.Resolve ? <>
              <button className="button danger" type="button" disabled={running} onClick={() => setConfirmation({ files: config.paths, text: discardText, action: "Discard and take the server's version", run: () => resolve("theirs") })}>Use the server's version…</button>
              <button className="button primary" type="button" disabled={running} onClick={() => resolve("mine")}>{running ? "Working…" : "Keep my version"}</button>
            </> : config.operation === Operation.Revert && !error
              ? <button className="button danger solid" type="button" disabled={running} onClick={() => setConfirmation({ files: config.paths, text: discardText, action: "Discard changes", run: () => start({ ...config, repositoryUrl, targetPath, message }) })}>{running ? "Working…" : current.action}</button>
              : <button className="button primary" type="submit" disabled={running || config.operation === Operation.Commit && (changes === null || changes.total > 0 && selected === 0)}>{running ? "Working…" : error ? "Retry" : current.action}</button>}
          </>}
        </footer>
      </form>
    </main>
  );
}
