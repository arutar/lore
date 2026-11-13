-- directory_id is the Active Directory objectGUID of a federated account; empty for local
-- accounts. Groups are refreshed from the directory by that ID, never by username.
ALTER TABLE users ADD COLUMN IF NOT EXISTS directory_id TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS directory_disabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS groups_synced_at TIMESTAMPTZ;
