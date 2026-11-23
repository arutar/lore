package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/arutar/lore/platform/control-plane/internal/deletions"
	"github.com/arutar/lore/platform/control-plane/internal/directory"
	"github.com/arutar/lore/platform/control-plane/internal/loreclient"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

type RepositoryManager interface {
	CreateRepository(ctx context.Context, repository model.Repository) error
	DeleteRepository(ctx context.Context, id, token string) error
}

type RepositoryContent interface {
	Tree(ctx context.Context, repositoryID, revision, path string) (loreclient.RepositoryTree, error)
	History(ctx context.Context, repositoryID, cursor string) (loreclient.HistoryPage, error)
	Changes(ctx context.Context, repositoryID, revision string, filter loreclient.ChangeFilter, cursor string, limit int) (loreclient.ChangePage, error)
	ChangeSummary(ctx context.Context, repositoryID, revision string) (loreclient.ChangeSummary, error)
	Locks(ctx context.Context, repositoryID string) ([]loreclient.RepositoryLock, error)
	Download(ctx context.Context, repositoryID, revision, path string) (*loreclient.Download, error)
	LastChanges(ctx context.Context, repositoryID, head string, paths []string) (map[string]loreclient.LastChange, error)
	ReleaseLock(ctx context.Context, repositoryID, branchID, hash string) (loreclient.RepositoryLock, error)
}

type GroupCatalog interface {
	SearchGroups(ctx context.Context, prefix string, limit int) ([]directory.Group, error)
	LookupGroup(ctx context.Context, id string) (directory.Group, error)
	LookupAccount(ctx context.Context, id string) (directory.Account, error)
	FindUser(ctx context.Context, username string) (directory.User, error)
	SearchUsers(ctx context.Context, query string) ([]directory.User, error)
}

type Config struct {
	// Deletions marks the repositories being deleted here, shared with the registry service so that
	// Lore's unregistration leaves their rows until Lore confirms (see package deletions).
	Deletions         *deletions.Tracker
	Repositories      RepositoryManager
	Directory         GroupCatalog
	Content           RepositoryContent
	TokenExchange     *TokenExchange
	RegistrationRealm RegistrationRealm
	Accounts          AccountManager
	BearerAuth        BearerAuthenticator
	StaticDir         string
	// DownloadsDir holds the client builds and manifest.json served at /downloads/.
	DownloadsDir        string
	DevAuth             bool
	DevUser             model.User
	OIDC                AuthConfig
	ExternalURL         string
	NativeRepositoryURL string
	// SecureHeaders adds Strict-Transport-Security; set it when the WebUI is served over HTTPS.
	SecureHeaders bool
	// MCP configures the /mcp endpoint for AI agents; its zero value turns it off.
	MCP MCPConfig
}

type Server struct {
	store   store.Store
	auth    *Authenticator
	config  Config
	groups  groupCache
	limiter *mcpLimiter
}

