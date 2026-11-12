package store

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/policy"
)

type Memory struct {
	mu           sync.RWMutex
	users        map[string]model.User
	repositories map[string]model.Repository
	grants       map[string]map[string]model.Grant
	sessions     map[string]model.Session
	settings     model.Settings
	audit        []model.AuditEvent
}

func NewMemory() *Memory {
	return &Memory{
		users:        make(map[string]model.User),
		repositories: make(map[string]model.Repository),
		grants:       make(map[string]map[string]model.Grant),
		sessions:     make(map[string]model.Session),
	}
}

func (m *Memory) Close() error { return nil }

func (m *Memory) Catalog(_ context.Context, user *model.User, after string, limit int) (model.Page[model.Repository], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	items := make([]model.Repository, 0, limit)
	for _, id := range m.sortedRepositoryIDs() {
		if id <= after {
			continue
		}
		repository := m.repositories[id]
		if user == nil || user.Role != model.RoleAdmin {
			if !policy.CanRead(user, m.grantListLocked(id)) {
				continue
			}
		}
		for _, grant := range m.grantListLocked(repository.ID) {
			if grant.SubjectType == model.SubjectAnonymous && grant.Permission == model.PermissionRead {
				repository.Public = true
			}
		}
		items = append(items, repository)
		if len(items) == limit {
			break
		}
	}
	hasMore := false
	if len(items) == limit {
		hasMore = m.repositoryExistsAfter(user, items[len(items)-1].ID)
	}
	return pageFrom(items, after, limit, hasMore), nil
}

func (m *Memory) repositoryExistsAfter(user *model.User, after string) bool {
	ids := m.sortedRepositoryIDs()
	for _, id := range ids {
		if id <= after {
			continue
		}
		if user == nil || user.Role != model.RoleAdmin || policy.CanRead(user, m.grantListLocked(id)) {
			return true
		}
	}
	return false
}

func (m *Memory) ListRepositories(_ context.Context, after string, limit int) (model.Page[model.Repository], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	items := make([]model.Repository, 0, limit)
	for _, id := range m.sortedRepositoryIDs() {
		if id > after {
			repository := m.repositories[id]
			for _, grant := range m.grantListLocked(id) {
				if grant.SubjectType == model.SubjectAnonymous && grant.Permission == model.PermissionRead {
					repository.Public = true
				}
			}
			items = append(items, repository)
			if len(items) == limit {
				break
			}
		}
	}
	next := ""
	if len(items) == limit {
		next = items[len(items)-1].ID
	}
	return model.Page[model.Repository]{Items: items, NextCursor: next}, nil
}

func (m *Memory) GetRepository(_ context.Context, id string) (model.Repository, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	repository, ok := m.repositories[id]
	if !ok {
		return model.Repository{}, ErrNotFound
	}
	for _, grant := range m.grantListLocked(id) {
		if grant.SubjectType == model.SubjectAnonymous && grant.Permission == model.PermissionRead {
			repository.Public = true
		}
	}
	return repository, nil
}

func (m *Memory) CreateRepository(_ context.Context, actorID string, repository model.Repository) (model.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, existing := range m.repositories {
		if id == repository.ID && existing.Name == repository.Name {
			repository.CreatedAt = existing.CreatedAt
			repository.UpdatedAt = time.Now().UTC()
			m.repositories[id] = repository
			owner := model.Grant{RepositoryID: id, SubjectType: model.SubjectUser, Subject: repository.OwnerID, Permission: model.PermissionOwner}
			m.grants[id][grantKey(owner)] = owner
			m.appendAuditLocked(actorID, "repository.create", "repository", id, existing.OwnerID, repository.OwnerID)
			return repository, nil
		}
		if existing.Name == repository.Name || id == repository.ID {
			return model.Repository{}, ErrConflict
		}
	}
	now := time.Now().UTC()
	repository.CreatedAt = now
	repository.UpdatedAt = now
	m.repositories[repository.ID] = repository
	m.grants[repository.ID] = make(map[string]model.Grant)
	owner := model.Grant{RepositoryID: repository.ID, SubjectType: model.SubjectUser, Subject: repository.OwnerID, Permission: model.PermissionOwner}
	m.grants[repository.ID][grantKey(owner)] = owner
	m.appendAuditLocked(actorID, "repository.create", "repository", repository.ID, "", repository.Name)
	return repository, nil
}

func (m *Memory) UpdateRepository(_ context.Context, actorID string, repository model.Repository) (model.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.repositories[repository.ID]
	if !ok {
		return model.Repository{}, ErrNotFound
	}
	for id, candidate := range m.repositories {
		if id != repository.ID && candidate.Name == repository.Name {
			return model.Repository{}, ErrConflict
		}
	}
	existing.Name = repository.Name
	existing.Description = repository.Description
	existing.UpdatedAt = time.Now().UTC()
	m.repositories[repository.ID] = existing
	m.appendAuditLocked(actorID, "repository.update", "repository", repository.ID, "", repository.Name)
	return existing, nil
}

func (m *Memory) DeleteRepository(_ context.Context, actorID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	repository, ok := m.repositories[id]
	if !ok {
		return nil
	}
	delete(m.repositories, id)
	delete(m.grants, id)
	m.appendAuditLocked(actorID, "repository.delete", "repository", id, repository.Name, "")
	return nil
}

