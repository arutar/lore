package store

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/model"
)

type mcpTokenRecord struct {
	token model.MCPToken
	hash  string
}

func (m *Memory) SetUserMCP(_ context.Context, actorID, id string, disabled bool) (model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[id]
	if !ok || user.Deleted {
		return model.User{}, ErrNotFound
	}
	if user.MCPDisabled != disabled {
		user.MCPDisabled = disabled
		m.users[id] = user
		m.appendAuditLocked(actorID, "user.mcp", "user", id, switchState(!disabled), switchState(disabled))
	}
	return cloneUser(user), nil
}

func (m *Memory) SetAccessMCP(_ context.Context, actorID string, subjectType model.SubjectType, subject string, disabled bool) (model.AccessEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := accessKey(subjectType, subject)
	entry, ok := m.access[key]
	if !ok {
		return model.AccessEntry{}, ErrNotFound
	}
	if entry.MCPDisabled != disabled {
		entry.MCPDisabled = disabled
		m.access[key] = entry
		m.appendAccessAuditLocked(actorID, "access.mcp", subjectType, entry.Subject, switchState(!disabled), switchState(disabled))
	}
	return entry, nil
}

func (m *Memory) CreateMCPToken(_ context.Context, token model.MCPToken, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[token.UserID]; !ok {
		return ErrNotFound
	}
	for _, record := range m.mcpTokens {
		if record.hash == hash {
			return ErrConflict
		}
	}
	if _, ok := m.mcpTokens[token.ID]; ok {
		return ErrConflict
	}
	token.Repositories = append([]string{}, token.Repositories...)
	m.mcpTokens[token.ID] = mcpTokenRecord{token: token, hash: hash}
	m.appendAuditLocked(token.UserID, "mcp.token.create", "user", token.UserID, "", token.Name)
	return nil
}

func (m *Memory) ListMCPTokens(_ context.Context, userID string) ([]model.MCPToken, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tokens := []model.MCPToken{}
	for _, record := range m.mcpTokens {
		if record.token.RevokedAt == nil && (userID == "" || record.token.UserID == userID) {
			tokens = append(tokens, cloneMCPToken(record.token))
		}
	}
	sort.Slice(tokens, func(i, j int) bool {
		if !tokens[i].CreatedAt.Equal(tokens[j].CreatedAt) {
			return tokens[i].CreatedAt.After(tokens[j].CreatedAt)
		}
		return tokens[i].ID < tokens[j].ID
	})
	return tokens, nil
}

func (m *Memory) GetMCPToken(_ context.Context, id string) (model.MCPToken, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, ok := m.mcpTokens[id]
	if !ok {
		return model.MCPToken{}, ErrNotFound
	}
	return cloneMCPToken(record.token), nil
}

func (m *Memory) GetMCPTokenByHash(_ context.Context, hash string) (model.MCPToken, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, record := range m.mcpTokens {
		if record.hash == hash {
			return cloneMCPToken(record.token), nil
		}
	}
	return model.MCPToken{}, ErrNotFound
}

func (m *Memory) RevokeMCPToken(_ context.Context, actorID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.mcpTokens[id]
	if !ok || record.token.RevokedAt != nil {
		return ErrNotFound
	}
	now := time.Now().UTC()
	record.token.RevokedAt = &now
	m.mcpTokens[id] = record
	for hash, link := range m.mcpLinks {
		if link.TokenID == id {
			delete(m.mcpLinks, hash)
		}
	}
	m.appendAuditLocked(actorID, "mcp.token.revoke", "user", record.token.UserID, record.token.Name, "")
	return nil
}

func (m *Memory) TouchMCPToken(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.mcpTokens[id]
	if !ok {
		return ErrNotFound
	}
	at = at.UTC()
	record.token.LastUsedAt = &at
	m.mcpTokens[id] = record
	return nil
}

func (m *Memory) CreateMCPLink(_ context.Context, hash string, link model.MCPLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for key, existing := range m.mcpLinks {
		if !now.Before(existing.ExpiresAt) {
			delete(m.mcpLinks, key)
		}
	}
	if _, ok := m.mcpLinks[hash]; ok {
		return ErrConflict
	}
	m.mcpLinks[hash] = link
	return nil
}

func (m *Memory) GetMCPLink(_ context.Context, hash string, now time.Time) (model.MCPLink, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	link, ok := m.mcpLinks[hash]
	if !ok || !now.Before(link.ExpiresAt) {
		return model.MCPLink{}, ErrNotFound
	}
	return link, nil
}

func cloneMCPToken(token model.MCPToken) model.MCPToken {
	token.Repositories = slices.Clone(token.Repositories)
	if token.Repositories == nil {
		token.Repositories = []string{}
	}
	for _, field := range []**time.Time{&token.ExpiresAt, &token.LastUsedAt, &token.RevokedAt} {
		if *field != nil {
			value := **field
			*field = &value
		}
	}
	return token
}

// switchState names an MCP switch's position in the audit log.
func switchState(disabled bool) string {
	if disabled {
		return "off"
	}
	return "on"
}
