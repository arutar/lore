package httpapi

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/policy"
)

var repositorySegment = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
var repositoryID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type repositoryInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Public      bool   `json:"public"`
}

type userInput struct {
	Role     model.Role `json:"role"`
	Disabled bool       `json:"disabled"`
}

type settingsInput struct {
	RegistrationEnabled bool   `json:"registrationEnabled"`
	Theme               string `json:"theme"`
}

func (s *Server) publicConfig(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"registrationEnabled": settings.RegistrationEnabled,
		"oidcEnabled":         s.auth.provider != nil,
		"devAuthEnabled":      s.config.DevAuth,
		"externalUrl":         s.config.ExternalURL,
		"theme":               settings.Theme,
	})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	user, session := currentUser(r.Context())
	if user == nil || session == nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user":          user,
		"csrfToken":     session.CSRFToken,
	})
}

func (s *Server) devLogin(w http.ResponseWriter, r *http.Request) {
	if !s.config.DevAuth {
		writeError(w, http.StatusNotFound, "not_found", "Resource not found")
		return
	}
	s.auth.devLogin(w, r, s.config.DevUser)
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_page_size", "Page size must be between 1 and 100")
		return
	}
	user, _ := currentUser(r.Context())
	page, err := s.store.Catalog(r.Context(), user, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) repository(w http.ResponseWriter, r *http.Request) {
	repository, _, ok := s.readableRepository(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, repository)
}

func (s *Server) grants(w http.ResponseWriter, r *http.Request) {
	_, grants, ok := s.manageableRepository(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": grants})
}

func (s *Server) setGrant(w http.ResponseWriter, r *http.Request) {
	repository, _, ok := s.manageableRepository(w, r)
	if !ok {
		return
	}
	var grant model.Grant
	if !decodeJSON(w, r, &grant) {
		return
	}
	grant.RepositoryID = repository.ID
	grant.Subject = strings.TrimSpace(grant.Subject)
	if err := policy.ValidateGrant(grant); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_grant", "Grant is invalid")
		return
	}
	actor, _ := currentUser(r.Context())
	if err := s.store.SetGrant(r.Context(), actor.ID, grant); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (s *Server) removeGrant(w http.ResponseWriter, r *http.Request) {
	repository, _, ok := s.manageableRepository(w, r)
	if !ok {
		return
	}
	var grant model.Grant
	if !decodeJSON(w, r, &grant) {
		return
	}
	grant.RepositoryID = repository.ID
	actor, _ := currentUser(r.Context())
	if err := s.store.RemoveGrant(r.Context(), actor.ID, grant); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_page_size", "Page size must be between 1 and 100")
		return
	}
	page, err := s.store.ListUsers(r.Context(), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	actor, _ := currentUser(r.Context())
	id := chi.URLParam(r, "id")
	var input userInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Role != model.RoleAdmin && input.Role != model.RoleUser {
		writeError(w, http.StatusBadRequest, "invalid_role", "Role must be admin or user")
		return
	}
	if actor.ID == id && (input.Disabled || input.Role != model.RoleAdmin) {
		writeError(w, http.StatusConflict, "self_lockout", "You can't disable or demote your own administrator account")
		return
	}
	user, err := s.store.UpdateUser(r.Context(), actor.ID, id, input.Role, input.Disabled)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) repositories(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_page_size", "Page size must be between 1 and 100")
		return
	}
	page, err := s.store.ListRepositories(r.Context(), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) createRepository(w http.ResponseWriter, r *http.Request) {
	actor, _ := currentUser(r.Context())
	var input repositoryInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validRepositoryName(input.Name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "Repository name contains invalid segments")
		return
	}
	id := strings.ToLower(input.ID)
	if id != "" && !repositoryID.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "Repository ID must be a UUID")
		return
	}
	if id == "" {
		var err error
		id, err = newRepositoryID()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "id_generation_failed", "Couldn't generate a repository ID")
			return
		}
	}
	desired := model.Repository{ID: id, Name: input.Name, Description: input.Description, OwnerID: actor.ID}
	if creator := s.config.Repositories; creator != nil {
		if err := creator.CreateRepository(r.Context(), desired); err != nil {
			switch status.Code(err) {
			case codes.AlreadyExists:
				writeError(w, http.StatusConflict, "conflict", "Repository already exists")
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, "invalid_repository", "Lore rejected the repository")
			default:
				writeError(w, http.StatusServiceUnavailable, "lore_unavailable", "Lore couldn't create the repository")
			}
			return
		}
	}
	repository, err := s.store.CreateRepository(r.Context(), actor.ID, desired)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if input.Public {
		grant := model.Grant{RepositoryID: id, SubjectType: model.SubjectAnonymous, Permission: model.PermissionRead}
		if err := s.store.SetGrant(r.Context(), actor.ID, grant); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, repository)
}

