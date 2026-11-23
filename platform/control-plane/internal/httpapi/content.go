package httpapi

import (
	"context"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/arutar/lore/platform/control-plane/internal/loreclient"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/policy"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

func (s *Server) repositoryContent(w http.ResponseWriter, r *http.Request) RepositoryContent {
	if _, _, ok := s.readableRepository(w, r); !ok {
		return nil
	}
	return s.configuredContent(w)
}

func (s *Server) configuredContent(w http.ResponseWriter) RepositoryContent {
	if s.config.Content == nil {
		writeError(w, http.StatusServiceUnavailable, "content_unavailable", "Repository content isn't configured")
		return nil
	}
	return s.config.Content
}

func (s *Server) repositoryTree(w http.ResponseWriter, r *http.Request) {
	content := s.repositoryContent(w, r)
	if content == nil {
		return
	}
	id, revision := chi.URLParam(r, "id"), r.URL.Query().Get("revision")
	result, err := content.Tree(r.Context(), id, revision, r.URL.Query().Get("path"))
	if err != nil {
		writeRepositoryContentError(w, err)
		return
	}
	// Who changed each entry last and when, for the branch's current revision. Without it the
	// listing still works: the column stays empty and the reason is logged.
	if revision == "" && result.Revision != "" && len(result.Items) > 0 {
		paths := make([]string, len(result.Items))
		for index, item := range result.Items {
			paths[index] = item.Path
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		changes, err := content.LastChanges(ctx, id, result.Revision, paths)
		cancel()
		if err != nil {
			log.Printf("Last changes of repository %s weren't read: %v", id, err)
		}
		names := s.accountNames(r.Context())
		for index := range result.Items {
			if change, ok := changes[strings.Trim(result.Items[index].Path, "/")]; ok {
				change.Author = names(change.Author)
				result.Items[index].LastChange = &change
			}
		}
	}
	writeJSON(w, http.StatusOK, result)
}

// accountNames returns a lookup from the account IDs Lore records to the names people know,
// remembering each answer for the request.
func (s *Server) accountNames(ctx context.Context) func(string) string {
	names := make(map[string]string)
	return func(id string) string {
		if id == "" {
			return ""
		}
		if name, ok := names[id]; ok {
			return name
		}
		user, err := s.store.GetUser(ctx, id)
		if err != nil {
			names[id] = "Former user"
		} else {
			names[id] = accountName(user)
		}
		return names[id]
	}
}

func (s *Server) repositoryHistory(w http.ResponseWriter, r *http.Request) {
	content := s.repositoryContent(w, r)
	if content == nil {
		return
	}
	result, err := content.History(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("cursor"))
	if err != nil {
		writeRepositoryContentError(w, err)
		return
	}
	username := s.accountNames(r.Context())
	for index := range result.Items {
		result.Items[index].Author = username(result.Items[index].Author)
		result.Items[index].Committer = username(result.Items[index].Committer)
	}
	writeJSON(w, http.StatusOK, result)
}

// revisionChanges lists, a page at a time, the files a revision changed against its parent.
// kind keeps one kind of change and q the paths containing it, so that a filter covers the
// whole revision rather than the page the browser has.
func (s *Server) revisionChanges(w http.ResponseWriter, r *http.Request) {
	content := s.repositoryContent(w, r)
	if content == nil {
		return
	}
	query := r.URL.Query()
	kind := query.Get("kind")
	switch kind {
	case "", loreclient.ChangeAdded, loreclient.ChangeModified, loreclient.ChangeDeleted, loreclient.ChangeMoved, loreclient.ChangeCopied:
	default:
		writeError(w, http.StatusBadRequest, "invalid_kind", "Unknown kind of change")
		return
	}
	limit, _ := strconv.Atoi(query.Get("limit"))
	result, err := content.Changes(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "revision"),
		loreclient.ChangeFilter{Kind: kind, Query: strings.TrimSpace(query.Get("q"))}, query.Get("cursor"), limit)
	if err != nil {
		writeRepositoryContentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// revisionSummary counts a revision's changes by kind. It reads the whole revision, so the
// WebUI asks for it beside the first page instead of before it.
func (s *Server) revisionSummary(w http.ResponseWriter, r *http.Request) {
	content := s.repositoryContent(w, r)
	if content == nil {
		return
	}
	result, err := content.ChangeSummary(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "revision"))
	if err != nil {
		writeRepositoryContentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func accountName(user model.User) string {
	if strings.HasPrefix(user.Username, "service-account-") {
		return "Lore service (automation)"
	}
	if user.DisplayName != "" {
		return user.DisplayName
	}
	if user.Email != "" {
		return user.Email
	}
	return user.Username
}

func (s *Server) repositoryLocks(w http.ResponseWriter, r *http.Request) {
	_, grants, ok := s.readableRepository(w, r)
	if !ok {
		return
	}
	content := s.configuredContent(w)
	if content == nil {
		return
	}
	result, err := content.Locks(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeRepositoryContentError(w, err)
		return
	}
	// Lore records the owner's account ID, like a revision's author; show the name, and mark
	// locks whose owner can no longer sign in (removed or disabled here or in the directory):
	// only someone who manages the repository can release those now.
	type ownerState struct {
		name     string
		disabled bool
	}
	owners := make(map[string]ownerState)
	for index := range result {
		owner := result[index].Owner
		state, ok := owners[owner]
		if !ok {
			user, err := s.store.GetUser(r.Context(), owner)
			switch {
			case err == nil:
				state = ownerState{name: accountName(user), disabled: !user.Active()}
			case errors.Is(err, store.ErrNotFound):
				state = ownerState{name: owner, disabled: true}
			default:
				state = ownerState{name: owner}
			}
			owners[owner] = state
		}
		result[index].Owner = state.name
		result[index].OwnerDisabled = state.disabled
	}
	user, _ := currentUser(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"items": result, "canRelease": policy.CanManage(user, grants)})
}

// releaseLock releases a lock whoever holds it: for the repository's owners and administrators,
// when the holder is away or has left. Recorded in the audit log.
func (s *Server) releaseLock(w http.ResponseWriter, r *http.Request) {
	repository, _, ok := s.manageableRepository(w, r)
	if !ok {
		return
	}
	content := s.configuredContent(w)
	if content == nil {
		return
	}
	var body struct {
		BranchID string `json:"branchId"`
		Hash     string `json:"hash"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.BranchID == "" || body.Hash == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "branchId and hash are required")
		return
	}
	lock, err := content.ReleaseLock(r.Context(), repository.ID, body.BranchID, body.Hash)
	if errors.Is(err, loreclient.ErrLockNotFound) {
		writeError(w, http.StatusNotFound, "lock_not_found", "The lock isn't held any more")
		return
	}
	if err != nil {
		log.Printf("Releasing a lock in repository %s failed: %v", repository.ID, err)
		writeError(w, http.StatusBadGateway, "lock_release_failed", "The Lore server didn't release the lock")
		return
	}
	actor, _ := currentUser(r.Context())
	ownerName := s.accountNames(r.Context())(lock.Owner)
	s.recordAudit(r, model.AuditEvent{ActorID: actor.ID, Action: "lock.release", TargetType: "repository", TargetID: repository.ID,
		SubjectType: model.SubjectUser, Subject: lock.Owner, SubjectName: ownerName, Before: lock.Path})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) repositoryDownload(w http.ResponseWriter, r *http.Request) {
	content := s.repositoryContent(w, r)
	if content == nil {
		return
	}
	result, err := content.Download(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("revision"), r.URL.Query().Get("path"))
	if err != nil {
		writeRepositoryContentError(w, err)
		return
	}
	defer result.Body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": result.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if result.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(result.Size, 10))
	}
	if _, err := io.Copy(w, result.Body); err != nil {
		log.Printf("Repository content download interrupted: %v", err)
	}
}

func writeRepositoryContentError(w http.ResponseWriter, err error) {
	log.Printf("Repository content request failed: %v", err)
	status, code, message := contentProblem(err)
	writeError(w, status, code, message)
}

// contentProblem explains a failed read of repository content: the HTTP status, the error code
// and a message for the person or the MCP client that asked.
func contentProblem(err error) (int, string, string) {
	switch status.Code(err) {
	case codes.NotFound:
		return http.StatusNotFound, "content_not_found", "The requested revision or path wasn't found"
	case codes.InvalidArgument:
		return http.StatusBadRequest, "invalid_content_request", "The revision or repository path is invalid"
	}
	if strings.Contains(err.Error(), "wasn't found in revision") {
		return http.StatusNotFound, "content_not_found", "The requested file wasn't found in this revision"
	}
	if strings.Contains(err.Error(), "invalid") {
		return http.StatusBadRequest, "invalid_content_request", "The revision or repository path is invalid"
	}
	return http.StatusBadGateway, "content_unavailable", "Couldn't read repository content"
}
