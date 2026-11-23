package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/arutar/lore/platform/control-plane/internal/directory"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/policy"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

// mcpTokenPrefix starts every MCP token, so a leaked one is recognisable, also to secret
// scanners, and can't be mistaken for any other credential.
const mcpTokenPrefix = "lore_mcp_"

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// newMCPSecret returns a token: the prefix and 43 base62 characters, 256 bits of randomness.
func newMCPSecret() (string, error) {
	var builder strings.Builder
	builder.WriteString(mcpTokenPrefix)
	limit := big.NewInt(int64(len(base62)))
	for range 43 {
		index, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		builder.WriteByte(base62[index.Int64()])
	}
	return builder.String(), nil
}

func secretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// mcpAllowed reports whether the account may use MCP now, by its own switch or the access list.
func (s *Server) mcpAllowed(r *http.Request, user model.User) (bool, error) {
	if !user.Federated() {
		return model.MCPAllowed(user, nil), nil
	}
	entries, err := s.store.ListAccess(r.Context())
	if err != nil {
		return false, err
	}
	return model.MCPAllowed(user, entries), nil
}

// signedInForMCP returns the signed-in account when MCP is enabled on this server and allowed
// for it; otherwise it answers the request itself.
func (s *Server) signedInForMCP(w http.ResponseWriter, r *http.Request) (*model.User, bool) {
	if s.config.MCP.Mode == MCPOff {
		writeError(w, http.StatusNotFound, "not_found", "Resource not found")
		return nil, false
	}
	user, _ := currentUser(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not_authenticated", "Authentication is required")
		return nil, false
	}
	allowed, err := s.mcpAllowed(r, *user)
	if err != nil {
		writeStoreError(w, err)
		return nil, false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "mcp_disabled", "An administrator has turned MCP off for your account")
		return nil, false
	}
	return user, true
}

func (s *Server) mcpTokens(w http.ResponseWriter, r *http.Request) {
	user, ok := s.signedInForMCP(w, r)
	if !ok {
		return
	}
	tokens, err := s.store.ListMCPTokens(r.Context(), user.ID)
	if err == nil {
		err = s.describeTokens(r, tokens)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tokens, "url": s.mcpURL()})
}

func (s *Server) mcpURL() string {
	return strings.TrimRight(s.config.ExternalURL, "/") + "/mcp"
}

