package httpapi

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/arutar/lore/platform/control-plane/internal/directory"
	"github.com/arutar/lore/platform/control-plane/internal/model"
)

// The Active Directory access list: who from the directory may sign in at all. An account is
// admitted if it, or a group it belongs to (nested groups included), is on the list. Local
// accounts don't need an entry. Repository grants still decide what an admitted account sees.

func (s *Server) accessList(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListAccess(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries, "directory": s.config.Directory != nil})
}

// accessSubjects suggests directory accounts and groups for the access list, a handful per
// search of at least two characters.
func (s *Server) accessSubjects(w http.ResponseWriter, r *http.Request) {
	if s.config.Directory == nil {
		writeError(w, http.StatusServiceUnavailable, "ad_unavailable", "Active Directory isn't configured")
		return
	}
	query := searchQuery(r)
	if len([]rune(query)) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []grantSubject{}})
		return
	}
	subjects := []grantSubject{}
	groups, err := s.config.Directory.SearchGroups(r.Context(), query, subjectSearchLimit)
	if err != nil {
		log.Printf("AD group search failed: %v", err)
		writeError(w, http.StatusBadGateway, "directory_unavailable", "Couldn't search Active Directory")
		return
	}
	for _, group := range groups {
		subjects = append(subjects, groupSubject(group))
	}
	users, err := s.config.Directory.SearchUsers(r.Context(), query)
	if err != nil {
		log.Printf("AD user search failed: %v", err)
		writeError(w, http.StatusBadGateway, "directory_unavailable", "Couldn't search Active Directory")
		return
	}
	for index, user := range users {
		if index == subjectSearchLimit {
			break
		}
		subjects = append(subjects, directoryUserSubject(user))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": subjects})
}

func directoryUserSubject(user directory.User) grantSubject {
	label := user.DisplayName
	if label == "" {
		label = user.Username
	}
	detail := user.Username
	if user.Email != "" && !strings.EqualFold(user.Email, user.Username) {
		detail += " · " + user.Email
	}
	return grantSubject{Type: model.SubjectUser, ID: user.ID, Value: user.Username, Label: label, Detail: detail}
}

// addAccess admits a directory account or group. The name is read from the directory by
// objectGUID, never taken from the request, so the list can't be given a misleading label.
func (s *Server) addAccess(w http.ResponseWriter, r *http.Request) {
	if s.config.Directory == nil {
		writeError(w, http.StatusServiceUnavailable, "ad_unavailable", "Active Directory isn't configured")
		return
	}
	var input struct {
		SubjectType model.SubjectType `json:"subjectType"`
		Subject     string            `json:"subject"`
		// Optional: a personal entry for a group member is often made to give a role or to
		// shut that one person out.
		Role     model.Role `json:"role"`
		Disabled bool       `json:"disabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	id := directory.NormalizeGUID(input.Subject)
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_subject", "An Active Directory object ID is required")
		return
	}
	entry := model.AccessEntry{SubjectType: input.SubjectType, Subject: id}
	switch input.SubjectType {
	case model.SubjectGroup:
		group, err := s.config.Directory.LookupGroup(r.Context(), id)
		if err != nil {
			s.directoryLookupError(w, err, "group")
			return
		}
		entry.Name, entry.Detail = group.Name, group.DistinguishedName
	case model.SubjectUser:
		account, err := s.config.Directory.LookupAccount(r.Context(), id)
		if err != nil {
			s.directoryLookupError(w, err, "account")
			return
		}
		subject := directoryUserSubject(account.User)
		entry.Name, entry.Detail = subject.Label, subject.Detail
	default:
		writeError(w, http.StatusBadRequest, "invalid_subject_type", "Access is given to a directory account or group")
		return
	}
	if input.Role == "" {
		input.Role = model.RoleUser
	}
	if input.Role != model.RoleUser && input.Role != model.RoleAdmin {
		writeError(w, http.StatusBadRequest, "invalid_role", "Role must be admin or user")
		return
	}
	user, _ := currentUser(r.Context())
	entry.Role, entry.Disabled = input.Role, input.Disabled
	if !s.keepsActorAdmin(w, r, user, func(entries []model.AccessEntry) []model.AccessEntry { return append(entries, entry) }) {
		return
	}
	if err := s.store.AddAccess(r.Context(), user.ID, entry); err != nil {
		writeStoreError(w, err)
		return
	}
	if _, err := s.store.UpdateAccess(r.Context(), user.ID, entry.SubjectType, entry.Subject, input.Role, input.Disabled); err != nil {
		writeStoreError(w, err)
		return
	}
	s.recheckAccess(r)
	s.accessList(w, r)
}

// updateAccess changes an entry's role or switches it off without removing it.
func (s *Server) updateAccess(w http.ResponseWriter, r *http.Request) {
	subjectType := model.SubjectType(chi.URLParam(r, "type"))
	if subjectType != model.SubjectUser && subjectType != model.SubjectGroup {
		writeError(w, http.StatusBadRequest, "invalid_subject_type", "Access is given to a directory account or group")
		return
	}
	subject := directory.NormalizeGUID(chi.URLParam(r, "subject"))
	var input struct {
		Role     model.Role `json:"role"`
		Disabled bool       `json:"disabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Role != model.RoleUser && input.Role != model.RoleAdmin {
		writeError(w, http.StatusBadRequest, "invalid_role", "Role must be admin or user")
		return
	}
	user, _ := currentUser(r.Context())
	if !s.keepsActorAdmin(w, r, user, func(entries []model.AccessEntry) []model.AccessEntry {
		for index := range entries {
			if entries[index].SubjectType == subjectType && strings.EqualFold(entries[index].Subject, subject) {
				entries[index].Role, entries[index].Disabled = input.Role, input.Disabled
			}
		}
		return entries
	}) {
		return
	}
	entry, err := s.store.UpdateAccess(r.Context(), user.ID, subjectType, subject, input.Role, input.Disabled)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.recheckAccess(r)
	writeJSON(w, http.StatusOK, entry)
}

