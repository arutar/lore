// Fictional data for the read-only demo (npm run build:demo). Nothing here comes from a real
// installation: the studio, people, groups and files are invented. Times are relative to when the
// page is opened, so the demo always looks recently used.
import type { AccessEntry, AuditEvent, LabeledGrant, LastChange, Members, PublicConfig, Repository, RepositoryLock, Revision, RevisionChange, TreeNode, User } from "../types";

const now = Date.now();
const ago = (hours: number) => new Date(now - hours * 3_600_000).toISOString();

// A stable 64-hex "signature" for a revision, so the history looks like Lore's.
function signature(seed: string) {
  let hash = 2166136261;
  let out = "";
  for (let round = 0; out.length < 64; round++) {
    for (const character of `${seed}:${round}`) hash = Math.imul(hash ^ character.charCodeAt(0), 16777619) >>> 0;
    out += hash.toString(16).padStart(8, "0");
  }
  return out.slice(0, 64);
}

const person = (id: string, username: string, displayName: string, role: User["role"], source: string, groups: string[] = [], hours = 30): User => ({
  id, username, displayName, role, source, groups,
  email: source === "local" ? username : `${username}@brightwater.example`,
  disabled: false, createdAt: ago(24 * 60 + hours), updatedAt: ago(hours),
  ...(source === "ad" ? { directoryId: `${id}-guid`, groupsSyncedAt: ago(0.1) } : {}),
});

export const groupNames: Record<string, string> = {
  "grp-artists": "studio-artists",
  "grp-engineers": "studio-engineers",
  "grp-leads": "studio-leads",
};

export const users: User[] = [
  person("u-admin", "admin", "Administrator", "admin", "local", [], 0.2),
  person("u-anna", "apetrova", "Anna Petrova", "admin", "ad", ["grp-leads", "grp-engineers"], 1),
  person("u-mark", "mchen", "Mark Chen", "user", "ad", ["grp-engineers"], 2),
  person("u-lina", "lsato", "Lina Sato", "user", "ad", ["grp-artists"], 3),
  person("u-omar", "ohaddad", "Omar Haddad", "user", "ad", ["grp-artists"], 5),
  person("u-elena", "eruiz", "Elena Ruiz", "user", "ad", ["grp-artists", "grp-leads"], 8),
  person("u-pavel", "porlov", "Pavel Orlov", "user", "ad", ["grp-engineers"], 20),
  person("u-kate", "kate.morgan@partner.example", "Kate Morgan", "user", "local", [], 50),
];
export const me = users[0];
const byId = (id: string) => users.find((user) => user.id === id)!;

export const config: PublicConfig = {
  registrationEnabled: false, oidcEnabled: true, devAuthEnabled: false, adEnabled: true,
  externalUrl: "https://lore.brightwater.example", nativeRepositoryUrl: "lore://lore.brightwater.example",
  theme: "midnight", language: "en",
};

type RepoSpec = {
  repository: Repository;
  // Newest first: [message, author id, hours ago].
  revisions: [string, string, number][];
  // [path, size in bytes, which of `revisions` last changed it: 1 is the newest].
  files: [string, number, number][];
  locks?: [string, string, number][];
  grants: Omit<LabeledGrant, "repositoryId">[];
};

const repo = (id: string, name: string, description: string, owner: string, isPublic: boolean, created: number, updated: number): Repository => ({
  id, name, description, ownerId: owner, ownerName: byId(owner).displayName, public: isPublic, createdAt: ago(created), updatedAt: ago(updated),
});

const user = (id: string, permission: LabeledGrant["permission"]): Omit<LabeledGrant, "repositoryId"> => {
  const account = byId(id);
  return { subjectType: "user", subject: id, permission, label: account.displayName, detail: `${account.username} · ${account.email}` };
};
const group = (id: string, permission: LabeledGrant["permission"]): Omit<LabeledGrant, "repositoryId"> =>
  ({ subjectType: "group", subject: id, permission, label: groupNames[id], detail: `CN=${groupNames[id]},OU=Groups,DC=brightwater,DC=example` });

