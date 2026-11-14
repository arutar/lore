package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/directory"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

// subjectSearchLimit caps each kind of suggestion: the grant form needs a handful of
// matches, and an owner must not be able to page through the whole user list or directory.
const subjectSearchLimit = 10

type grantSubject struct {
	Type   model.SubjectType `json:"type"`
	ID     string            `json:"id"`
	Value  string            `json:"value"`
	Label  string            `json:"label"`
	Detail string            `json:"detail"`
}

// labeledGrant is a grant with a readable name for its subject.
type labeledGrant struct {
	model.Grant
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// grantSubjects suggests users and groups for the grant form. It answers only a search of
// at least two characters with a few matches, never the full list.
func (s *Server) grantSubjects(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.manageableRepository(w, r); !ok {
		return
	}
	query := searchQuery(r)
	if len([]rune(query)) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []grantSubject{}})
		return
	}
	subjects, err := s.searchSubjects(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusBadGateway, "directory_unavailable", "Couldn't search users and groups")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": subjects})
}

func (s *Server) searchSubjects(ctx context.Context, query string) ([]grantSubject, error) {
	subjects := []grantSubject{}
	page, err := s.store.ListUsers(ctx, query, "", subjectSearchLimit*2)
	if err != nil {
		return nil, err
	}
	for _, user := range page.Items {
		if !user.Active() || user.Source == SourceService || strings.HasPrefix(user.Username, "service-account-") {
			continue
		}
		subjects = append(subjects, userSubject(user))
		if len(subjects) == subjectSearchLimit {
			break
		}
	}
	if s.config.Directory != nil {
		// Only groups on the list of users and groups: whoever isn't on it doesn't exist for Lore.
		entries, err := s.store.ListAccess(ctx)
		if err != nil {
			return nil, err
		}
		lowered := strings.ToLower(query)
		count := 0
		for _, entry := range entries {
			if entry.SubjectType != model.SubjectGroup || entry.Disabled || !strings.Contains(strings.ToLower(entry.Name), lowered) {
				continue
			}
			subjects = append(subjects, groupSubject(directory.Group{ID: entry.Subject, Name: entry.Name}))
			if count++; count == subjectSearchLimit {
				break
			}
		}
	}
	return subjects, nil
}

func userSubject(user model.User) grantSubject {
	label := user.DisplayName
	if label == "" {
		label = user.Username
	}
	detail := user.Username
	if user.Email != "" && user.Email != user.Username {
		detail += " · " + user.Email
	}
	return grantSubject{Type: model.SubjectUser, ID: user.ID, Value: user.Username, Label: label, Detail: detail}
}

func groupSubject(group directory.Group) grantSubject {
	detail := "Active Directory"
	if group.Description != "" {
		detail += " · " + group.Description
	}
	return grantSubject{Type: model.SubjectGroup, ID: group.ID, Value: group.Name, Label: group.Name, Detail: detail}
}

// labelGrants names the subject of every grant, so the access page needn't load any list.
func (s *Server) labelGrants(ctx context.Context, grants []model.Grant) []labeledGrant {
	items := make([]labeledGrant, 0, len(grants))
	for _, grant := range grants {
		item := labeledGrant{Grant: grant}
		switch grant.SubjectType {
		case model.SubjectAnonymous:
			item.Label = "Anyone"
		case model.SubjectAuthenticated:
			item.Label = "Any signed-in user"
		case model.SubjectUser:
			if user, err := s.store.GetUser(ctx, grant.Subject); err == nil {
				subject := userSubject(user)
				item.Label, item.Detail = subject.Label, subject.Detail
			} else {
				item.Label, item.Detail = "Former user", grant.Subject
			}
		case model.SubjectGroup:
			if group, ok := s.groupName(ctx, grant.Subject); ok {
				subject := groupSubject(group)
				item.Label, item.Detail = subject.Label, subject.Detail
			} else if directory.NormalizeGUID(grant.Subject) == "" {
				item.Label, item.Detail = grant.Subject, "Identity provider group"
			} else {
				item.Label, item.Detail = "Group "+shortIdentity(grant.Subject), "Not found in Active Directory"
			}
		}
		items = append(items, item)
	}
	return items
}

