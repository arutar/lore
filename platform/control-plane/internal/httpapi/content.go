package httpapi

import (
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/arutar/lore/platform/control-plane/internal/model"
)

func (s *Server) repositoryContent(w http.ResponseWriter, r *http.Request) RepositoryContent {
	if _, _, ok := s.readableRepository(w, r); !ok {
		return nil
	}
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
	result, err := content.Tree(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("revision"), r.URL.Query().Get("path"))
	if err != nil {
		writeRepositoryContentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
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
	usernames := make(map[string]string)
	username := func(id string) string {
		if id == "" {
			return ""
		}
		if name, ok := usernames[id]; ok {
			return name
		}
		user, err := s.store.GetUser(r.Context(), id)
		if err != nil {
			return "Former user"
		}
		usernames[id] = accountName(user)
		return usernames[id]
	}
	for index := range result.Items {
		result.Items[index].Author = username(result.Items[index].Author)
		result.Items[index].Committer = username(result.Items[index].Committer)
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
	content := s.repositoryContent(w, r)
	if content == nil {
		return
	}
	result, err := content.Locks(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeRepositoryContentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
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
	switch status.Code(err) {
	case codes.NotFound:
		writeError(w, http.StatusNotFound, "content_not_found", "The requested revision or path wasn't found")
	case codes.InvalidArgument:
		writeError(w, http.StatusBadRequest, "invalid_content_request", "The revision or repository path is invalid")
	default:
		if strings.Contains(err.Error(), "wasn't found in revision") {
			writeError(w, http.StatusNotFound, "content_not_found", "The requested file wasn't found in this revision")
			return
		}
		if strings.Contains(err.Error(), "invalid") {
			writeError(w, http.StatusBadRequest, "invalid_content_request", "The revision or repository path is invalid")
			return
		}
		writeError(w, http.StatusBadGateway, "content_unavailable", "Couldn't read repository content")
	}
}