const specs: RepoSpec[] = [
  {
    repository: repo("hollow-keep", "game/hollow-keep", "Hollow Keep — the main game project: code, levels and packaged assets.", "u-anna", false, 24 * 120, 1.5),
    revisions: [
      ["Lighting pass on the courtyard level", "u-elena", 1.5],
      ["Fix the save system losing quest flags on reload", "u-mark", 4],
      ["New textures for the castle gate", "u-lina", 7],
      ["Merge the audio streaming branch", "u-pavel", 26],
      ["Courtyard: block-out of the eastern wall", "u-omar", 30],
      ["Player controller: ledge grab", "u-mark", 52],
      ["Import character rigs v3", "u-lina", 75],
      ["Build: strip debug symbols from shipping builds", "u-pavel", 98],
      ["Initial project layout", "u-anna", 24 * 120],
    ],
    files: [
      ["README.md", 2_340, 9], ["project.lore", 612, 9],
      ["source/game/player_controller.cpp", 48_210, 6], ["source/game/player_controller.h", 6_120, 6],
      ["source/game/save_system.cpp", 22_904, 2], ["source/game/save_system.h", 3_310, 2],
      ["source/engine/audio/streaming.cpp", 31_775, 4], ["source/engine/audio/streaming.h", 4_016, 4],
      ["source/engine/render/lighting.cpp", 57_342, 1],
      ["levels/courtyard/courtyard.level", 18_400_112, 1], ["levels/courtyard/lightmap.bin", 64_002_048, 1],
      ["levels/castle/castle.level", 22_100_903, 5], ["levels/castle/navmesh.bin", 3_402_100, 5],
      ["art/textures/gate_diffuse.png", 8_389_120, 3], ["art/textures/gate_normal.png", 8_389_120, 3],
      ["art/textures/wall_stone_diffuse.png", 4_194_816, 5],
      ["art/characters/knight/knight.fbx", 12_844_032, 7], ["art/characters/knight/knight_rig.fbx", 2_110_464, 7],
      ["audio/music/main_theme.ogg", 6_201_344, 4], ["audio/sfx/gate_open.wav", 1_204_224, 4],
      ["tools/build/package.py", 9_812, 8],
    ],
    locks: [["levels/courtyard/courtyard.level", "u-elena", 2], ["art/characters/knight/knight.fbx", "u-lina", 20]],
    grants: [user("u-anna", "owner"), group("grp-engineers", "write"), group("grp-artists", "write"), user("u-kate", "read")],
  },
  {
    repository: repo("character-pack", "art/character-pack", "Source files for characters: sculpts, rigs and textures.", "u-elena", false, 24 * 90, 7),
    revisions: [
      ["Knight: final texture set", "u-lina", 7],
      ["Rogue: retopology", "u-omar", 40],
      ["Shared skeleton v3", "u-elena", 120],
      ["Initial import", "u-elena", 24 * 90],
    ],
    files: [
      ["knight/knight.blend", 184_221_184, 1], ["knight/textures/knight_albedo.tga", 16_777_216, 1], ["knight/textures/knight_normal.tga", 16_777_216, 1],
      ["rogue/rogue.blend", 152_043_520, 2], ["rogue/rogue_retopo.blend", 61_210_624, 2],
      ["shared/skeleton_v3.fbx", 1_604_608, 3], ["README.md", 1_120, 4],
    ],
    locks: [["rogue/rogue.blend", "u-omar", 3]],
    grants: [user("u-elena", "owner"), group("grp-artists", "write"), group("grp-leads", "read")],
  },
  {
    repository: repo("build-pipeline", "tools/build-pipeline", "Build and packaging scripts used by CI and by hand.", "u-pavel", false, 24 * 60, 26),
    revisions: [
      ["Parallel shader compilation", "u-pavel", 26],
      ["Package Linux builds", "u-mark", 70],
      ["Initial scripts", "u-pavel", 24 * 60],
    ],
    files: [
      ["build.py", 14_220, 1], ["package.py", 9_812, 2], ["shaders/compile.py", 7_450, 1], ["config/targets.toml", 1_904, 2], ["README.md", 2_010, 3],
    ],
    grants: [user("u-pavel", "owner"), group("grp-engineers", "write")],
  },
  {
    repository: repo("handbook", "studio/handbook", "How the studio works: onboarding, conventions, release checklist.", "u-anna", true, 24 * 200, 50),
    revisions: [
      ["Release checklist: add the localization step", "u-anna", 50],
      ["Naming conventions for assets", "u-elena", 200],
      ["First version", "u-anna", 24 * 200],
    ],
    files: [
      ["onboarding.md", 8_210, 3], ["conventions/assets.md", 5_604, 2], ["conventions/code.md", 6_903, 3], ["release-checklist.md", 3_402, 1],
    ],
    grants: [user("u-anna", "owner"), { subjectType: "anonymous", subject: "", permission: "read", label: "Everyone" }],
  },
  {
    repository: repo("ambience", "audio/ambience", "Ambient loops and field recordings.", "u-pavel", false, 24 * 30, 120),
    revisions: [["Forest and castle loops", "u-pavel", 120], ["Initial recordings", "u-pavel", 24 * 30]],
    files: [["forest/day_loop.wav", 52_428_800, 1], ["forest/night_loop.wav", 48_234_496, 1], ["castle/hall_loop.wav", 38_797_312, 1], ["field/rain_raw.wav", 104_857_600, 2]],
    grants: [user("u-pavel", "owner"), group("grp-leads", "read")],
  },
  {
    repository: repo("prototype-sandbox", "team/prototype-sandbox", "", "u-mark", false, 6, 6),
    revisions: [],
    files: [],
    grants: [user("u-mark", "owner"), { subjectType: "authenticated", subject: "", permission: "read", label: "All signed-in users" }],
  },
];

