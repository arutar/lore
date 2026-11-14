-- Entries on the access list carry a role and can be switched off without being removed; a
-- group's role applies to all its members.
ALTER TABLE directory_access ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin'));
ALTER TABLE directory_access ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT FALSE;
-- The Aurora theme was removed.
UPDATE settings SET theme = 'midnight' WHERE theme = 'aurora';
