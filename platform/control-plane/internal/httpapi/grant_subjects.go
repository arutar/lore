package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

type grantSubject struct {
	Type      model.SubjectType `json:"type"`
	ID        string            `json:"id"`
	Value     string            `json:"value"`
	Label     string            `json:"label"`
	Detail    string            `json:"detail"`
	Automated bool              `json:"automated"`
}

func (s *Server) grantSubjects(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.manageableRepository(w, r); !ok {
		return
	}
	subjects, err := s.subjectList(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "directory_unavailable", "Couldn't load users and groups")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": subjects})
}

func (s *Server) subjectList(ctx context.Context) ([]grantSubject, error) {
	users, err := s.allUsers(ctx)
	if err != nil {
		return nil, err
	}
	subjects := make([]grantSubject, 0, len(users))
	groupIDs := make(map[string]struct{})
	for _, user := range users {
		label := user.DisplayName
		if label == "" {
			label = user.Username
		}
		automated := strings.HasPrefix(user.Username, "service-account-")
		if automated {
			label = "Lore service (automation)"
		}
		subjects = append(subjects, grantSubject{
			Type: model.SubjectUser, ID: user.ID, Value: user.Username, Label: label,
			Detail: user.Email, Automated: automated,
		})
		for _, group := range user.Groups {
			groupIDs[group] = struct{}{}
		}
	}
	groups := make(map[string]grantSubject)
	if s.config.Directory != nil {
		directoryGroups, err := s.config.Directory.ListGroups(ctx)
		if err != nil {
			return nil, err
		}
		for _, group := range directoryGroups {
			groups[group.ID] = grantSubject{Type: model.SubjectGroup, ID: group.ID, Value: group.Name, Label: group.Name, Detail: "Active Directory"}
		}
	}
	for id := range groupIDs {
		if _, ok := groups[id]; !ok {
			groups[id] = grantSubject{Type: model.SubjectGroup, ID: id, Value: id, Label: "Group " + shortIdentity(id), Detail: "Local or unavailable directory group"}
		}
	}
	for _, group := range groups {
		subjects = append(subjects, group)
	}
	sort.Slice(subjects, func(i, j int) bool {
		if subjects[i].Type != subjects[j].Type {
			return subjects[i].Type == model.SubjectUser
		}
		if subjects[i].Label != subjects[j].Label {
			return strings.ToLower(subjects[i].Label) < strings.ToLower(subjects[j].Label)
		}
		return subjects[i].ID < subjects[j].ID
	})
	return subjects, nil
}

func (s *Server) validGrantSubject(ctx context.Context, grant model.Grant) (bool, error) {
	switch grant.SubjectType {
	case model.SubjectAnonymous, model.SubjectAuthenticated:
		return true, nil
	case model.SubjectUser:
		_, err := s.store.GetUser(ctx, grant.Subject)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return err == nil, err
	case model.SubjectGroup:
		subjects, err := s.subjectList(ctx)
		if err != nil {
			return false, err
		}
		for _, subject := range subjects {
			if subject.Type == model.SubjectGroup && subject.ID == grant.Subject {
				return true, nil
			}
		}
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