// createMCPToken issues a token for the signed-in account. The secret is in this response only.
func (s *Server) createMCPToken(w http.ResponseWriter, r *http.Request) {
	user, ok := s.signedInForMCP(w, r)
	if !ok {
		return
	}
	var input struct {
		Name string `json:"name"`
		// Repositories limits the token; empty means every repository the account may read.
		Repositories []string `json:"repositories"`
		// ExpiresInDays: 0 makes a token that doesn't expire.
		ExpiresInDays int `json:"expiresInDays"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_name", "Give the token a name of up to 80 characters")
		return
	}
	if input.ExpiresInDays < 0 || input.ExpiresInDays > 3650 {
		writeError(w, http.StatusBadRequest, "invalid_expiry", "Expiry must be between 0 (never) and 3650 days")
		return
	}
	if len(input.Repositories) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_repositories", "A token can name at most 100 repositories")
		return
	}
	// A token may only name repositories its owner can read now; it never reaches further later
	// either, since every call checks the owner's current access.
	repositories := []string{}
	for _, id := range input.Repositories {
		repository, err := s.store.GetRepository(r.Context(), id)
		if err == nil {
			var grants []model.Grant
			if grants, err = s.store.ListGrants(r.Context(), repository.ID); err == nil && !policy.CanRead(user, grants) {
				err = store.ErrNotFound
			}
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_repositories", "A repository wasn't found")
			return
		}
		repositories = append(repositories, repository.ID)
	}
	secret, err := newMCPSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Couldn't create a token")
		return
	}
	id, err := newRepositoryID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Couldn't create a token")
		return
	}
	now := time.Now().UTC()
	token := model.MCPToken{ID: id, UserID: user.ID, Name: input.Name, Hint: secret[:len(mcpTokenPrefix)+4], Repositories: repositories, CreatedAt: now}
	if input.ExpiresInDays > 0 {
		expires := now.AddDate(0, 0, input.ExpiresInDays)
		token.ExpiresAt = &expires
	}
	if err := s.store.CreateMCPToken(r.Context(), token, secretHash(secret)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "secret": secret, "url": s.mcpURL()})
}

// revokeMCPToken ends one of the signed-in account's own tokens.
func (s *Server) revokeMCPToken(w http.ResponseWriter, r *http.Request) {
	user, _ := currentUser(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not_authenticated", "Authentication is required")
		return
	}
	token, err := s.store.GetMCPToken(r.Context(), chi.URLParam(r, "id"))
	if err != nil || token.UserID != user.ID {
		writeStoreError(w, store.ErrNotFound)
		return
	}
	s.revokeToken(w, r, user.ID, token.ID)
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, actorID, id string) {
	if err := s.store.RevokeMCPToken(r.Context(), actorID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.mcpLimiter().forget(id)
	w.WriteHeader(http.StatusNoContent)
}

// allMCPTokens pages through every account's tokens for an administrator, newest first, with a
// search on the token's name and its owner, and each token's status.
func (s *Server) allMCPTokens(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_page_size", "Page size must be between 1 and 100")
		return
	}
	page, err := s.store.SearchMCPTokens(r.Context(), searchQuery(r), r.URL.Query().Get("cursor"), limit)
	if errors.Is(err, store.ErrInvalidCursor) {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "The page cursor is invalid")
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.describeTokens(r, page.Items); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// describeTokens fills in each token's owner name and status: whether it works now, and if not,
// why. A token isn't changed by its owner being switched off; it just stops working until the
// owner may use MCP again.
func (s *Server) describeTokens(r *http.Request, tokens []model.MCPToken) error {
	var entries []model.AccessEntry
	loaded := false
	owners := map[string]*model.User{}
	now := time.Now()
	for index := range tokens {
		token := &tokens[index]
		owner, seen := owners[token.UserID]
		if !seen {
			user, err := s.store.GetUser(r.Context(), token.UserID)
			switch {
			case err == nil:
				owner = &user
			case !errors.Is(err, store.ErrNotFound):
				return err
			}
			owners[token.UserID] = owner
		}
		if owner != nil {
			token.UserName = accountName(*owner)
			if token.UserLogin == "" {
				token.UserLogin = owner.Username
			}
		}
		switch {
		case owner == nil || !owner.Active():
			token.Status = "user_disabled"
		case token.ExpiresAt != nil && !now.Before(*token.ExpiresAt):
			token.Status = "expired"
		default:
			if owner.Federated() && !loaded {
				var err error
				if entries, err = s.store.ListAccess(r.Context()); err != nil {
					return err
				}
				loaded = true
			}
			if model.MCPAllowed(*owner, entries) {
				token.Status = "active"
			} else {
				token.Status = "mcp_off"
			}
		}
	}
	return nil
}

func (s *Server) adminRevokeMCPToken(w http.ResponseWriter, r *http.Request) {
	actor, _ := currentUser(r.Context())
	if _, err := s.store.GetMCPToken(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeStoreError(w, err)
		return
	}
	s.revokeToken(w, r, actor.ID, chi.URLParam(r, "id"))
}

type mcpSwitchInput struct {
	Enabled bool `json:"enabled"`
}

// setUserMCP turns MCP on or off for a local account. Directory accounts follow the access list.
func (s *Server) setUserMCP(w http.ResponseWriter, r *http.Request) {
	actor, _ := currentUser(r.Context())
	var input mcpSwitchInput
	if !decodeJSON(w, r, &input) {
		return
	}
	existing, err := s.store.GetUser(r.Context(), chi.URLParam(r, "id"))
	if err != nil || existing.Deleted {
		writeStoreError(w, store.ErrNotFound)
		return
	}
	if existing.Federated() {
		writeError(w, http.StatusConflict, "managed_by_access_list", "Change Active Directory accounts in the list of users and groups")
		return
	}
	user, err := s.store.SetUserMCP(r.Context(), actor.ID, existing.ID, !input.Enabled)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// setAccessMCP turns MCP on or off for an account or a group on the access list.
func (s *Server) setAccessMCP(w http.ResponseWriter, r *http.Request) {
	subjectType := model.SubjectType(chi.URLParam(r, "type"))
	if subjectType != model.SubjectUser && subjectType != model.SubjectGroup {
		writeError(w, http.StatusBadRequest, "invalid_subject_type", "Access is given to a directory account or group")
		return
	}
	var input mcpSwitchInput
	if !decodeJSON(w, r, &input) {
		return
	}
	actor, _ := currentUser(r.Context())
	entry, err := s.store.SetAccessMCP(r.Context(), actor.ID, subjectType, directory.NormalizeGUID(chi.URLParam(r, "subject")), !input.Enabled)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}