// keepsActorAdmin refuses a change to the list that would take away the acting administrator's
// own access or role: an administrator who signs in from Active Directory mustn't lock
// themselves out by switching off or demoting the group they're an administrator through.
func (s *Server) keepsActorAdmin(w http.ResponseWriter, r *http.Request, actor *model.User, change func([]model.AccessEntry) []model.AccessEntry) bool {
	if actor == nil || !actor.Federated() {
		return true
	}
	entries, err := s.store.ListAccess(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return false
	}
	admitted, role := model.ResolveAccess(change(entries), actor.DirectoryID, actor.Groups)
	if !admitted || role != model.RoleAdmin {
		writeError(w, http.StatusConflict, "self_lockout", "This change would take away your own administrator access")
		return false
	}
	return true
}

func (s *Server) removeAccess(w http.ResponseWriter, r *http.Request) {
	subjectType := model.SubjectType(chi.URLParam(r, "type"))
	if subjectType != model.SubjectUser && subjectType != model.SubjectGroup {
		writeError(w, http.StatusBadRequest, "invalid_subject_type", "Access is given to a directory account or group")
		return
	}
	user, _ := currentUser(r.Context())
	subject := directory.NormalizeGUID(chi.URLParam(r, "subject"))
	if !s.keepsActorAdmin(w, r, user, func(entries []model.AccessEntry) []model.AccessEntry {
		kept := entries[:0]
		for _, entry := range entries {
			if !(entry.SubjectType == subjectType && strings.EqualFold(entry.Subject, subject)) {
				kept = append(kept, entry)
			}
		}
		return kept
	}) {
		return
	}
	if err := s.store.RemoveAccess(r.Context(), user.ID, subjectType, subject); err != nil {
		writeStoreError(w, err)
		return
	}
	s.recheckAccess(r)
	w.WriteHeader(http.StatusNoContent)
}

// recheckAccess applies a changed list to the stored accounts at once. A failure only delays
// it to each account's next directory refresh, so it's logged rather than reported.
func (s *Server) recheckAccess(r *http.Request) {
	if s.auth == nil {
		return
	}
	if err := s.auth.RecheckAccess(r.Context()); err != nil {
		log.Printf("Applying the access list failed: %v", err)
	}
}

func (s *Server) directoryLookupError(w http.ResponseWriter, err error, kind string) {
	if errors.Is(err, directory.ErrUserNotFound) || errors.Is(err, directory.ErrGroupNotFound) {
		writeError(w, http.StatusNotFound, kind+"_not_found", "The Active Directory "+kind+" wasn't found")
		return
	}
	log.Printf("AD %s lookup failed: %v", kind, err)
	writeError(w, http.StatusBadGateway, "directory_unavailable", "Couldn't read Active Directory")
}

// memberGroup is a group on the list with the accounts that came in through it.
type memberGroup struct {
	model.AccessEntry
	Members []model.User `json:"members"`
}

// memberAccount is an account added personally, with its stored record once it has signed in.
type memberAccount struct {
	model.AccessEntry
	User *model.User `json:"user,omitempty"`
}

// members lists everyone who may use Lore in one place: local accounts, directory accounts
// added personally and directory groups with the members who have signed in. Whoever isn't on
// the list isn't shown.
func (s *Server) members(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entries, err := s.store.ListAccess(ctx)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var users []model.User
	for cursor := ""; ; {
		page, err := s.store.ListUsers(ctx, "", cursor, 500)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		users = append(users, page.Items...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	local := []model.User{}
	byDirectoryID := map[string]model.User{}
	for _, user := range users {
		switch {
		case user.Source == SourceService || strings.HasPrefix(user.Username, "service-account-"):
		case user.Federated():
			byDirectoryID[strings.ToLower(user.DirectoryID)] = user
		default:
			local = append(local, user)
		}
	}
	groups := []memberGroup{}
	accounts := []memberAccount{}
	for _, entry := range entries {
		switch entry.SubjectType {
		case model.SubjectGroup:
			group := memberGroup{AccessEntry: entry, Members: []model.User{}}
			for _, user := range users {
				if !user.Federated() {
					continue
				}
				for _, id := range user.Groups {
					if strings.EqualFold(id, entry.Subject) {
						group.Members = append(group.Members, user)
						break
					}
				}
			}
			groups = append(groups, group)
		case model.SubjectUser:
			account := memberAccount{AccessEntry: entry}
			if user, ok := byDirectoryID[strings.ToLower(entry.Subject)]; ok {
				account.User = &user
			}
			accounts = append(accounts, account)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"local": local, "accounts": accounts, "groups": groups, "directory": s.config.Directory != nil,
	})
}

