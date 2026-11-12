package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arutar/lore/platform/control-plane/internal/model"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("configure postgres: %w", err)
	}
	store := &Postgres{pool: pool}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := store.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (p *Postgres) Close() error {
	p.pool.Close()
	return nil
}

func (p *Postgres) migrate(ctx context.Context) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL)"); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	for _, entry := range entries {
		var applied bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", entry.Name()).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if applied {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES ($1, $2)", entry.Name(), time.Now().UTC()); err != nil {
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func (p *Postgres) Catalog(ctx context.Context, user *model.User, after string, limit int) (model.Page[model.Repository], error) {
	if user != nil && user.Role == model.RoleAdmin && !user.Disabled {
		return p.ListRepositories(ctx, after, limit)
	}

	query := `
		SELECT r.id, r.name, r.description, r.owner_id, r.created_at, r.updated_at,
		       EXISTS (SELECT 1 FROM grants AS public_grant WHERE public_grant.repository_id = r.id AND public_grant.subject_type = 'anonymous' AND public_grant.permission = 'read')
		FROM repositories AS r
		WHERE ($1 = '' OR r.id > $1)
		  AND EXISTS (
			SELECT 1 FROM grants AS g
			WHERE g.repository_id = r.id
			  AND g.permission IN ('read', 'write', 'owner')
			  AND (
				(g.subject_type = 'anonymous' AND g.permission = 'read')
				OR ($2 AND g.subject_type = 'authenticated' AND g.permission = 'read')
				OR ($2 AND g.subject_type = 'user' AND g.subject = $3)
				OR ($2 AND g.subject_type = 'group' AND g.subject = ANY($4))
			  )
		  )
		ORDER BY r.id
		LIMIT $5`
	authenticated := user != nil && !user.Disabled
	userID := ""
	groups := []string{}
	if user != nil {
		userID = user.ID
		groups = user.Groups
	}
	return p.repositoryPage(ctx, query, after, authenticated, userID, groups, limit+1, limit)
}

func (p *Postgres) ListRepositories(ctx context.Context, after string, limit int) (model.Page[model.Repository], error) {
	query := `
		SELECT id, name, description, owner_id, created_at, updated_at,
		       EXISTS (SELECT 1 FROM grants AS public_grant WHERE public_grant.repository_id = repositories.id AND public_grant.subject_type = 'anonymous' AND public_grant.permission = 'read')
		FROM repositories
		WHERE ($1 = '' OR id > $1)
		ORDER BY id
		LIMIT $2`
	return p.repositoryPage(ctx, query, after, limit+1, limit)
}

func (p *Postgres) repositoryPage(ctx context.Context, query string, args ...any) (model.Page[model.Repository], error) {
	if len(args) < 2 {
		return model.Page[model.Repository]{}, errors.New("repository page arguments")
	}
	limit, ok := args[len(args)-1].(int)
	if !ok {
		return model.Page[model.Repository]{}, errors.New("repository page limit")
	}
	rows, err := p.pool.Query(ctx, query, args[:len(args)-1]...)
	if err != nil {
		return model.Page[model.Repository]{}, fmt.Errorf("list repositories: %w", err)
	}
	defer rows.Close()

	items := make([]model.Repository, 0, limit+1)
	for rows.Next() {
		var repository model.Repository
		if err := rows.Scan(&repository.ID, &repository.Name, &repository.Description, &repository.OwnerID, &repository.CreatedAt, &repository.UpdatedAt, &repository.Public); err != nil {
			return model.Page[model.Repository]{}, fmt.Errorf("scan repository: %w", err)
		}
		items = append(items, repository)
	}
	if err := rows.Err(); err != nil {
		return model.Page[model.Repository]{}, fmt.Errorf("read repositories: %w", err)
	}

	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	return model.Page[model.Repository]{Items: items, NextCursor: next}, nil
}

func (p *Postgres) GetRepository(ctx context.Context, id string) (model.Repository, error) {
	var repository model.Repository
	err := p.pool.QueryRow(ctx, `
		SELECT id, name, description, owner_id, created_at, updated_at,
		       EXISTS (SELECT 1 FROM grants AS public_grant WHERE public_grant.repository_id = repositories.id AND public_grant.subject_type = 'anonymous' AND public_grant.permission = 'read')
		FROM repositories WHERE id = $1`, id).Scan(
		&repository.ID, &repository.Name, &repository.Description, &repository.OwnerID, &repository.CreatedAt, &repository.UpdatedAt, &repository.Public,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Repository{}, ErrNotFound
	}
	if err != nil {
		return model.Repository{}, fmt.Errorf("get repository: %w", err)
	}
	return repository, nil
}

func (p *Postgres) CreateRepository(ctx context.Context, actorID string, repository model.Repository) (model.Repository, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.Repository{}, fmt.Errorf("begin repository create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now().UTC()
	repository.CreatedAt = now
	repository.UpdatedAt = now
	result, err := tx.Exec(ctx, `
		INSERT INTO repositories(id, name, description, owner_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT(id) DO UPDATE SET description = EXCLUDED.description, owner_id = EXCLUDED.owner_id, updated_at = EXCLUDED.updated_at
		WHERE repositories.name = EXCLUDED.name`,
		repository.ID, repository.Name, repository.Description, repository.OwnerID, now, now)
	if err != nil {
		return model.Repository{}, classifyConflict("create repository", err)
	}
	if result.RowsAffected() == 0 {
		return model.Repository{}, ErrConflict
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO grants(repository_id, subject_type, subject, permission)
		VALUES ($1, 'user', $2, 'owner') ON CONFLICT DO NOTHING`, repository.ID, repository.OwnerID)
	if err != nil {
		return model.Repository{}, fmt.Errorf("create owner grant: %w", err)
	}
	if err := insertAudit(ctx, tx, actorID, "repository.create", "repository", repository.ID, "", repository.Name); err != nil {
		return model.Repository{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Repository{}, fmt.Errorf("commit repository create: %w", err)
	}
	return repository, nil
}

func (p *Postgres) UpdateRepository(ctx context.Context, actorID string, repository model.Repository) (model.Repository, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.Repository{}, fmt.Errorf("begin repository update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `
		UPDATE repositories SET name = $2, description = $3, updated_at = $4 WHERE id = $1`,
		repository.ID, repository.Name, repository.Description, time.Now().UTC())
	if err != nil {
		return model.Repository{}, classifyConflict("update repository", err)
	}
	if result.RowsAffected() == 0 {
		return model.Repository{}, ErrNotFound
	}
	if err := insertAudit(ctx, tx, actorID, "repository.update", "repository", repository.ID, "", repository.Name); err != nil {
		return model.Repository{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Repository{}, fmt.Errorf("commit repository update: %w", err)
	}
	return p.GetRepository(ctx, repository.ID)
}

func (p *Postgres) DeleteRepository(ctx context.Context, actorID, id string) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin repository delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var name string
	err = tx.QueryRow(ctx, "DELETE FROM repositories WHERE id = $1 RETURNING name", id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete repository: %w", err)
	}
	if err := insertAudit(ctx, tx, actorID, "repository.delete", "repository", id, name, ""); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit repository delete: %w", err)
	}
	return nil
}

func (p *Postgres) ListUsers(ctx context.Context, after string, limit int) (model.Page[model.User], error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, username, email, display_name, role, groups, source, disabled, created_at, updated_at
		FROM users WHERE ($1 = '' OR id > $1) ORDER BY id LIMIT $2`, after, limit+1)
	if err != nil {
		return model.Page[model.User]{}, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	items := make([]model.User, 0, limit+1)
	for rows.Next() {
		var user model.User
		if err := rows.Scan(&user.ID, &user.Username, &user.Email, &user.DisplayName, &user.Role, &user.Groups, &user.Source, &user.Disabled, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return model.Page[model.User]{}, fmt.Errorf("scan user: %w", err)
		}
		items = append(items, user)
	}
	if err := rows.Err(); err != nil {
		return model.Page[model.User]{}, fmt.Errorf("read users: %w", err)
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	return model.Page[model.User]{Items: items, NextCursor: next}, nil
}

func (p *Postgres) GetUser(ctx context.Context, id string) (model.User, error) {
	var user model.User
	err := p.pool.QueryRow(ctx, `
		SELECT id, username, email, display_name, role, groups, source, disabled, created_at, updated_at
		FROM users WHERE id = $1`, id).Scan(
		&user.ID, &user.Username, &user.Email, &user.DisplayName, &user.Role, &user.Groups, &user.Source, &user.Disabled, &user.CreatedAt, &user.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("get user: %w", err)
	}
	return user, nil
}

func (p *Postgres) UpsertUser(ctx context.Context, user model.User) (model.User, error) {
	now := time.Now().UTC()
	err := p.pool.QueryRow(ctx, `
		INSERT INTO users(id, username, email, display_name, role, groups, source, disabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, COALESCE(NULLIF($5, ''), 'user'), COALESCE($6, '{}'::text[]), $7, FALSE, $8, $8)
		ON CONFLICT (id) DO UPDATE SET
			username = EXCLUDED.username,
			email = EXCLUDED.email,
			display_name = EXCLUDED.display_name,
			groups = EXCLUDED.groups,
			source = EXCLUDED.source,
			updated_at = EXCLUDED.updated_at
		RETURNING id, username, email, display_name, role, groups, source, disabled, created_at, updated_at`,
		user.ID, user.Username, user.Email, user.DisplayName, user.Role, user.Groups, user.Source, now).Scan(
		&user.ID, &user.Username, &user.Email, &user.DisplayName, &user.Role, &user.Groups, &user.Source, &user.Disabled, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return model.User{}, fmt.Errorf("upsert user: %w", err)
	}
	return user, nil
}

func (p *Postgres) UpdateUser(ctx context.Context, actorID, id string, role model.Role, disabled bool) (model.User, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.User{}, fmt.Errorf("begin user update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before string
	err = tx.QueryRow(ctx, "SELECT role FROM users WHERE id = $1 FOR UPDATE", id).Scan(&before)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("lock user: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET role = $2, disabled = $3, updated_at = $4 WHERE id = $1", id, role, disabled, time.Now().UTC()); err != nil {
		return model.User{}, fmt.Errorf("update user: %w", err)
	}
	if err := insertAudit(ctx, tx, actorID, "user.update", "user", id, before, string(role)); err != nil {
		return model.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.User{}, fmt.Errorf("commit user update: %w", err)
	}
	return p.GetUser(ctx, id)
}

func (p *Postgres) ListGrants(ctx context.Context, repositoryID string) ([]model.Grant, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT repository_id, subject_type, subject, permission FROM grants
		WHERE repository_id = $1 ORDER BY subject_type, subject`, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("list grants: %w", err)
	}
	defer rows.Close()
	grants := make([]model.Grant, 0)
	for rows.Next() {
		var grant model.Grant
		if err := rows.Scan(&grant.RepositoryID, &grant.SubjectType, &grant.Subject, &grant.Permission); err != nil {
			return nil, fmt.Errorf("scan grant: %w", err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read grants: %w", err)
	}
	return grants, nil
}

func (p *Postgres) SetGrant(ctx context.Context, actorID string, grant model.Grant) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin grant set: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before string
	_ = tx.QueryRow(ctx, "SELECT permission FROM grants WHERE repository_id = $1 AND subject_type = $2 AND subject = $3", grant.RepositoryID, grant.SubjectType, grant.Subject).Scan(&before)
	_, err = tx.Exec(ctx, `
		INSERT INTO grants(repository_id, subject_type, subject, permission) VALUES ($1, $2, $3, $4)
		ON CONFLICT(repository_id, subject_type, subject) DO UPDATE SET permission = EXCLUDED.permission`,
		grant.RepositoryID, grant.SubjectType, grant.Subject, grant.Permission)
	if err != nil {
		return fmt.Errorf("set grant: %w", err)
	}
	if err := insertAudit(ctx, tx, actorID, "grant.set", "repository", grant.RepositoryID, before, string(grant.Permission)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit grant set: %w", err)
	}
	return nil
}

func (p *Postgres) RemoveGrant(ctx context.Context, actorID string, grant model.Grant) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin grant remove: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before string
	err = tx.QueryRow(ctx, `
		DELETE FROM grants WHERE repository_id = $1 AND subject_type = $2 AND subject = $3
		RETURNING permission`, grant.RepositoryID, grant.SubjectType, grant.Subject).Scan(&before)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove grant: %w", err)
	}
	if err := insertAudit(ctx, tx, actorID, "grant.remove", "repository", grant.RepositoryID, before, ""); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit grant remove: %w", err)
	}
	return nil
}

func (p *Postgres) GetSettings(ctx context.Context) (model.Settings, error) {
	settings := model.Settings{Theme: model.DefaultTheme}
	err := p.pool.QueryRow(ctx, "SELECT registration_enabled, theme FROM settings WHERE id = 'global'").Scan(&settings.RegistrationEnabled, &settings.Theme)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return model.Settings{}, fmt.Errorf("get settings: %w", err)
	}
	return settings, nil
}

func (p *Postgres) UpdateSettings(ctx context.Context, actorID string, settings model.Settings) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin settings update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	before, err := p.GetSettings(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO settings(id, registration_enabled, theme) VALUES ('global', $1, $2)
		ON CONFLICT(id) DO UPDATE SET registration_enabled = EXCLUDED.registration_enabled, theme = EXCLUDED.theme`, settings.RegistrationEnabled, settings.Theme); err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	if err := insertAudit(ctx, tx, actorID, "settings.update", "settings", "global", strconv.FormatBool(before.RegistrationEnabled)+":"+before.Theme, strconv.FormatBool(settings.RegistrationEnabled)+":"+settings.Theme); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit settings update: %w", err)
	}
	return nil
}

func (p *Postgres) ListAudit(ctx context.Context, after int64, limit int) (model.Page[model.AuditEvent], error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, actor_id, action, target_type, target_id, before, after, created_at
		FROM audit_events WHERE id > $1 ORDER BY id LIMIT $2`, after, limit+1)
	if err != nil {
		return model.Page[model.AuditEvent]{}, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()
	items := make([]model.AuditEvent, 0, limit+1)
	for rows.Next() {
		var event model.AuditEvent
		if err := rows.Scan(&event.ID, &event.ActorID, &event.Action, &event.TargetType, &event.TargetID, &event.Before, &event.After, &event.CreatedAt); err != nil {
			return model.Page[model.AuditEvent]{}, fmt.Errorf("scan audit: %w", err)
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return model.Page[model.AuditEvent]{}, fmt.Errorf("read audit: %w", err)
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	return model.Page[model.AuditEvent]{Items: items, NextCursor: next}, nil
}

func (p *Postgres) CreateSession(ctx context.Context, session model.Session) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO sessions(id_hash, csrf_token, user_id, id_token, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		session.IDHash, session.CSRFToken, session.UserID, session.IDToken, session.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (p *Postgres) GetSession(ctx context.Context, idHash string, now time.Time) (model.Session, error) {
	var session model.Session
	err := p.pool.QueryRow(ctx, `
		SELECT id_hash, csrf_token, user_id, id_token, expires_at FROM sessions
		WHERE id_hash = $1 AND expires_at > $2`, idHash, now).Scan(
		&session.IDHash, &session.CSRFToken, &session.UserID, &session.IDToken, &session.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Session{}, ErrNotFound
	}
	if err != nil {
		return model.Session{}, fmt.Errorf("get session: %w", err)
	}
	return session, nil
}

func (p *Postgres) DeleteSession(ctx context.Context, idHash string) error {
	_, err := p.pool.Exec(ctx, "DELETE FROM sessions WHERE id_hash = $1", idHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func insertAudit(ctx context.Context, tx pgx.Tx, actorID, action, targetType, targetID, before, after string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_events(actor_id, action, target_type, target_id, before, after, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		actorID, action, targetType, targetID, before, after, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	return nil
}

func classifyConflict(message string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		return ErrConflict
	}
	return fmt.Errorf("%s: %w", message, err)
}
