import { demoApi } from "./demo/api";
import type { AccessEntry, ADUser, Members, AuditEvent, BulkResult, ChangeKind, ChangeSummary, GlobalSettings, Grant, GrantSubject, LabeledGrant, Page, PublicConfig, Repository, RepositoryLock, RepositoryTree, Revision, RevisionChange, Role, Session, User } from "./types";

let csrfToken = "";

export function setCSRFToken(token: string | undefined) {
  csrfToken = token ?? "";
}

/** An API failure with the server's stable error code, so the UI can translate it. */
export class ApiError extends Error {
  constructor(message: string, readonly status: number, readonly code: string) {
    super(message);
  }
}

let unauthorized: (() => void) | null = null;

/** Called when the server answers that the session is gone, as after a long absence: the WebUI
 * then asks to sign in again instead of showing each page's failure. */
export function onUnauthorized(handler: (() => void) | null) {
  unauthorized = handler;
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (init.method && !["GET", "HEAD"].includes(init.method)) headers.set("X-CSRF-Token", csrfToken);
  const response = await fetch(`/web/v1${path}`, { ...init, headers, credentials: "same-origin" });
  if (!response.ok) {
    if (response.status === 401 && path !== "/session") unauthorized?.();
    let message = `Request failed (${response.status})`;
    let code = "http_" + response.status;
    try {
      const body = await response.json();
      message = body.error?.message ?? message;
      code = body.error?.code ?? code;
    } catch {
      // A proxy may answer with a non-JSON page; the status is still actionable.
    }
    throw new ApiError(message, response.status, code);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

const grantBody = ({ repositoryId, subjectType, subject, permission }: Grant): Grant => ({ repositoryId, subjectType, subject, permission });

const query = (values: Record<string, string | number | undefined>) =>
  Object.entries(values).filter(([, value]) => value !== undefined && value !== "").map(([key, value]) => `${key}=${encodeURIComponent(String(value))}`).join("&");

const serverApi = {
  config: () => request<PublicConfig>("/config"),
  session: () => request<Session>("/session"),
  devLogin: () => request<{ authenticated: boolean }>("/dev-login", { method: "POST" }),
  logout: () => request<{ logoutUrl?: string }>("/logout", { method: "POST" }),
  catalog: (q = "", cursor = "") => request<Page<Repository>>(`/catalog?${query({ pageSize: 50, q, cursor })}`),
  repository: (id: string) => request<Repository>(`/repositories/${id}`),
  tree: (id: string, path = "", revision = "") => request<RepositoryTree>(`/repositories/${id}/tree?${query({ path, revision })}`),
  history: (id: string, cursor = "") => request<Page<Revision>>(`/repositories/${id}/history?${query({ cursor })}`),
  // A revision's changed files, a page at a time; kind and q filter the whole revision.
  changes: (id: string, revision: string, filter: { kind?: ChangeKind; q?: string; cursor?: string; limit?: number }, signal?: AbortSignal) =>
    request<Page<RevisionChange>>(`/repositories/${id}/revisions/${revision}/changes?${query({ kind: filter.kind, q: filter.q, cursor: filter.cursor, limit: filter.limit })}`, { signal }),
  changeSummary: (id: string, revision: string, signal?: AbortSignal) => request<ChangeSummary>(`/repositories/${id}/revisions/${revision}/summary`, { signal }),
  locks: (id: string) => request<{ items: RepositoryLock[]; canRelease: boolean }>(`/repositories/${id}/locks`),
  releaseLock: (id: string, lock: RepositoryLock) => request<void>(`/repositories/${id}/locks/release`, { method: "POST", body: JSON.stringify({ branchId: lock.branchId, hash: lock.hash }) }),
  downloadUrl: (id: string, path: string, revision: string) => `/web/v1/repositories/${id}/download?${query({ path, revision })}`,
  grants: (id: string) => request<{ items: LabeledGrant[] }>(`/repositories/${id}/grants/`),
  grantSubjects: (id: string, q: string, signal?: AbortSignal) => request<{ items: GrantSubject[] }>(`/repositories/${id}/grants/subjects?${query({ q })}`, { signal }),
  // Only the grant's own fields: the access tab passes labelled grants (label, detail), and the
  // server rejects fields it doesn't know.
  setGrant: (grant: Grant) => request<Grant>(`/repositories/${grant.repositoryId}/grants/`, { method: "PUT", body: JSON.stringify(grantBody(grant)) }),
  removeGrant: (grant: Grant) => request<void>(`/repositories/${grant.repositoryId}/grants/`, { method: "DELETE", body: JSON.stringify(grantBody(grant)) }),
  importGrants: (id: string, body: string, csv: boolean) => request<BulkResult>(`/repositories/${id}/grants/import`, { method: "POST", headers: { "Content-Type": csv ? "text/csv" : "application/json" }, body }),
  grantsExportUrl: (id: string, csv: boolean) => `/web/v1/repositories/${id}/grants/export${csv ? "?format=csv" : ""}`,
  users: (q = "", cursor = "") => request<Page<User>>(`/admin/users?${query({ pageSize: 50, q, cursor })}`),
  searchADUsers: (q: string, signal?: AbortSignal) => request<{ items: ADUser[] }>(`/admin/users/search?${query({ q })}`, { signal }),
  createUser: (input: { email: string; displayName: string; password: string; role: Role }) => request<User>("/admin/users", { method: "POST", body: JSON.stringify(input) }),
  accessList: () => request<{ items: AccessEntry[]; directory: boolean }>("/admin/access"),
  accessSubjects: (q: string, signal?: AbortSignal) => request<{ items: GrantSubject[] }>(`/admin/access/subjects?${query({ q })}`, { signal }),
  addAccess: (subjectType: "user" | "group", subject: string) => request<{ items: AccessEntry[]; directory: boolean }>("/admin/access", { method: "POST", body: JSON.stringify({ subjectType, subject }) }),
  members: () => request<Members>("/admin/members"),
  addPersonalAccess: (subject: string, role: Role, disabled: boolean) => request<{ items: AccessEntry[] }>("/admin/access", { method: "POST", body: JSON.stringify({ subjectType: "user", subject, role, disabled }) }),
  updateAccess: (subjectType: "user" | "group", subject: string, role: Role, disabled: boolean) => request<AccessEntry>(`/admin/access/${subjectType}/${encodeURIComponent(subject)}`, { method: "PATCH", body: JSON.stringify({ role, disabled }) }),
  removeAccess: (subjectType: "user" | "group", subject: string) => request<void>(`/admin/access/${subjectType}/${encodeURIComponent(subject)}`, { method: "DELETE" }),
  provisionADUser: (username: string) => request<User>("/admin/users/provision", { method: "POST", body: JSON.stringify({ username }) }),
  deleteUser: (id: string) => request<void>(`/admin/users/${encodeURIComponent(id)}`, { method: "DELETE" }),
  updateUser: (user: Pick<User, "id" | "role" | "disabled">) => request<User>(`/admin/users/${user.id}`, { method: "PATCH", body: JSON.stringify({ role: user.role, disabled: user.disabled }) }),
  importUsers: (body: string, csv: boolean) => request<BulkResult>("/admin/users/import", { method: "POST", headers: { "Content-Type": csv ? "text/csv" : "application/json" }, body }),
  usersExportUrl: (csv: boolean) => `/web/v1/admin/users/export${csv ? "?format=csv" : ""}`,
  repositories: (q = "", cursor = "") => request<Page<Repository>>(`/admin/repositories?${query({ pageSize: 50, q, cursor })}`),
  createRepository: (input: { id?: string; name: string; description: string; public: boolean }) => request<Repository>("/admin/repositories", { method: "POST", body: JSON.stringify(input) }),
  deleteRepository: (id: string, name: string) => request<void>(`/admin/repositories/${id}`, { method: "DELETE", body: JSON.stringify({ name }) }),
  updateRepository: (id: string, input: { name: string; description: string; public?: boolean }) => request<Repository>(`/admin/repositories/${id}`, { method: "PATCH", body: JSON.stringify(input) }),
  settings: () => request<GlobalSettings>("/admin/settings"),
  // The server replaces all settings at once: always send the complete object.
  saveSettings: (settings: GlobalSettings) => request<GlobalSettings>("/admin/settings", { method: "PATCH", body: JSON.stringify(settings) }),
  audit: (cursor = "") => request<Page<AuditEvent>>(`/admin/audit?${query({ pageSize: 50, cursor })}`),
};

export type Api = typeof serverApi;

/** The read-only demo (npm run build:demo) answers in the browser from fictional data; the
 *  regular build talks to the control plane. The demo's module is dropped from the regular build. */
export const isDemo = import.meta.env.MODE === "demo";

export const api: Api = isDemo ? demoApi : serverApi;
