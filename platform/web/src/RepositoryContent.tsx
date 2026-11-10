import { useEffect, useState } from "react";
import { api } from "./api";
import type { RepositoryLock, RepositoryTree, Revision } from "./types";

function ContentError({ message }: { message: string }) {
  return message ? <div className="alert error">{message}</div> : null;
}

function formatSize(size: number) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MB`;
  return `${(size / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

export function RepositoryFiles({ id }: { id: string }) {
  const [path, setPath] = useState("");
  const [tree, setTree] = useState<RepositoryTree | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    api.tree(id, path).then(setTree).catch((cause) => setError(cause instanceof Error ? cause.message : "Couldn't load files")).finally(() => setLoading(false));
  }, [id, path]);

  const segments = path ? path.split("/") : [];
  const items = tree?.items.filter((item) => item.path !== path) ?? [];
  return <section className="panel table-panel">
    <div className="repository-toolbar">
      <div className="breadcrumbs"><button onClick={() => setPath("")}>Root</button>{segments.map((segment, index) => <span key={segments.slice(0, index + 1).join("/")}><span>/</span><button onClick={() => setPath(segments.slice(0, index + 1).join("/"))}>{segment}</button></span>)}</div>
      {tree?.revision && <span className="mono muted" title="Revision signature">{tree.revision.slice(0, 12)}</span>}
    </div>
    <ContentError message={error} />
    {loading ? <div className="loading">Loading files…</div> : items.length === 0 ? <div className="empty-state"><h2>No files</h2><p>This revision or directory is empty.</p></div> : <table><thead><tr><th>Name</th><th>Type</th><th>Size</th><th /></tr></thead><tbody>
      {items.map((item) => {
        const name = item.path.slice(path ? path.length + 1 : 0);
        return <tr key={item.path}><td>{item.type === "directory" ? <button className="file-link" onClick={() => setPath(item.path)}>{name}</button> : <span className="strong">{name}</span>}</td><td><span className="tag">{item.type}</span></td><td>{formatSize(item.size)}</td><td className="file-actions">{item.type === "file" && <a className="button ghost small" href={`/web/v1/repositories/${id}/download?path=${encodeURIComponent(item.path)}&revision=${encodeURIComponent(tree?.revision ?? "")}`}>Download</a>}</td></tr>;
      })}
    </tbody></table>}
  </section>;
}

export function RepositoryHistory({ id }: { id: string }) {
  const [revisions, setRevisions] = useState<Revision[]>([]);
  const [cursor, setCursor] = useState("");
  const [nextCursor, setNextCursor] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    api.history(id, cursor).then((page) => {
      setRevisions(page.items);
      setNextCursor(page.nextCursor ?? "");
    }).catch((cause) => setError(cause instanceof Error ? cause.message : "Couldn't load history")).finally(() => setLoading(false));
  }, [id, cursor]);

  return <section className="panel table-panel"><ContentError message={error} />
    {loading ? <div className="loading">Loading history…</div> : revisions.length === 0 ? <div className="empty-state"><h2>No revisions</h2><p>Commits will appear here after the first sync.</p></div> : <table><thead><tr><th>Revision</th><th>Commit</th><th>Author</th><th>Committed</th></tr></thead><tbody>
      {revisions.map((revision) => <tr key={revision.signature}><td><span className="tag">#{revision.number}</span><div className="mono muted" title={revision.signature}>{revision.signature.slice(0, 12)}</div></td><td className="strong">{revision.message || "No commit message"}</td><td>{revision.author || revision.committer}</td><td>{new Date(revision.committedAt).toLocaleString()}</td></tr>)}
    </tbody></table>}
    <div className="history-actions">{cursor && <button className="button ghost small" onClick={() => setCursor("")}>Newest</button>}{nextCursor && <button className="button ghost small" disabled={loading} onClick={() => setCursor(nextCursor)}>Older revisions</button>}</div>
  </section>;
}

export function RepositoryLocks({ id }: { id: string }) {
  const [locks, setLocks] = useState<RepositoryLock[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    api.locks(id).then((result) => setLocks(result.items)).catch((cause) => setError(cause instanceof Error ? cause.message : "Couldn't load locks")).finally(() => setLoading(false));
  }, [id]);
  return <section className="panel table-panel"><ContentError message={error} />
    {loading ? <div className="loading">Loading locks…</div> : locks.length === 0 ? <div className="empty-state"><h2>No active locks</h2><p>Locked files will appear here.</p></div> : <table><thead><tr><th>Path</th><th>Owner</th><th>Locked at</th></tr></thead><tbody>
      {locks.map((lock) => <tr key={`${lock.branchId}:${lock.path}:${lock.hash}`}><td className="strong">{lock.path}</td><td>{lock.owner}</td><td>{new Date(lock.lockedAt).toLocaleString()}</td></tr>)}
    </tbody></table>}
  </section>;
}
