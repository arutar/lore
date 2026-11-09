export type Role = "admin" | "user";
export type Permission = "read" | "write" | "owner";
export type SubjectType = "user" | "group" | "anonymous" | "authenticated";

export interface User {
  id: string;
  username: string;
  email: string;
  displayName: string;
  role: Role;
  groups: string[];
  source: string;
  disabled: boolean;
}

export interface Session {
  authenticated: boolean;
  user?: User;
  csrfToken?: string;
}

export interface PublicConfig {
  registrationEnabled: boolean;
  oidcEnabled: boolean;
  devAuthEnabled: boolean;
  externalUrl: string;
  theme: string;
}

export interface GlobalSettings {
  registrationEnabled: boolean;
  theme: string;
}

export interface Repository {
  id: string;
  name: string;
  description: string;
  ownerId: string;
}

export interface Grant {
  repositoryId: string;
  subjectType: SubjectType;
  subject: string;
  permission: Permission;
}

export interface AuditEvent {
  id: number;
  actorId: string;
  action: string;
  targetType: string;
  targetId: string;
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