func (s *Server) updateRepository(w http.ResponseWriter, r *http.Request) {
	actor, _ := currentUser(r.Context())
	existing, err := s.store.GetRepository(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var input repositoryInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validRepositoryName(input.Name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "Repository name contains invalid segments")
		return
	}
	existing.Name = input.Name
	existing.Description = input.Description
	repository, err := s.store.UpdateRepository(r.Context(), actor.ID, existing)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	grant := model.Grant{RepositoryID: repository.ID, SubjectType: model.SubjectAnonymous, Permission: model.PermissionRead}
	if input.Public {
		err = s.store.SetGrant(r.Context(), actor.ID, grant)
	} else {
		err = s.store.RemoveGrant(r.Context(), actor.ID, grant)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repository)
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var input settingsInput
	if !decodeJSON(w, r, &input) {
		return
	}
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if input.Theme == "" {
		input.Theme = settings.Theme
	}
	if !model.ValidTheme(input.Theme) {
		writeError(w, http.StatusBadRequest, "invalid_theme", "Theme isn't supported")
		return
	}
	actor, _ := currentUser(r.Context())
	registrationChanged := settings.RegistrationEnabled != input.RegistrationEnabled
	previousRegistration := settings.RegistrationEnabled
	if registrationChanged && s.config.RegistrationRealm != nil {
		if err := s.config.RegistrationRealm.SetRegistrationAllowed(r.Context(), input.RegistrationEnabled); err != nil {
			writeError(w, http.StatusBadGateway, "registration_sync_failed", "Couldn't update registration in Keycloak")
			return
		}
	}
	settings.RegistrationEnabled = input.RegistrationEnabled
	settings.Theme = input.Theme
	if err := s.store.UpdateSettings(r.Context(), actor.ID, settings); err != nil {
		if registrationChanged && s.config.RegistrationRealm != nil {
			_ = s.config.RegistrationRealm.SetRegistrationAllowed(r.Context(), previousRegistration)
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_page_size", "Page size must be between 1 and 100")
		return
	}
	var after int64
	if value := r.URL.Query().Get("cursor"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "Audit cursor is invalid")
			return
		}
		after = parsed
	}
	page, err := s.store.ListAudit(r.Context(), after, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) readableRepository(w http.ResponseWriter, r *http.Request) (model.Repository, []model.Grant, bool) {
	repository, err := s.store.GetRepository(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return model.Repository{}, nil, false
	}
	grants, err := s.store.ListGrants(r.Context(), repository.ID)
	if err != nil {
		writeStoreError(w, err)
		return model.Repository{}, nil, false
	}
	user, _ := currentUser(r.Context())
	if !policy.CanRead(user, grants) {
		writeError(w, http.StatusNotFound, "not_found", "Resource not found")
		return model.Repository{}, nil, false
	}
	return repository, grants, true
}

func (s *Server) manageableRepository(w http.ResponseWriter, r *http.Request) (model.Repository, []model.Grant, bool) {
	repository, grants, ok := s.readableRepository(w, r)
	if !ok {
		return model.Repository{}, nil, false
	}
	user, _ := currentUser(r.Context())
	if !policy.CanManage(user, grants) {
		writeError(w, http.StatusForbidden, "not_authorized", "Repository owner access is required")
		return model.Repository{}, nil, false
	}
	return repository, grants, true
}

func validRepositoryName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if !repositorySegment.MatchString(segment) {
			return false
		}
	}
	return true
}

func newRepositoryID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}
