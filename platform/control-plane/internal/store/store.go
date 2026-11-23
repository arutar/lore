package store

import (
	"context"
	"errors"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/model"
)

// DirectoryState is what a directory synchronization learned about one account.
type DirectoryState struct {
	DirectoryID string
	Groups      []string
	Disabled    bool
	SyncedAt    time.Time
}

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	// ErrInvalidCursor is a page cursor the store didn't hand out.
	ErrInvalidCursor = errors.New("invalid cursor")
)

type Store interface {
	// Catalog, ListRepositories and ListUsers page by ID after the cursor. A non-empty query
	// keeps only items whose name, description, username, display name or email contain it,
	// case-insensitively.
	Catalog(ctx context.Context, user *model.User, query, after string, limit int) (model.Page[model.Repository], error)
	ListRepositories(ctx context.Context, query, after string, limit int) (model.Page[model.Repository], error)
	GetRepository(ctx context.Context, id string) (model.Repository, error)
	GetRepositoryByName(ctx context.Context, name string) (model.Repository, error)
	// CreateRepository stores the repository, its owner grant and, when repository.Public
	// is set, anonymous read, atomically.
	CreateRepository(ctx context.Context, actorID string, repository model.Repository) (model.Repository, error)
	// UpdateRepository changes the name and description. A non-nil public also sets or
	// removes anonymous read in the same transaction; nil leaves visibility untouched.
	UpdateRepository(ctx context.Context, actorID string, repository model.Repository, public *bool) (model.Repository, error)
	RepositoryNameTaken(ctx context.Context, name, exceptID string) (bool, error)
	DeleteRepository(ctx context.Context, actorID, id string) error

	ListUsers(ctx context.Context, query, after string, limit int) (model.Page[model.User], error)
	// GroupHasMembers reports whether any stored account belongs to the group.
	GroupHasMembers(ctx context.Context, group string) (bool, error)
	GetUser(ctx context.Context, id string) (model.User, error)
	// UpsertUser creates or updates an account by ID, keeping role and Disabled. If another
	// ID holds the same username (the account was recreated in the identity provider, or
	// the name moved to another person in the directory), that record is renamed to free
	// the name and keeps its grants, so a new identity never inherits access by name alone.
	UpsertUser(ctx context.Context, user model.User) (model.User, error)
	UpdateUser(ctx context.Context, actorID, id string, role model.Role, disabled bool) (model.User, error)
	// DeleteUser removes an account: its grants and sessions go, the record is renamed and
	// hidden (model.User.Deleted) so the audit log keeps its name.
	DeleteUser(ctx context.Context, actorID, id string) error
	// UpdateDirectoryState stores the result of a directory synchronization.
	UpdateDirectoryState(ctx context.Context, id string, state DirectoryState) (model.User, error)
	// HasActiveAdmin reports whether any enabled administrator exists.
	HasActiveAdmin(ctx context.Context) (bool, error)
	// ApplyDirectoryAccess stores what the access list gives a directory account: whether it's
	// admitted (model.User.AccessDenied) and its role.
	ApplyDirectoryAccess(ctx context.Context, id string, denied bool, role model.Role) (model.User, error)

	// ListAccess returns the Active Directory access list, groups first, then by name.
	ListAccess(ctx context.Context) ([]model.AccessEntry, error)
	// AddAccess adds an entry; adding one that exists only updates its name and detail.
	AddAccess(ctx context.Context, actorID string, entry model.AccessEntry) error
	// UpdateAccess changes an entry's role and whether it's switched off.
	UpdateAccess(ctx context.Context, actorID string, subjectType model.SubjectType, subject string, role model.Role, disabled bool) (model.AccessEntry, error)
	RemoveAccess(ctx context.Context, actorID string, subjectType model.SubjectType, subject string) error

	// SetUserMCP switches MCP on or off for a local account; SetAccessMCP for an entry on the
	// access list. Both record the change in the audit log.
	SetUserMCP(ctx context.Context, actorID, id string, disabled bool) (model.User, error)
	SetAccessMCP(ctx context.Context, actorID string, subjectType model.SubjectType, subject string, disabled bool) (model.AccessEntry, error)

	// CreateMCPToken stores a token under the SHA-256 of its secret.
	CreateMCPToken(ctx context.Context, token model.MCPToken, hash string) error
	// ListMCPTokens returns the account's tokens, newest first; an empty userID lists everyone's.
	// Revoked tokens aren't listed.
	ListMCPTokens(ctx context.Context, userID string) ([]model.MCPToken, error)
	// SearchMCPTokens pages through every account's unrevoked tokens, newest first, with the
	// owner's username in UserLogin. A non-empty query keeps tokens whose name, or whose owner's
	// username, display name or email, contain it, case-insensitively.
	SearchMCPTokens(ctx context.Context, query, after string, limit int) (model.Page[model.MCPToken], error)
	GetMCPToken(ctx context.Context, id string) (model.MCPToken, error)
	GetMCPTokenByHash(ctx context.Context, hash string) (model.MCPToken, error)
	// RevokeMCPToken ends a token at once, together with the download links issued through it.
	RevokeMCPToken(ctx context.Context, actorID, id string) error
	// TouchMCPToken records when a token was last used.
	TouchMCPToken(ctx context.Context, id string, at time.Time) error
	// CreateMCPLink stores a download link under the SHA-256 of its key and drops expired ones.
	CreateMCPLink(ctx context.Context, hash string, link model.MCPLink) error
	// GetMCPLink returns a link that hasn't expired at now.
	GetMCPLink(ctx context.Context, hash string, now time.Time) (model.MCPLink, error)

	ListGrants(ctx context.Context, repositoryID string) ([]model.Grant, error)
	SetGrant(ctx context.Context, actorID string, grant model.Grant) error
	RemoveGrant(ctx context.Context, actorID string, grant model.Grant) error

	GetSettings(ctx context.Context) (model.Settings, error)
	UpdateSettings(ctx context.Context, actorID string, settings model.Settings) error
	// ListAudit returns events newest first. before is the cursor: 0 starts at the newest.
	ListAudit(ctx context.Context, before int64, limit int) (model.Page[model.AuditEvent], error)
	RecordAudit(ctx context.Context, event model.AuditEvent) error

	CreateSession(ctx context.Context, session model.Session) error
	GetSession(ctx context.Context, idHash string, now time.Time) (model.Session, error)
	DeleteSession(ctx context.Context, idHash string) error

	Close() error
}
