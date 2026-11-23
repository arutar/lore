package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arutar/lore/platform/control-plane/internal/model"
)

func (p *Postgres) SetUserMCP(ctx context.Context, actorID, id string, disabled bool) (model.User, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.User{}, fmt.Errorf("begin user MCP update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before bool
	err = tx.QueryRow(ctx, "SELECT mcp_disabled FROM users WHERE id = $1 AND NOT deleted FOR UPDATE", id).Scan(&before)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("lock user: %w", err)
	}
	user, err := scanUser(tx.QueryRow(ctx, "UPDATE users SET mcp_disabled = $2 WHERE id = $1 RETURNING "+userColumns, id, disabled))
	if err != nil {
		return model.User{}, err
	}
	if before != disabled {
		if err := insertAudit(ctx, tx, actorID, "user.mcp", "user", id, switchState(before), switchState(disabled)); err != nil {
			return model.User{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return model.User{}, fmt.Errorf("commit user MCP update: %w", err)
	}
	return user, nil
}

func (p *Postgres) SetAccessMCP(ctx context.Context, actorID string, subjectType model.SubjectType, subject string, disabled bool) (model.AccessEntry, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.AccessEntry{}, fmt.Errorf("begin access MCP update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var entry model.AccessEntry
	err = tx.QueryRow(ctx, `
		SELECT subject_type, subject, name, detail, role, disabled, mcp_disabled, created_at FROM directory_access
		WHERE subject_type = $1 AND subject = $2 FOR UPDATE`, subjectType, subject).
		Scan(&entry.SubjectType, &entry.Subject, &entry.Name, &entry.Detail, &entry.Role, &entry.Disabled, &entry.MCPDisabled, &entry.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.AccessEntry{}, ErrNotFound
	}
	if err != nil {
		return model.AccessEntry{}, fmt.Errorf("read access entry: %w", err)
	}
	if entry.MCPDisabled == disabled {
		return entry, nil
	}
	if _, err := tx.Exec(ctx, "UPDATE directory_access SET mcp_disabled = $3 WHERE subject_type = $1 AND subject = $2", subjectType, subject, disabled); err != nil {
		return model.AccessEntry{}, fmt.Errorf("update access entry: %w", err)
	}
	if err := insertAccessAudit(ctx, tx, actorID, "access.mcp", subjectType, subject, switchState(entry.MCPDisabled), switchState(disabled)); err != nil {
		return model.AccessEntry{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.AccessEntry{}, fmt.Errorf("commit access MCP update: %w", err)
	}
	entry.MCPDisabled = disabled
	return entry, nil
}

func (p *Postgres) CreateMCPToken(ctx context.Context, token model.MCPToken, hash string) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin MCP token create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	repositories := token.Repositories
	if repositories == nil {
		repositories = []string{}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO mcp_tokens(id, user_id, name, hint, hash, repositories, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		token.ID, token.UserID, token.Name, token.Hint, hash, repositories, token.CreatedAt, token.ExpiresAt)
	if err != nil {
		return classifyConflict("create MCP token", err)
	}
	if err := insertAudit(ctx, tx, token.UserID, "mcp.token.create", "user", token.UserID, "", token.Name); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit MCP token create: %w", err)
	}
	return nil
}

const mcpTokenColumns = "id, user_id, name, hint, repositories, created_at, expires_at, last_used_at, revoked_at"

func scanMCPToken(row pgx.Row) (model.MCPToken, error) {
	var token model.MCPToken
	err := row.Scan(&token.ID, &token.UserID, &token.Name, &token.Hint, &token.Repositories, &token.CreatedAt, &token.ExpiresAt, &token.LastUsedAt, &token.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.MCPToken{}, ErrNotFound
	}
	if err != nil {
		return model.MCPToken{}, fmt.Errorf("scan MCP token: %w", err)
	}
	if token.Repositories == nil {
		token.Repositories = []string{}
	}
	return token, nil
}

func (p *Postgres) ListMCPTokens(ctx context.Context, userID string) ([]model.MCPToken, error) {
	rows, err := p.pool.Query(ctx, "SELECT "+mcpTokenColumns+" FROM mcp_tokens WHERE revoked_at IS NULL AND ($1 = '' OR user_id = $1) ORDER BY created_at DESC, id", userID)
	if err != nil {
		return nil, fmt.Errorf("list MCP tokens: %w", err)
	}
	defer rows.Close()
	tokens := []model.MCPToken{}
	for rows.Next() {
		token, err := scanMCPToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (p *Postgres) GetMCPToken(ctx context.Context, id string) (model.MCPToken, error) {
	return scanMCPToken(p.pool.QueryRow(ctx, "SELECT "+mcpTokenColumns+" FROM mcp_tokens WHERE id = $1", id))
}

func (p *Postgres) GetMCPTokenByHash(ctx context.Context, hash string) (model.MCPToken, error) {
	return scanMCPToken(p.pool.QueryRow(ctx, "SELECT "+mcpTokenColumns+" FROM mcp_tokens WHERE hash = $1", hash))
}

func (p *Postgres) RevokeMCPToken(ctx context.Context, actorID, id string) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin MCP token revoke: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID, name string
	err = tx.QueryRow(ctx, "UPDATE mcp_tokens SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL RETURNING user_id, name", id, time.Now().UTC()).Scan(&userID, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("revoke MCP token: %w", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM mcp_links WHERE token_id = $1", id); err != nil {
		return fmt.Errorf("drop MCP links: %w", err)
	}
	if err := insertAudit(ctx, tx, actorID, "mcp.token.revoke", "user", userID, name, ""); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit MCP token revoke: %w", err)
	}
	return nil
}

func (p *Postgres) TouchMCPToken(ctx context.Context, id string, at time.Time) error {
	if _, err := p.pool.Exec(ctx, "UPDATE mcp_tokens SET last_used_at = $2 WHERE id = $1", id, at.UTC()); err != nil {
		return fmt.Errorf("touch MCP token: %w", err)
	}
	return nil
}

func (p *Postgres) CreateMCPLink(ctx context.Context, hash string, link model.MCPLink) error {
	if _, err := p.pool.Exec(ctx, "DELETE FROM mcp_links WHERE expires_at <= $1", time.Now().UTC()); err != nil {
		return fmt.Errorf("drop expired MCP links: %w", err)
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO mcp_links(hash, token_id, user_id, repository_id, path, revision, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		hash, link.TokenID, link.UserID, link.RepositoryID, link.Path, link.Revision, link.ExpiresAt.UTC())
	if err != nil {
		return classifyConflict("create MCP link", err)
	}
	return nil
}

func (p *Postgres) GetMCPLink(ctx context.Context, hash string, now time.Time) (model.MCPLink, error) {
	var link model.MCPLink
	err := p.pool.QueryRow(ctx, `
		SELECT token_id, user_id, repository_id, path, revision, expires_at FROM mcp_links
		WHERE hash = $1 AND expires_at > $2`, hash, now.UTC()).
		Scan(&link.TokenID, &link.UserID, &link.RepositoryID, &link.Path, &link.Revision, &link.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.MCPLink{}, ErrNotFound
	}
	if err != nil {
		return model.MCPLink{}, fmt.Errorf("get MCP link: %w", err)
	}
	link.ExpiresAt = link.ExpiresAt.UTC()
	return link, nil
}