// groupCache keeps group names for a few minutes: the access page shows the same groups
// on every visit, and the directory needn't be asked each time.
type groupCache struct {
	mu      sync.Mutex
	entries map[string]groupCacheEntry
}

type groupCacheEntry struct {
	group   directory.Group
	found   bool
	expires time.Time
}

const groupCacheLifetime = 5 * time.Minute

func (s *Server) groupName(ctx context.Context, id string) (directory.Group, bool) {
	if s.config.Directory == nil {
		return directory.Group{}, false
	}
	s.groups.mu.Lock()
	entry, ok := s.groups.entries[id]
	s.groups.mu.Unlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.group, entry.found
	}
	group, err := s.config.Directory.LookupGroup(ctx, id)
	if err != nil && !errors.Is(err, directory.ErrGroupNotFound) {
		return directory.Group{}, false // outage: don't cache, try again next time
	}
	entry = groupCacheEntry{group: group, found: err == nil, expires: time.Now().Add(groupCacheLifetime)}
	s.groups.mu.Lock()
	if s.groups.entries == nil {
		s.groups.entries = map[string]groupCacheEntry{}
	}
	s.groups.entries[id] = entry
	s.groups.mu.Unlock()
	return entry.group, entry.found
}

// resolveSubject accepts what a person types or a file contains: a user ID, username or
// email, or a group objectGUID or exact group name. It returns the stored ID; when nothing
// matches, the subject is returned unchanged and validGrantSubject rejects it.
func (s *Server) resolveSubject(ctx context.Context, grant model.Grant) (string, error) {
	subject := grant.Subject
	switch grant.SubjectType {
	case model.SubjectUser:
		if _, err := s.store.GetUser(ctx, subject); err == nil {
			return subject, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		page, err := s.store.ListUsers(ctx, subject, "", 50)
		if err != nil {
			return "", err
		}
		var match *model.User
		for index, user := range page.Items {
			if strings.EqualFold(user.Username, subject) || (user.Email != "" && strings.EqualFold(user.Email, subject)) {
				if match != nil {
					return subject, nil // ambiguous: refuse rather than guess
				}
				match = &page.Items[index]
			}
		}
		if match != nil {
			return match.ID, nil
		}
	case model.SubjectGroup:
		if id := directory.NormalizeGUID(subject); id != "" {
			return id, nil
		}
		if s.config.Directory == nil {
			return subject, nil
		}
		entries, err := s.store.ListAccess(ctx)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if entry.SubjectType == model.SubjectGroup && strings.EqualFold(entry.Name, subject) {
				return entry.Subject, nil
			}
		}
	}
	return subject, nil
}

func (s *Server) validGrantSubject(ctx context.Context, grant model.Grant) (bool, error) {
	switch grant.SubjectType {
	case model.SubjectAnonymous, model.SubjectAuthenticated:
		return true, nil
	case model.SubjectUser:
		// Only someone who may use Lore: a local account or an admitted directory account.
		user, err := s.store.GetUser(ctx, grant.Subject)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return err == nil && user.Active(), err
	case model.SubjectGroup:
		if s.config.Directory != nil {
			// Active Directory groups only from the list of users and groups.
			entries, err := s.store.ListAccess(ctx)
			if err != nil {
				return false, err
			}
			for _, entry := range entries {
				if entry.SubjectType == model.SubjectGroup && strings.EqualFold(entry.Subject, grant.Subject) {
					return true, nil
				}
			}
			return false, nil
		}
		// Without a directory connection: an identity provider group some account is in.
		return s.store.GroupHasMembers(ctx, grant.Subject)
	}
	return false, nil
}

func shortIdentity(id string) string {
	compact := strings.ReplaceAll(id, "-", "")
	if len(compact) > 8 {
		return compact[:8]
	}
	return compact
}

func writeInvalidSubject(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_subject", "Selected user or group doesn't exist")
}
