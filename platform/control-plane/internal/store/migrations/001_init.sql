CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    email TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL CHECK (role IN ('admin', 'user')),
    groups TEXT[] NOT NULL DEFAULT '{}',
    source TEXT NOT NULL DEFAULT 'local',
    disabled BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS repositories (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    owner_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS grants (
    repository_id TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    subject_type TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    permission TEXT NOT NULL CHECK (permission IN ('none', 'read', 'write', 'owner')),
    PRIMARY KEY (repository_id, subject_type, subject)
);

CREATE INDEX IF NOT EXISTS grants_subject_idx ON grants(subject_type, subject, repository_id, permission);

CREATE TABLE IF NOT EXISTS settings (
    id TEXT PRIMARY KEY,
    registration_enabled BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS audit_events (
    id BIGSERIAL PRIMARY KEY,
    actor_id TEXT NOT NULL,
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL,
    before TEXT NOT NULL DEFAULT '',
    after TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id_hash TEXT PRIMARY KEY,
    csrf_token TEXT NOT NULL,
    user_id TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS sessions_expiry_idx ON sessions(expires_at);
