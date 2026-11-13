-- Search: substring matches on names use trigram indexes instead of reading every row.
-- pg_trgm ships with PostgreSQL and is a trusted extension (no superuser needed since 13).
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS repositories_name_trgm_idx ON repositories USING gin (lower(name) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS repositories_description_trgm_idx ON repositories USING gin (lower(description) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS users_username_trgm_idx ON users USING gin (lower(username) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS users_display_name_trgm_idx ON users USING gin (lower(display_name) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS users_email_trgm_idx ON users USING gin (lower(email) gin_trgm_ops);
-- "Does anyone belong to this group" when a grant names a group.
CREATE INDEX IF NOT EXISTS users_groups_idx ON users USING gin (groups);
-- "Is there an active administrator" on every sign-in of the bootstrap account.
CREATE INDEX IF NOT EXISTS users_active_admins_idx ON users (id) WHERE role = 'admin' AND NOT disabled AND NOT directory_disabled;
