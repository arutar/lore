package store

import (
	"context"
	"sort"
	"strconv"
	"strings"
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
	access       map[string]model.AccessEntry
}

func NewMemory() *Memory {
	return &Memory{
		users:        make(map[string]model.User),
		repositories: make(map[string]model.Repository),
		grants:       make(map[string]map[string]model.Grant),
		sessions:     make(map[string]model.Session),
		access:       make(map[string]model.AccessEntry),
	}
}

func (m *Memory) Close() error { return nil }

func (m *Memory) Catalog(_ context.Context, user *model.User, query, after string, limit int) (model.Page[model.Repository], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	admin := user != nil && user.Role == model.RoleAdmin && user.Active()
	return m.repositoryPageLocked(query, after, limit, func(id string) bool {
		return admin || policy.CanRead(user, m.grantListLocked(id))
	}), nil
}

func (m *Memory) ListRepositories(_ context.Context, query, after string, limit int) (model.Page[model.Repository], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.repositoryPageLocked(query, after, limit, func(string) bool { return true }), nil
}

func (m *Memory) repositoryPageLocked(query, after string, limit int, visible func(id string) bool) model.Page[model.Repository] {
	items := make([]model.Repository, 0, limit+1)
	for _, id := range m.sortedRepositoryIDs() {
		repository := m.repositories[id]
		if id <= after || !visible(id) || !containsFold(query, repository.Name, repository.Description) {
			continue
		}
		repository.Public = m.publicLocked(id)
		items = append(items, repository)
		if len(items) > limit {
			break
		}
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	return model.Page[model.Repository]{Items: items, NextCursor: next}
}

func (m *Memory) publicLocked(id string) bool {
	for _, grant := range m.grantListLocked(id) {
		if grant.SubjectType == model.SubjectAnonymous && grant.Permission == model.PermissionRead {
			return true
		}
	}
	return false
}

func (m *Memory) GetRepositoryByName(_ context.Context, name string) (model.Repository, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, repository := range m.repositories {
		if repository.Name == name {
			repository.Public = m.publicLocked(id)
			return repository, nil
		}
	}
	return model.Repository{}, ErrNotFound
}

// containsFold reports whether any value contains query, ignoring case; an empty query matches.
func containsFold(query string, values ...string) bool {
	if query == "" {
		return true
	}
	query = strings.ToLower(query)
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
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
	// Visibility lives in the grants, as in PostgreSQL; the stored record never carries it.
	public := repository.Public
	repository.Public = false
	for id, existing := range m.repositories {
		if id == repository.ID && existing.Name == repository.Name {
			repository.CreatedAt = existing.CreatedAt
			repository.UpdatedAt = time.Now().UTC()
			m.repositories[id] = repository
			owner := model.Grant{RepositoryID: id, SubjectType: model.SubjectUser, Subject: repository.OwnerID, Permission: model.PermissionOwner}
			m.grants[id][grantKey(owner)] = owner
			m.appendAuditLocked(actorID, "repository.create", "repository", id, existing.OwnerID, repository.OwnerID)
			m.setPublicLocked(actorID, id, public)
			repository.Public = public
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
	if public {
		m.setPublicLocked(actorID, repository.ID, true)
		repository.Public = true
	}
	return repository, nil
}

func (m *Memory) RepositoryNameTaken(_ context.Context, name, exceptID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, repository := range m.repositories {
		if repository.Name == name && id != exceptID {
			return true, nil
		}
	}
	return false, nil
}

// setPublicLocked sets or removes anonymous read and audits only a real change.
func (m *Memory) setPublicLocked(actorID, repositoryID string, public bool) {
	grant := model.Grant{RepositoryID: repositoryID, SubjectType: model.SubjectAnonymous, Permission: model.PermissionRead}
	key := grantKey(grant)
	existing, ok := m.grants[repositoryID][key]
	switch {
	case public && (!ok || existing.Permission != model.PermissionRead):
		m.grants[repositoryID][key] = grant
		m.appendGrantAuditLocked(actorID, "grant.set", grant, string(existing.Permission), string(grant.Permission))
	case !public && ok:
		delete(m.grants[repositoryID], key)
		m.appendGrantAuditLocked(actorID, "grant.remove", grant, string(existing.Permission), "")
	}
}

func (m *Memory) UpdateRepository(_ context.Context, actorID string, repository model.Repository, public *bool) (model.Repository, error) {
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
	before := existing
	existing.Name = repository.Name
	existing.Description = repository.Description
	existing.UpdatedAt = time.Now().UTC()
	m.repositories[repository.ID] = existing
	if existing.Name != before.Name || existing.Description != before.Description {
		m.appendAuditLocked(actorID, "repository.update", "repository", repository.ID, before.Name, existing.Name)
	}
	if public != nil {
		m.setPublicLocked(actorID, repository.ID, *public)
	}
	for _, grant := range m.grantListLocked(repository.ID) {
		if grant.SubjectType == model.SubjectAnonymous && grant.Permission == model.PermissionRead {
			existing.Public = true
		}
	}
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

func (m *Memory) ListUsers(_ context.Context, query, after string, limit int) (model.Page[model.User], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.users))
	for id := range m.users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]model.User, 0, limit+1)
	for _, id := range ids {
		user := m.users[id]
		if id <= after || user.Deleted || !containsFold(query, user.Username, user.DisplayName, user.Email) {
			continue
		}
		items = append(items, cloneUser(user))
		if len(items) > limit {
			break
		}
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
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
	for id, former := range m.users {
		if id != user.ID && former.Username == user.Username {
			name := former.Username
			former.Username = name + "~" + id
			former.UpdatedAt = now
			m.users[id] = former
			m.appendAuditLocked("", "user.supersede", "user", id, name, user.ID)
		}
	}
	existing, ok := m.users[user.ID]
	if ok {
		user.Role = existing.Role
		user.Disabled = existing.Disabled
		user.CreatedAt = existing.CreatedAt
	} else {
		if user.Role == "" {
			user.Role = model.RoleUser
		}
		user.Disabled = false
		user.CreatedAt = now
	}
	user.UpdatedAt = now
	user.Groups = append([]string{}, user.Groups...)
	m.users[user.ID] = user
	return cloneUser(user), nil
}

func (m *Memory) UpdateDirectoryState(_ context.Context, id string, state DirectoryState) (model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[id]
	if !ok {
		return model.User{}, ErrNotFound
	}
	if user.DirectoryDisabled != state.Disabled {
		action := "user.directory_enable"
		if state.Disabled {
			action = "user.directory_disable"
		}
		m.appendAuditLocked("", action, "user", id, "", "")
	}
	syncedAt := state.SyncedAt
	user.DirectoryID = state.DirectoryID
	user.Groups = append([]string{}, state.Groups...)
	user.DirectoryDisabled = state.Disabled
	user.GroupsSyncedAt = &syncedAt
	m.users[id] = user
	return cloneUser(user), nil
}

func (m *Memory) GroupHasMembers(_ context.Context, group string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, user := range m.users {
		for _, member := range user.Groups {
			if member == group {
				return true, nil
			}
		}
	}
	return false, nil
}

func (m *Memory) HasActiveAdmin(context.Context) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, user := range m.users {
		if user.Role == model.RoleAdmin && user.Active() {
			return true, nil
		}
	}
	return false, nil
}

