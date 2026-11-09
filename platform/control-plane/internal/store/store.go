package store

import (
	"context"
	"errors"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/model"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type Store interface {
	Catalog(ctx context.Context, user *model.User, after string, limit int) (model.Page[model.Repository], error)
	ListRepositories(ctx context.Context, after string, limit int) (model.Page[model.Repository], error)
	GetRepository(ctx context.Context, id string) (model.Repository, error)
	CreateRepository(ctx context.Context, actorID string, repository model.Repository) (model.Repository, error)
	UpdateRepository(ctx context.Context, actorID string, repository model.Repository) (model.Repository, error)
	DeleteRepository(ctx context.Context, actorID, id string) error

	ListUsers(ctx context.Context, after string, limit int) (model.Page[model.User], error)
	GetUser(ctx context.Context, id string) (model.User, error)
	UpsertUser(ctx context.Context, user model.User) (model.User, error)
	UpdateUser(ctx context.Context, actorID, id string, role model.Role, disabled bool) (model.User, error)

	ListGrants(ctx context.Context, repositoryID string) ([]model.Grant, error)
	SetGrant(ctx context.Context, actorID string, grant model.Grant) error
	RemoveGrant(ctx context.Context, actorID string, grant model.Grant) error

	GetSettings(ctx context.Context) (model.Settings, error)
	UpdateSettings(ctx context.Context, actorID string, settings model.Settings) error
	ListAudit(ctx context.Context, after int64, limit int) (model.Page[model.AuditEvent], error)

	CreateSession(ctx context.Context, session model.Session) error
	GetSession(ctx context.Context, idHash string, now time.Time) (model.Session, error)
	DeleteSession(ctx context.Context, idHash string) error

	Close() error
}
