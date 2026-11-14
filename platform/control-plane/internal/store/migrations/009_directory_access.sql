-- Active Directory accounts are admitted only through this list: the account itself or a group
-- it belongs to (nested groups included). Local accounts aren't affected.
CREATE TABLE IF NOT EXISTS directory_access (
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user', 'group')),
    subject TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (subject_type, subject)
);
ALTER TABLE users ADD COLUMN IF NOT EXISTS access_denied BOOLEAN NOT NULL DEFAULT FALSE;
-- The list starts empty, so no directory account is admitted until an administrator adds it.
UPDATE users SET access_denied = TRUE WHERE directory_id <> '';