func (m *Memory) ApplyDirectoryAccess(_ context.Context, id string, denied bool, role model.Role) (model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[id]
	if !ok {
		return model.User{}, ErrNotFound
	}
	if user.AccessDenied != denied {
		action := "user.access_allow"
		if denied {
			action = "user.access_deny"
		}
		m.appendAuditLocked("", action, "user", id, "", "")
	}
	if user.Role != role {
		m.appendAuditLocked("", "user.role_from_directory", "user", id, string(user.Role), string(role))
	}
	user.AccessDenied = denied
	user.Role = role
	m.users[id] = user
	return cloneUser(user), nil
}

func accessKey(subjectType model.SubjectType, subject string) string {
	return string(subjectType) + "\x00" + strings.ToLower(subject)
}

func (m *Memory) ListAccess(context.Context) ([]model.AccessEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entries := make([]model.AccessEntry, 0, len(m.access))
	for _, entry := range m.access {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].SubjectType != entries[j].SubjectType {
			return entries[i].SubjectType == model.SubjectGroup
		}
		if a, b := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name); a != b {
			return a < b
		}
		return entries[i].Subject < entries[j].Subject
	})
	return entries, nil
}

func (m *Memory) AddAccess(_ context.Context, actorID string, entry model.AccessEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := accessKey(entry.SubjectType, entry.Subject)
	existing, ok := m.access[key]
	if ok && existing.Name == entry.Name && existing.Detail == entry.Detail {
		return nil
	}
	if ok {
		entry.CreatedAt, entry.Role, entry.Disabled = existing.CreatedAt, existing.Role, existing.Disabled
	} else {
		entry.CreatedAt = time.Now().UTC()
		if entry.Role == "" {
			entry.Role = model.RoleUser
		}
	}
	m.access[key] = entry
	m.appendAccessAuditLocked(actorID, "access.add", entry.SubjectType, entry.Subject, "", entry.Name)
	return nil
}

func (m *Memory) UpdateAccess(_ context.Context, actorID string, subjectType model.SubjectType, subject string, role model.Role, disabled bool) (model.AccessEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := accessKey(subjectType, subject)
	entry, ok := m.access[key]
	if !ok {
		return model.AccessEntry{}, ErrNotFound
	}
	if entry.Role == role && entry.Disabled == disabled {
		return entry, nil
	}
	before := entry.Role + model.Role(":"+strconv.FormatBool(!entry.Disabled))
	entry.Role, entry.Disabled = role, disabled
	m.access[key] = entry
	m.appendAccessAuditLocked(actorID, "access.update", subjectType, entry.Subject, string(before), string(role)+":"+strconv.FormatBool(!disabled))
	return entry, nil
}