func NewServer(store store.Store, auth *Authenticator, config Config) http.Handler {
	if config.MCP.Mode == "" {
		config.MCP.Mode = MCPOff
	}
	if config.MCP.MaxRead <= 0 {
		config.MCP.MaxRead = 1 << 20
	}
	if config.MCP.LinkTTL <= 0 {
		config.MCP.LinkTTL = 10 * time.Minute
	}
	server := &Server{store: store, auth: auth, config: config,
		limiter: &mcpLimiter{limit: config.MCP.Concurrency, running: map[string]int{}}}
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)
	router.Use(securityHeaders(config.SecureHeaders))
	router.Use(func(next http.Handler) http.Handler {
		withTimeout := middleware.Timeout(30 * time.Second)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && (strings.HasSuffix(r.URL.Path, "/download") || strings.HasPrefix(r.URL.Path, "/downloads/") || strings.HasPrefix(r.URL.Path, "/mcp/download/")) {
				next.ServeHTTP(w, r)
				return
			}
			withTimeout.ServeHTTP(w, r)
		})
	})
	router.Use(auth.middleware)
	router.Get("/healthz", server.health)
	router.Get("/downloads/{name}", server.download)
	if config.MCP.Mode != MCPOff {
		router.Post("/mcp", server.mcp)
		router.Get("/mcp", server.mcpNoStream)
		router.Delete("/mcp", server.mcpNoStream)
		router.Get("/mcp/download/{key}", server.mcpDownload)
	} else {
		// Off: no MCP at all, rather than the WebUI's page.
		notFound := func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "Resource not found")
		}
		router.HandleFunc("/mcp", notFound)
		router.HandleFunc("/mcp/*", notFound)
	}
	router.Route("/web/v1", func(router chi.Router) {
		router.Get("/config", server.publicConfig)
		router.Get("/session", server.session)
		router.Get("/login", auth.login)
		router.Get("/oidc/callback", auth.callback)
		router.With(requireCSRF).Post("/logout", auth.logout)
		router.Post("/native-logout", server.nativeLogout)
		if config.DevAuth {
			router.Post("/dev-login", server.devLogin)
		}
		if config.TokenExchange != nil {
			router.Get("/lore-oidc/.well-known/openid-configuration", config.TokenExchange.discovery)
			router.Get("/lore-oidc/jwks", config.TokenExchange.jwks)
			router.Post("/lore-oidc/token", config.TokenExchange.exchange)
		}
		router.Get("/mcp-tokens", server.mcpTokens)
		router.With(requireCSRF).Post("/mcp-tokens", server.createMCPToken)
		router.With(requireCSRF).Delete("/mcp-tokens/{id}", server.revokeMCPToken)
		router.Get("/catalog", server.catalog)
		router.Get("/repositories/{id}", server.repository)
		router.Get("/repositories/{id}/tree", server.repositoryTree)
		router.Get("/repositories/{id}/history", server.repositoryHistory)
		router.Get("/repositories/{id}/revisions/{revision}/changes", server.revisionChanges)
		router.Get("/repositories/{id}/revisions/{revision}/summary", server.revisionSummary)
		router.Get("/repositories/{id}/locks", server.repositoryLocks)
		router.With(requireCSRF).Post("/repositories/{id}/locks/release", server.releaseLock)
		router.Get("/repositories/{id}/download", server.repositoryDownload)
		router.Route("/repositories/{id}/grants", func(router chi.Router) {
			router.Get("/", server.grants)
			router.With(requireCSRF).Put("/", server.setGrant)
			router.With(requireCSRF).Delete("/", server.removeGrant)
			router.Get("/export", server.exportGrants)
			router.Get("/subjects", server.grantSubjects)
			router.With(requireCSRF).Post("/import", server.importGrants)
		})
		router.Route("/admin", func(router chi.Router) {
			router.Use(server.requireAdmin)
			router.Get("/users", server.users)
			router.Get("/users/search", server.searchADUsers)
			router.With(requireCSRF).Post("/users", server.createLocalUser)
			router.With(requireCSRF).Post("/users/provision", server.provisionADUser)
			router.Get("/users/export", server.exportUsers)
			router.With(requireCSRF).Post("/users/import", server.importUsers)
			router.With(requireCSRF).Patch("/users/{id}", server.updateUser)
			router.With(requireCSRF).Delete("/users/{id}", server.deleteUser)
			router.With(requireCSRF).Put("/users/{id}/mcp", server.setUserMCP)
			router.Get("/mcp-tokens", server.allMCPTokens)
			router.With(requireCSRF).Delete("/mcp-tokens/{id}", server.adminRevokeMCPToken)
			router.Get("/repositories", server.repositories)
			router.With(requireCSRF).Post("/repositories", server.createRepository)
			router.With(requireCSRF).Patch("/repositories/{id}", server.updateRepository)
			router.With(requireCSRF).Delete("/repositories/{id}", server.deleteRepository)
			router.Get("/settings", server.settings)
			router.With(requireCSRF).Patch("/settings", server.updateSettings)
			router.Get("/audit", server.audit)
			router.Get("/access", server.accessList)
			router.Get("/access/subjects", server.accessSubjects)
			router.With(requireCSRF).Post("/access", server.addAccess)
			router.With(requireCSRF).Delete("/access/{type}/{subject}", server.removeAccess)
			router.With(requireCSRF).Patch("/access/{type}/{subject}", server.updateAccess)
			router.With(requireCSRF).Put("/access/{type}/{subject}/mcp", server.setAccessMCP)
			router.Get("/members", server.members)
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
		if !user.Active() || user.Role != model.RoleAdmin {
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

// contentSecurityPolicy allows only the WebUI's own scripts, styles, images and API calls,
// and forbids framing, which also protects the administration pages from clickjacking.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(https bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := w.Header()
			header.Set("Content-Security-Policy", contentSecurityPolicy)
			header.Set("X-Content-Type-Options", "nosniff")
			header.Set("X-Frame-Options", "DENY")
			header.Set("Referrer-Policy", "same-origin")
			header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			if https {
				header.Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}
