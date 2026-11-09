import type { AuditEvent, BulkResult, GlobalSettings, Grant, Page, PublicConfig, Repository, Session, User } from "./types";

let csrfToken = "";

export function setCSRFToken(token: string | undefined) {
  csrfToken = token ?? "";
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (init.method && !["GET", "HEAD"].includes(init.method)) {
    headers.set("X-CSRF-Token", csrfToken);
  }
  const response = await fetch(`/web/v1${path}`, {
    ...init,
    headers,
    credentials: "same-origin",
  });
  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    try {
      const body = await response.json();
      message = body.error?.message ?? message;
    } catch {
      // The status remains actionable when a proxy returns non-JSON content.
    }
    throw new Error(message);
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return response.json() as Promise<T>;
}

export const api = {
  config: () => request<PublicConfig>("/config"),
  session: () => request<Session>("/session"),
  devLogin: () => request<{ authenticated: boolean }>("/dev-login", { method: "POST" }),
  logout: () => request<void>("/logout", { method: "POST" }),
  catalog: (cursor = "") => request<Page<Repository>>(`/catalog?pageSize=50&cursor=${encodeURIComponent(cursor)}`),
  repository: (id: string) => request<Repository>(`/repositories/${id}`),
  grants: (id: string) => request<{ items: Grant[] }>(`/repositories/${id}/grants/`),
  setGrant: (grant: Grant) => request<Grant>(`/repositories/${grant.repositoryId}/grants/`, { method: "PUT", body: JSON.stringify(grant) }),
  removeGrant: (grant: Grant) => request<void>(`/repositories/${grant.repositoryId}/grants/`, { method: "DELETE", body: JSON.stringify(grant) }),
  importGrants: (id: string, body: string, csv: boolean) => request<BulkResult>(`/repositories/${id}/grants/import`, { method: "POST", headers: { "Content-Type": csv ? "text/csv" : "application/json" }, body }),
  users: () => request<Page<User>>("/admin/users?pageSize=100"),
  updateUser: (user: User) => request<User>(`/admin/users/${user.id}`, { method: "PATCH", body: JSON.stringify({ role: user.role, disabled: user.disabled }) }),
  importUsers: (body: string, csv: boolean) => request<BulkResult>("/admin/users/import", { method: "POST", headers: { "Content-Type": csv ? "text/csv" : "application/json" }, body }),
  repositories: () => request<Page<Repository>>("/admin/repositories?pageSize=100"),
  createRepository: (input: { id?: string; name: string; description: string; public: boolean }) => request<Repository>("/admin/repositories", { method: "POST", body: JSON.stringify(input) }),
  settings: () => request<GlobalSettings>("/admin/settings"),
  saveSettings: (settings: GlobalSettings) => request<GlobalSettings>("/admin/settings", { method: "PATCH", body: JSON.stringify(settings) }),
  audit: () => request<Page<AuditEvent>>("/admin/audit?pageSize=100"),
};