export const repositories: Repository[] = specs.map((spec) => spec.repository);

const spec = (id: string) => specs.find((item) => item.repository.id === id);

export function revisions(id: string): Revision[] {
  const list = spec(id)?.revisions ?? [];
  return list.map(([message, author, hours], index) => ({
    number: list.length - index, signature: signature(`${id}:${list.length - index}`), message,
    author: byId(author).displayName, committer: byId(author).displayName, committedAt: ago(hours),
  }));
}

function lastChange(id: string, newest: number): LastChange | undefined {
  const revision = revisions(id)[newest - 1];
  return revision && { revision: revision.signature, number: revision.number, author: revision.author, message: revision.message, committedAt: revision.committedAt };
}

/** The entries directly below `path` ("" for the root), folders carrying their newest change. */
export function tree(id: string, path: string): TreeNode[] | undefined {
  const current = spec(id);
  if (!current) return undefined;
  const prefix = path ? `${path}/` : "";
  const entries = new Map<string, TreeNode>();
  for (const [file, size, newest] of current.files) {
    if (!file.startsWith(prefix)) continue;
    const rest = file.slice(prefix.length);
    const slash = rest.indexOf("/");
    const change = lastChange(id, newest);
    if (slash < 0) {
      entries.set(file, { path: file, type: "file", size, mode: 0o644, executable: false, lastChange: change });
      continue;
    }
    const folder = prefix + rest.slice(0, slash);
    const existing = entries.get(folder);
    const newer = !existing?.lastChange || (change && change.number > existing.lastChange.number);
    entries.set(folder, { path: folder, type: "directory", size: 0, mode: 0o755, executable: false, lastChange: newer ? change : existing?.lastChange });
  }
  return [...entries.values()];
}

/** What revision `signature` changed: the files it last changed (all of them added in the first
 * revision), a moved and a deleted file in the newest one, and a long list of new textures in any
 * revision about textures, so the panel has something to scroll. */
export function changes(id: string, signature: string): RevisionChange[] | undefined {
  const current = spec(id);
  const list = revisions(id);
  const index = list.findIndex((revision) => revision.signature === signature);
  if (!current || index < 0) return undefined;
  const first = index === list.length - 1;
  const result: RevisionChange[] = current.files.filter(([, , newest]) => newest === index + 1 || first)
    .map(([path]) => ({ kind: first ? "added" : "modified", path }));
  if (index === 0 && !first) {
    const [path] = current.files[0] ?? ["readme.txt"];
    const slash = path.lastIndexOf("/");
    result.push({ kind: "moved", path: `${path.slice(0, slash + 1)}archive/${path.slice(slash + 1)}`, from: `${path.slice(0, slash + 1)}old/${path.slice(slash + 1)}` });
    result.push({ kind: "deleted", path: `${path.slice(0, slash + 1)}obsolete_backup.bin` });
  }
  if (/texture/i.test(list[index].message)) {
    for (let n = 1; n <= 1500; n++) result.push({ kind: "added", path: `Art/Textures/Batch${String(Math.ceil(n / 100)).padStart(2, "0")}/tex_${String(n).padStart(4, "0")}.png` });
  }
  return result;
}