func (m *Memory) ListUsers(_ context.Context, after string, limit int) (model.Page[model.User], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.users))
	for id := range m.users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]model.User, 0, limit)
	for _, id := range ids {
		if id > after {
			items = append(items, cloneUser(m.users[id]))
			if len(items) == limit {
				break
			}
		}
	}
	next := ""
	if len(items) == limit {
		next = items[len(items)-1].ID
	}
	return model.Page[model.User]{Items: items, NextCursor: next}, nil
}

func (m *Memory) GetUser(_ context.Context, id string) (model.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, ok := m.users[id]
	if !ok {
		return model.User{}, ErrNotFound
	}
	return cloneUser(user), nil
}

func (m *Memory) UpsertUser(_ context.Context, user model.User) (model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	existing, ok := m.users[user.ID]
	if ok {
		user.Role = existing.Role
		user.Disabled = existing.Disabled
		user.CreatedAt = existing.CreatedAt
	} else {
		if user.Role == "" {
			user.Role = model.RoleUser
		}
		user.CreatedAt = now
	}
	user.UpdatedAt = now
	user.Groups = append([]string(nil), user.Groups...)
	m.users[user.ID] = user
	return cloneUser(user), nil
}

func (m *Memory) UpdateUser(_ context.Context, actorID, id string, role model.Role, disabled bool) (model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[id]
	if !ok {
		return model.User{}, ErrNotFound
	}
	before := string(user.Role)
	user.Role = role
	user.Disabled = disabled
	user.UpdatedAt = time.Now().UTC()
	m.users[id] = user
	m.appendAuditLocked(actorID, "user.update", "user", id, before, string(role))
	return cloneUser(user), nil
}

func (m *Memory) ListGrants(_ context.Context, repositoryID string) ([]model.Grant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.repositories[repositoryID]; !ok {
		return nil, ErrNotFound
	}
	return m.grantListLocked(repositoryID), nil
}

func (m *Memory) SetGrant(_ context.Context, actorID string, grant model.Grant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.repositories[grant.RepositoryID]; !ok {
		return ErrNotFound
	}
	if m.grants[grant.RepositoryID] == nil {
		m.grants[grant.RepositoryID] = make(map[string]model.Grant)
	}
	key := grantKey(grant)
	before := ""
	if existing, ok := m.grants[grant.RepositoryID][key]; ok {
		before = string(existing.Permission)
	}
	m.grants[grant.RepositoryID][key] = grant
	m.appendAuditLocked(actorID, "grant.set", "repository", grant.RepositoryID, before, string(grant.Permission))
	return nil
}

func (m *Memory) RemoveGrant(_ context.Context, actorID string, grant model.Grant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.repositories[grant.RepositoryID]; !ok {
		return ErrNotFound
	}
	key := grantKey(grant)
	existing, ok := m.grants[grant.RepositoryID][key]
	if !ok {
		return nil
	}
	delete(m.grants[grant.RepositoryID], key)
	m.appendAuditLocked(actorID, "grant.remove", "repository", grant.RepositoryID, string(existing.Permission), "")
	return nil
}

func (m *Memory) GetSettings(_ context.Context) (model.Settings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	settings := m.settings
	if settings.Theme == "" {
		settings.Theme = model.DefaultTheme
	}
	return settings, nil
}

func (m *Memory) UpdateSettings(_ context.Context, actorID string, settings model.Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.settings.RegistrationEnabled
	m.settings = settings
	m.appendAuditLocked(actorID, "settings.update", "settings", "global", boolString(before), boolString(settings.RegistrationEnabled))
	return nil
}

func (m *Memory) ListAudit(_ context.Context, after int64, limit int) (model.Page[model.AuditEvent], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]model.AuditEvent, 0, limit)
	for _, event := range m.audit {
		if event.ID > after {
			items = append(items, event)
			if len(items) == limit {
				break
			}
		}
	}
	next := ""
	if len(items) == limit {
		next = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	return model.Page[model.AuditEvent]{Items: items, NextCursor: next}, nil
}

func (m *Memory) CreateSession(_ context.Context, session model.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.IDHash] = session
	return nil
}

func (m *Memory) GetSession(_ context.Context, idHash string, now time.Time) (model.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, ok := m.sessions[idHash]
	if !ok || !session.ExpiresAt.After(now) {
		return model.Session{}, ErrNotFound
	}
	return session, nil
}

func (m *Memory) DeleteSession(_ context.Context, idHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, idHash)
	return nil
}

func (m *Memory) sortedRepositoryIDs() []string {
	ids := make([]string, 0, len(m.repositories))
	for id := range m.repositories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (m *Memory) grantListLocked(repositoryID string) []model.Grant {
	grants := make([]model.Grant, 0, len(m.grants[repositoryID]))
	for _, grant := range m.grants[repositoryID] {
		grants = append(grants, grant)
	}
	return grants
}

func (m *Memory) appendAuditLocked(actorID, action, targetType, targetID, before, after string) {
	m.audit = append(m.audit, model.AuditEvent{
		ID:         int64(len(m.audit) + 1),
		ActorID:    actorID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Before:     before,
		After:      after,
		CreatedAt:  time.Now().UTC(),
	})
}

func cloneUser(user model.User) model.User {
	user.Groups = append([]string(nil), user.Groups...)
	return user
}

func grantKey(grant model.Grant) string {
	return string(grant.SubjectType) + "\x00" + grant.Subject
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func pageFrom(items []model.Repository, after string, limit int, hasMore bool) model.Page[model.Repository] {
	next := ""
	if len(items) == limit && hasMore {
		next = items[len(items)-1].ID
	}
	return model.Page[model.Repository]{Items: items, NextCursor: next}
}
