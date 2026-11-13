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
	// UpdateDirectoryState stores the result of a directory synchronization.
	UpdateDirectoryState(ctx context.Context, id string, state DirectoryState) (model.User, error)
	// HasActiveAdmin reports whether any enabled administrator exists.
	HasActiveAdmin(ctx context.Context) (bool, error)

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