export function locks(id: string): RepositoryLock[] {
  return (spec(id)?.locks ?? []).map(([path, owner, hours]) => ({
    branchId: "main", path, hash: signature(`${id}:${path}`).slice(0, 16), owner: byId(owner).displayName, lockedAt: ago(hours),
  }));
}

export function grants(id: string): LabeledGrant[] {
  return (spec(id)?.grants ?? []).map((grant) => ({ ...grant, repositoryId: id }));
}

export const accessEntries: AccessEntry[] = [
  { subjectType: "group", subject: "grp-artists", name: "studio-artists", detail: "CN=studio-artists,OU=Groups,DC=brightwater,DC=example", role: "user", disabled: false, createdAt: ago(24 * 100) },
  { subjectType: "group", subject: "grp-engineers", name: "studio-engineers", detail: "CN=studio-engineers,OU=Groups,DC=brightwater,DC=example", role: "user", disabled: false, createdAt: ago(24 * 100) },
  { subjectType: "group", subject: "grp-leads", name: "studio-leads", detail: "CN=studio-leads,OU=Groups,DC=brightwater,DC=example", role: "admin", disabled: false, createdAt: ago(24 * 100) },
  { subjectType: "user", subject: "u-pavel-guid", name: "Pavel Orlov", detail: "porlov · porlov@brightwater.example", role: "user", disabled: false, createdAt: ago(24 * 40) },
  { subjectType: "user", subject: "u-sven-guid", name: "Sven Lund", detail: "slund · slund@brightwater.example", role: "user", disabled: false, createdAt: ago(3) },
];

export const members: Members = {
  local: users.filter((account) => account.source === "local"),
  accounts: accessEntries.filter((entry) => entry.subjectType === "user").map((entry) => ({ ...entry, user: users.find((account) => account.directoryId === entry.subject) })),
  groups: accessEntries.filter((entry) => entry.subjectType === "group").map((entry) => ({ ...entry, members: users.filter((account) => account.groups.includes(entry.subject)) })),
  directory: true,
};

let eventId = 100;
const event = (hours: number, actor: string, action: string, targetType: string, targetId: string, targetName: string, extra: Partial<AuditEvent> = {}): AuditEvent =>
  ({ id: eventId--, actorId: actor, actorName: byId(actor).displayName, action, targetType, targetId, targetName, createdAt: ago(hours), ...extra });

export const audit: AuditEvent[] = [
  event(2, "u-anna", "grant.set", "repository", "hollow-keep", "game/hollow-keep", { subjectType: "user", subject: "u-kate", subjectName: "Kate Morgan", before: "", after: "read" }),
  event(3, "u-admin", "user.provision", "user", "u-sven", "Sven Lund"),
  event(6, "u-mark", "repository.create", "repository", "prototype-sandbox", "team/prototype-sandbox"),
  event(6, "u-mark", "grant.set", "repository", "prototype-sandbox", "team/prototype-sandbox", { subjectType: "authenticated", before: "", after: "read" }),
  event(20, "u-elena", "lock.release", "repository", "character-pack", "art/character-pack", { subjectType: "user", subjectName: "Omar Haddad", before: "rogue/rogue_old.blend" }),
  event(26, "u-admin", "settings.update", "settings", "global", "", { before: "true:midnight:en", after: "false:midnight:en" }),
  event(50, "u-anna", "repository.update", "repository", "handbook", "studio/handbook", { before: "docs/handbook", after: "studio/handbook" }),
  event(52, "u-anna", "grant.set", "repository", "handbook", "studio/handbook", { subjectType: "anonymous", before: "", after: "read" }),
  event(70, "u-admin", "user.update", "user", "u-anna", "Anna Petrova", { before: "user", after: "admin" }),
  event(98, "u-pavel", "grant.remove", "repository", "build-pipeline", "tools/build-pipeline", { subjectType: "group", subject: "grp-artists", subjectName: "studio-artists", before: "read", after: "" }),
  event(120, "u-admin", "user.create", "user", "u-kate", "Kate Morgan"),
];
