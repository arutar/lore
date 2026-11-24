export type Role = "admin" | "user";
export type Permission = "read" | "write" | "owner";
export type SubjectType = "user" | "group" | "anonymous" | "authenticated";

export interface User {
  directoryId?: string;
  directoryDisabled?: boolean;
  groupsSyncedAt?: string;
  id: string;
  username: string;
  email: string;
  displayName: string;
  role: Role;
  groups: string[];
  source: string;
  disabled: boolean;
  accessDenied?: boolean;
  /** MCP is switched off for this local account. */
  mcpDisabled?: boolean;
  createdAt?: string;
  updatedAt?: string;
}

export interface ADUser {
  id?: string;
  username: string;
  email: string;
  displayName: string;
}

export interface Session {
  authenticated: boolean;
  user?: User;
  csrfToken?: string;
  /** The account may use MCP: it's on for this server and not switched off for the account. */
  mcpAllowed?: boolean;
}

/** A personal token for the MCP endpoint. The secret itself is shown only when it's created. */
export interface MCPToken {
  id: string;
  userId: string;
  userName?: string;
  userLogin?: string;
  /** Whether it works now; when not, why: its owner is disabled, MCP is off for them, or it expired. */
  status?: "active" | "expired" | "user_disabled" | "mcp_off";
  name: string;
  /** The start of the secret, to tell tokens apart. */
  hint: string;
  /** Repository IDs the token is limited to; empty: every repository its owner may read. */
  repositories: string[];
  createdAt: string;
  expiresAt?: string;
  lastUsedAt?: string;
}

/** A client build the server hands out from /downloads, as its manifest.json lists it. */
export interface ClientDownload {
  platform: "windows" | "linux";
  /** Linux packages name their system, and the Dolphin plugins' package adds _dolphin. */
  kind: "msi" | "zip" | "tar" | "rpm_el9" | "rpm_el9_dolphin" | "deb_ubuntu2404" | "deb_ubuntu2404_dolphin" | "deb_debian13" | "deb_debian13_dolphin";
  arch: "x86_64" | "aarch64";
  file: string;
  url: string;
  size: number;
  sha256: string;
}

export interface ClientDownloads {
  version: string;
  /** "github": the links lead to the release on GitHub; "server": this server holds the files. */
  source: "github" | "server";
  items: ClientDownload[];
}

export interface PublicConfig {
  registrationEnabled: boolean;
  oidcEnabled: boolean;
  devAuthEnabled: boolean;
  adEnabled: boolean;
  externalUrl: string;
  nativeRepositoryUrl: string;
  theme: string;
  language: Language;
  /** The server answers MCP at /mcp. */
  mcpEnabled?: boolean;
}

export type Language = "ru" | "en";

export interface GlobalSettings {
  registrationEnabled: boolean;
  theme: string;
  language: Language;
}

export interface Repository {
  id: string;
  name: string;
  description: string;
  ownerId: string;
  ownerName?: string;
  public: boolean;
  createdAt?: string;
  updatedAt?: string;
}

export interface Grant {
  repositoryId: string;
  subjectType: SubjectType;
  subject: string;
  permission: Permission;
}

export interface LabeledGrant extends Grant {
  label: string;
  detail?: string;
}

/** An Active Directory account or group that may sign in to Lore (nested groups included). */
export interface AccessEntry {
  subjectType: "user" | "group";
  subject: string;
  name: string;
  detail?: string;
  role: Role;
  disabled: boolean;
  /** MCP is switched off for the account, or for the group's members. */
  mcpDisabled?: boolean;
  createdAt: string;
}

/** Everyone who may use Lore: local accounts, directory accounts added personally and
 *  directory groups with the members who have signed in. */
export interface Members {
  local: User[];
  accounts: (AccessEntry & { user?: User })[];
  groups: (AccessEntry & { members: User[] })[];
  directory: boolean;
}

export interface GrantSubject {
  type: "user" | "group";
  id: string;
  value: string;
  label: string;
  detail: string;
}

export interface AuditEvent {
  subjectType?: SubjectType;
  subject?: string;
  subjectName?: string;
  id: number;
  actorId: string;
  actorName?: string;
  action: string;
  targetType: string;
  targetId: string;
  targetName?: string;
  before?: string;
  after?: string;
  createdAt: string;
}

export interface BulkResult {
  imported: number;
  errors: { line: number; message: string }[];
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

// The newest revision that changed a path (a folder: anything under it).
export interface LastChange {
  revision: string;
  number: number;
  author: string;
  message: string;
  committedAt: string;
}

export interface TreeNode {
  path: string;
  lastChange?: LastChange;
  type: "directory" | "file" | "link";
  size: number;
  mode: number;
  executable: boolean;
}

export interface RepositoryTree {
  revision: string;
  branchId: string;
  items: TreeNode[];
}

export interface Revision {
  number: number;
  signature: string;
  message: string;
  author: string;
  committer: string;
  committedAt: string;
}

export type ChangeKind = "added" | "modified" | "deleted" | "moved" | "copied";

/** A file a revision changed against its parent; `from` is a moved or copied file's old path. */
export interface RevisionChange {
  kind: ChangeKind;
  path: string;
  from?: string;
}

export interface ChangeSummary {
  parent?: string;
  added: number;
  modified: number;
  deleted: number;
  moved: number;
  copied: number;
  total: number;
}

export interface RepositoryLock {
  branchId: string;
  path: string;
  hash: string;
  owner: string;
  lockedAt: string;
  /** The owner can't sign in any more (removed or disabled). */
  ownerDisabled?: boolean;
}
