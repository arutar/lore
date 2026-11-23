-- The administrator's list of tokens pages through the unrevoked ones, newest first, and
-- searches token names like the other lists (users' names already have trigram indexes).
CREATE INDEX IF NOT EXISTS mcp_tokens_active_created ON mcp_tokens (created_at DESC, id DESC) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS mcp_tokens_name_trgm_idx ON mcp_tokens USING gin (lower(name) gin_trgm_ops);
