import type { ADUser, AuditEvent, BulkResult, GlobalSettings, Grant, GrantSubject, LabeledGrant, Page, PublicConfig, Repository, RepositoryLock, RepositoryTree, Revision, Role, Session, User } from "./types";

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

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (init.method && !["GET", "HEAD"].includes(init.method)) headers.set("X-CSRF-Token", csrfToken);
  const response = await fetch(`/web/v1${path}`, { ...init, headers, credentials: "same-origin" });
  if (!response.ok) {
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

const query = (values: Record<string, string | number | undefined>) =>
  Object.entries(values).filter(([, value]) => value !== undefined && value !== "").map(([key, value]) => `${key}=${encodeURIComponent(String(value))}`).join("&");

export const api = {
  config: () => request<PublicConfig>("/config"),
  session: () => request<Session>("/session"),
  devLogin: () => request<{ authenticated: boolean }>("/dev-login", { method: "POST" }),
  logout: () => request<{ logoutUrl?: string }>("/logout", { method: "POST" }),
  catalog: (q = "", cursor = "") => request<Page<Repository>>(`/catalog?${query({ pageSize: 50, q, cursor })}`),
  repository: (id: string) => request<Repository>(`/repositories/${id}`),
  tree: (id: string, path = "", revision = "") => request<RepositoryTree>(`/repositories/${id}/tree?${query({ path, revision })}`),
  history: (id: string, cursor = "") => request<Page<Revision>>(`/repositories/${id}/history?${query({ cursor })}`),
  locks: (id: string) => request<{ items: RepositoryLock[] }>(`/repositories/${id}/locks`),
  downloadUrl: (id: string, path: string, revision: string) => `/web/v1/repositories/${id}/download?${query({ path, revision })}`,
  grants: (id: string) => request<{ items: LabeledGrant[] }>(`/repositories/${id}/grants/`),
  grantSubjects: (id: string, q: string, signal?: AbortSignal) => request<{ items: GrantSubject[] }>(`/repositories/${id}/grants/subjects?${query({ q })}`, { signal }),
  setGrant: (grant: Grant) => request<Grant>(`/repositories/${grant.repositoryId}/grants/`, { method: "PUT", body: JSON.stringify(grant) }),
  removeGrant: (grant: Grant) => request<void>(`/repositories/${grant.repositoryId}/grants/`, { method: "DELETE", body: JSON.stringify(grant) }),
  importGrants: (id: string, body: string, csv: boolean) => request<BulkResult>(`/repositories/${id}/grants/import`, { method: "POST", headers: { "Content-Type": csv ? "text/csv" : "application/json" }, body }),
  grantsExportUrl: (id: string, csv: boolean) => `/web/v1/repositories/${id}/grants/export${csv ? "?format=csv" : ""}`,
  users: (q = "", cursor = "") => request<Page<User>>(`/admin/users?${query({ pageSize: 50, q, cursor })}`),
  searchADUsers: (q: string, signal?: AbortSignal) => request<{ items: ADUser[] }>(`/admin/users/search?${query({ q })}`, { signal }),
  createUser: (input: { email: string; displayName: string; password: string; role: Role }) => request<User>("/admin/users", { method: "POST", body: JSON.stringify(input) }),
  provisionADUser: (username: string) => request<User>("/admin/users/provision", { method: "POST", body: JSON.stringify({ username }) }),
  updateUser: (user: Pick<User, "id" | "role" | "disabled">) => request<User>(`/admin/users/${user.id}`, { method: "PATCH", body: JSON.stringify({ role: user.role, disabled: user.disabled }) }),
  importUsers: (body: string, csv: boolean) => request<BulkResult>("/admin/users/import", { method: "POST", headers: { "Content-Type": csv ? "text/csv" : "application/json" }, body }),
  usersExportUrl: (csv: boolean) => `/web/v1/admin/users/export${csv ? "?format=csv" : ""}`,
  repositories: (q = "", cursor = "") => request<Page<Repository>>(`/admin/repositories?${query({ pageSize: 50, q, cursor })}`),
  createRepository: (input: { id?: string; name: string; description: string; public: boolean }) => request<Repository>("/admin/repositories", { method: "POST", body: JSON.stringify(input) }),
  updateRepository: (id: string, input: { name: string; description: string; public?: boolean }) => request<Repository>(`/admin/repositories/${id}`, { method: "PATCH", body: JSON.stringify(input) }),
  settings: () => request<GlobalSettings>("/admin/settings"),
  // The server replaces all settings at once: always send the complete object.
  saveSettings: (settings: GlobalSettings) => request<GlobalSettings>("/admin/settings", { method: "PATCH", body: JSON.stringify(settings) }),
  audit: (cursor = "") => request<Page<AuditEvent>>(`/admin/audit?${query({ pageSize: 50, cursor })}`),
};
