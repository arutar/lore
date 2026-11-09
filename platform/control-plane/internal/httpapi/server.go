package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

type RepositoryCreator interface {
	CreateRepository(ctx context.Context, repository model.Repository) error
}

type Config struct {
	Repositories  RepositoryCreator
	TokenExchange *TokenExchange
	StaticDir     string
	DevAuth       bool
	DevUser       model.User
	OIDC          AuthConfig
	ExternalURL   string
}

type Server struct {
	store  store.Store
	auth   *Authenticator
	config Config
}

func NewServer(store store.Store, auth *Authenticator, config Config) http.Handler {
	server := &Server{store: store, auth: auth, config: config}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(30 * time.Second))
	router.Use(auth.middleware)
	router.Get("/healthz", server.health)
	router.Route("/web/v1", func(router chi.Router) {
		router.Get("/config", server.publicConfig)
		router.Get("/session", server.session)
		router.Get("/login", auth.login)
		router.Get("/oidc/callback", auth.callback)
		router.With(requireCSRF).Post("/logout", auth.logout)
		if config.DevAuth {
			router.Post("/dev-login", server.devLogin)
		}
		if config.TokenExchange != nil {
			router.Get("/lore-oidc/.well-known/openid-configuration", config.TokenExchange.discovery)
			router.Get("/lore-oidc/jwks", config.TokenExchange.jwks)
			router.Post("/lore-oidc/token", config.TokenExchange.exchange)
		}
		router.Get("/catalog", server.catalog)
		router.Get("/repositories/{id}", server.repository)
		router.Route("/repositories/{id}/grants", func(router chi.Router) {
			router.Get("/", server.grants)
			router.With(requireCSRF).Put("/", server.setGrant)
			router.With(requireCSRF).Delete("/", server.removeGrant)
			router.Get("/export", server.exportGrants)
			router.With(requireCSRF).Post("/import", server.importGrants)
		})
		router.Route("/admin", func(router chi.Router) {
			router.Use(server.requireAdmin)
			router.Get("/users", server.users)
			router.Get("/users/export", server.exportUsers)
			router.With(requireCSRF).Post("/users/import", server.importUsers)
			router.With(requireCSRF).Patch("/users/{id}", server.updateUser)
			router.Get("/repositories", server.repositories)
			router.With(requireCSRF).Post("/repositories", server.createRepository)
			router.With(requireCSRF).Patch("/repositories/{id}", server.updateRepository)
			router.Get("/settings", server.settings)
			router.With(requireCSRF).Patch("/settings", server.updateSettings)
			router.Get("/audit", server.audit)
		})
	})
	router.NotFound(server.static)
	return router
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := currentUser(r.Context())
		if user == nil {
			writeError(w, http.StatusUnauthorized, "not_authenticated", "Authentication is required")
			return
		}
		if user.Disabled || user.Role != model.RoleAdmin {
			writeError(w, http.StatusForbidden, "not_authorized", "Administrator access is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Service is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "registrationEnabled": settings.RegistrationEnabled})
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if s.config.StaticDir == "" {
		writeError(w, http.StatusNotFound, "not_found", "Resource not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method isn't supported")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" || !strings.Contains(path, ".") {
		path = "index.html"
	}
	if strings.Contains(path, "..") || strings.Contains(path, "\\") {
		writeError(w, http.StatusBadRequest, "invalid_path", "Static path is invalid")
		return
	}
	http.ServeFile(w, r, s.config.StaticDir+"/"+path)
}
