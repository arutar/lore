-- MCP: personal tokens for the /mcp endpoint, the short-lived download links MCP clients ask
-- for, and the switches that turn MCP off for an account or for an Active Directory entry.
ALTER TABLE users ADD COLUMN IF NOT EXISTS mcp_disabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE directory_access ADD COLUMN IF NOT EXISTS mcp_disabled BOOLEAN NOT NULL DEFAULT FALSE;

-- Only the SHA-256 of a token is kept; hint is its first characters, to tell tokens apart.
CREATE TABLE IF NOT EXISTS mcp_tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    hint TEXT NOT NULL,
    hash TEXT NOT NULL UNIQUE,
    repositories TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS mcp_tokens_user ON mcp_tokens(user_id);

CREATE TABLE IF NOT EXISTS mcp_links (
    hash TEXT PRIMARY KEY,
    token_id TEXT NOT NULL REFERENCES mcp_tokens(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL,
    repository_id TEXT NOT NULL,
    path TEXT NOT NULL,
    revision TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS mcp_links_expiry ON mcp_links(expires_at);