func (m *Memory) RemoveAccess(_ context.Context, actorID string, subjectType model.SubjectType, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := accessKey(subjectType, subject)
	entry, ok := m.access[key]
	if !ok {
		return ErrNotFound
	}
	delete(m.access, key)
	m.appendAccessAuditLocked(actorID, "access.remove", subjectType, entry.Subject, entry.Name, "")
	return nil
}

func (m *Memory) DeleteUser(_ context.Context, actorID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[id]
	if !ok || user.Deleted {
		return ErrNotFound
	}
	name := user.Username
	user.Username, user.Deleted, user.Disabled, user.UpdatedAt = name+"~"+id, true, true, time.Now().UTC()
	m.users[id] = user
	for _, grants := range m.grants {
		for key, grant := range grants {
			if grant.SubjectType == model.SubjectUser && grant.Subject == id {
				delete(grants, key)
			}
		}
	}
	for key, session := range m.sessions {
		if session.UserID == id {
			delete(m.sessions, key)
		}
	}
	m.appendAuditLocked(actorID, "user.delete", "user", id, name, "")
	return nil
}

func (m *Memory) UpdateUser(_ context.Context, actorID, id string, role model.Role, disabled bool) (model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[id]
	if !ok {
		return model.User{}, ErrNotFound
	}
	before := user
	user.Role = role
	user.Disabled = disabled
	user.UpdatedAt = time.Now().UTC()
	m.users[id] = user
	if before.Role != role {
		m.appendAuditLocked(actorID, "user.update", "user", id, string(before.Role), string(role))
	}
	if before.Disabled != disabled {
		action := "user.enable"
		if disabled {
			action = "user.disable"
		}
		m.appendAuditLocked(actorID, action, "user", id, "", "")
	}
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
	if before != string(grant.Permission) {
		m.appendGrantAuditLocked(actorID, "grant.set", grant, before, string(grant.Permission))
	}
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
	m.appendGrantAuditLocked(actorID, "grant.remove", grant, string(existing.Permission), "")
	return nil
}

func (m *Memory) GetSettings(_ context.Context) (model.Settings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	settings := m.settings
	if settings.Theme == "" {
		settings.Theme = model.DefaultTheme
	}
	if settings.Language == "" {
		settings.Language = model.DefaultLanguage
	}
	return settings, nil
}

func (m *Memory) UpdateSettings(_ context.Context, actorID string, settings model.Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.settings
	if before.Theme == "" {
		before.Theme = model.DefaultTheme
	}
	if before.Language == "" {
		before.Language = model.DefaultLanguage
	}
	m.settings = settings
	// The same "registration:theme:language" values PostgreSQL records.
	m.appendAuditLocked(actorID, "settings.update", "settings", "global",
		boolString(before.RegistrationEnabled)+":"+before.Theme+":"+before.Language,
		boolString(settings.RegistrationEnabled)+":"+settings.Theme+":"+settings.Language)
	return nil
}

func (m *Memory) ListAudit(_ context.Context, before int64, limit int) (model.Page[model.AuditEvent], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]model.AuditEvent, 0, limit)
	next := ""
	for index := len(m.audit) - 1; index >= 0; index-- {
		event := m.audit[index]
		if before != 0 && event.ID >= before {
			continue
		}
		if len(items) == limit {
			next = strconv.FormatInt(items[len(items)-1].ID, 10)
			break
		}
		items = append(items, event)
	}
	return model.Page[model.AuditEvent]{Items: items, NextCursor: next}, nil
}

func (m *Memory) CreateSession(_ context.Context, session model.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	for id, existing := range m.sessions {
		if existing.ExpiresAt.Before(now) {
			delete(m.sessions, id)
		}
	}
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

func (m *Memory) appendAccessAuditLocked(actorID, action string, subjectType model.SubjectType, subject, before, after string) {
	m.appendAuditLocked(actorID, action, "access", subject, before, after)
	m.audit[len(m.audit)-1].SubjectType = subjectType
	m.audit[len(m.audit)-1].Subject = subject
}

func (m *Memory) appendGrantAuditLocked(actorID, action string, grant model.Grant, before, after string) {
	m.appendAuditLocked(actorID, action, "repository", grant.RepositoryID, before, after)
	m.audit[len(m.audit)-1].SubjectType = grant.SubjectType
	m.audit[len(m.audit)-1].Subject = grant.Subject
}

func (m *Memory) RecordAudit(_ context.Context, event model.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	event.ID = int64(len(m.audit) + 1)
	event.CreatedAt = time.Now().UTC()
	m.audit = append(m.audit, event)
	return nil
}

func cloneUser(user model.User) model.User {
	user.Groups = append([]string{}, user.Groups...)
	if user.GroupsSyncedAt != nil {
		syncedAt := *user.GroupsSyncedAt
		user.GroupsSyncedAt = &syncedAt
	}
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
