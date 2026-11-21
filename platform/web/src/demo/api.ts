// The WebUI's API answered in the browser for the read-only demo, from the fictional data in
// ./data. Reading works everywhere; anything that would change the installation is refused with
// the "demo_read_only" error the UI shows as a toast. The appearance settings (theme, language)
// are the exception: they're kept in this browser, so visitors can try every theme.
import { ApiError, type Api } from "../api";
import type { GlobalSettings, GrantSubject, Language, Page } from "../types";
import * as data from "./data";

const settingsKey = "lore-demo-settings";

function loadSettings(): GlobalSettings {
  const fallback: GlobalSettings = {
    registrationEnabled: data.config.registrationEnabled,
    theme: data.config.theme,
    // English first, whatever the browser's language; RU in the strip switches.
    language: "en" as Language,
  };
  try {
    return { ...fallback, ...JSON.parse(localStorage.getItem(settingsKey) ?? "{}") };
  } catch {
    return fallback;
  }
}

// A short pause, so loading states show as they would against a server.
const reply = <T,>(value: T) => new Promise<T>((resolve) => setTimeout(() => resolve(structuredClone(value)), 120));

const readOnly = (): Promise<never> => Promise.reject(new ApiError("This is a demo: changes are turned off.", 403, "demo_read_only"));

const missing = (): Promise<never> => Promise.reject(new ApiError("Not found", 404, "not_found"));

const matches = (needle: string, ...values: (string | undefined)[]) =>
  !needle || values.some((value) => value?.toLowerCase().includes(needle.toLowerCase()));

const page = <T,>(items: T[]): Page<T> => ({ items });

// Download and export links: a small text file saying what the real installation would send.
const notice = (what: string) => `data:text/plain;charset=utf-8,${encodeURIComponent(`Lore Platform demo\n\nA real installation sends ${what} here.\n`)}`;

const directoryPeople = [
  { username: "slund", displayName: "Sven Lund" },
  { username: "nkovacs", displayName: "Nora Kovacs" },
  { username: "tbaker", displayName: "Tom Baker" },
  { username: "yito", displayName: "Yuki Ito" },
];

function subjects(q: string): GrantSubject[] {
  if (q.trim().length < 2) return [];
  const groups = Object.entries(data.groupNames).filter(([, name]) => matches(q, name))
    .map(([id, name]) => ({ type: "group" as const, id, value: name, label: name, detail: `CN=${name},OU=Groups,DC=brightwater,DC=example` }));
  const people = [...data.users.filter((user) => user.source === "ad"), ...directoryPeople.map((person) => ({ ...person, id: `${person.username}-id`, email: `${person.username}@brightwater.example` }))]
    .filter((person) => matches(q, person.username, person.displayName))
    .map((person) => ({ type: "user" as const, id: person.id, value: person.username, label: person.displayName, detail: `${person.username} · ${person.email}` }));
  return [...groups, ...people].slice(0, 20);
}

export const demoApi: Api = {
  config: () => {
    const settings = loadSettings();
    return reply({ ...data.config, theme: settings.theme, language: settings.language, registrationEnabled: settings.registrationEnabled });
  },
  session: () => reply({ authenticated: true, user: data.me, csrfToken: "demo" }),
  devLogin: readOnly,
  logout: readOnly,
  catalog: (q = "") => reply(page(data.repositories.filter((repository) => matches(q, repository.name, repository.description)))),
  repository: (id) => {
    const repository = data.repositories.find((item) => item.id === id);
    return repository ? reply(repository) : missing();
  },
  tree: (id, path = "") => {
    const items = data.tree(id, path);
    const revision = data.revisions(id)[0]?.signature ?? "";
    return items ? reply({ revision, branchId: "main", items }) : missing();
  },
  history: (id) => reply(page(data.revisions(id))),
  changes: (id, revision, filter) => {
    const all = data.changes(id, revision);
    if (!all) return missing();
    const kept = all.filter((change) => (!filter.kind || change.kind === filter.kind) && matches(filter.q ?? "", change.path, change.from));
    const start = Number(filter.cursor || 0);
    const end = start + (filter.limit ?? 500);
    return reply({ items: kept.slice(start, end), nextCursor: end < kept.length ? String(end) : undefined });
  },
  // `?downloads=server` in the demo's address shows a server that holds the files itself.
  downloads: () => reply(/[?&]downloads=server/.test(window.location.href)
    ? { ...data.downloads, source: "server" as const, items: data.downloads.items.map((item) => ({ ...item, url: `https://lore.brightwater.example/downloads/${item.file}` })) }
    : data.downloads),
  changeSummary: (id, revision) => {
    const all = data.changes(id, revision);
    if (!all) return missing();
    const count = (kind: string) => all.filter((change) => change.kind === kind).length;
    const list = data.revisions(id);
    const parent = list[list.findIndex((item) => item.signature === revision) + 1]?.signature;
    return reply({ parent, added: count("added"), modified: count("modified"), deleted: count("deleted"), moved: count("moved"), copied: count("copied"), total: all.length });
  },
  locks: (id) => reply({ items: data.locks(id), canRelease: true }),
  releaseLock: readOnly,
  downloadUrl: (_id, path) => notice(`the file ${path}`),
  grants: (id) => reply({ items: data.grants(id) }),
  grantSubjects: (_id, q) => reply({ items: subjects(q) }),
  setGrant: readOnly,
  removeGrant: readOnly,
  importGrants: readOnly,
  grantsExportUrl: () => notice("the repository's access list as CSV"),
  users: (q = "") => reply(page(data.users.filter((user) => matches(q, user.username, user.displayName, user.email)))),
  searchADUsers: (q) => reply({ items: directoryPeople.filter((person) => matches(q, person.username, person.displayName)).map((person) => ({ ...person, email: `${person.username}@brightwater.example` })) }),
  createUser: readOnly,
  accessList: () => reply({ items: data.accessEntries, directory: true }),
  accessSubjects: (q) => reply({ items: subjects(q) }),
  addAccess: readOnly,
  members: () => reply(data.members),
  addPersonalAccess: readOnly,
  updateAccess: readOnly,
  removeAccess: readOnly,
  provisionADUser: readOnly,
  deleteUser: readOnly,
  updateUser: readOnly,
  importUsers: readOnly,
  usersExportUrl: () => notice("the list of users as CSV"),
  repositories: (q = "") => reply(page(data.repositories.filter((repository) => matches(q, repository.name, repository.description)))),
  createRepository: readOnly,
  deleteRepository: readOnly,
  updateRepository: readOnly,
  settings: () => reply(loadSettings()),
  saveSettings: (settings) => {
    // Registration is the server's, not appearance: changing it is refused like any change.
    if (settings.registrationEnabled !== loadSettings().registrationEnabled) return readOnly();
    localStorage.setItem(settingsKey, JSON.stringify(settings));
    return reply(settings);
  },
  audit: () => reply(page(data.audit)),
};
