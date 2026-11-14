-- Removed local accounts stay as renamed, hidden records so the audit log keeps their names.
ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted BOOLEAN NOT NULL DEFAULT FALSE;
