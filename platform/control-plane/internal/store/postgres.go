package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
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
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("configure postgres: %w", err)
	}
	// pgx prepares and caches statements; after a few runs PostgreSQL may switch a statement to
	// a generic plan, which can't tell a selective search pattern or group from an unselective
	// one and falls back to reading whole tables instead of the trigram and GIN indexes. The
	// queries are small, so planning each execution for its actual values costs far less.
	if _, set := config.ConnConfig.RuntimeParams["plan_cache_mode"]; !set {
		config.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_custom_plan"
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
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

const repositoryColumns = `r.id, r.name, r.description, r.owner_id, r.created_at, r.updated_at,
	EXISTS (SELECT 1 FROM grants AS public_grant WHERE public_grant.repository_id = r.id AND public_grant.subject_type = 'anonymous' AND public_grant.permission = 'read')`

// containsPattern turns a search into a LIKE pattern matching it anywhere: %, _ and \ typed
// by a user are escaped, so they match literally. Lowered in SQL (lower($n)) to use the
// trigram indexes on lower(column).
func containsPattern(search string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search)
	return "%" + escaped + "%"
}

// pageConditions builds the cursor and search conditions only when they apply: a predicate
// like ($1 = ” OR id > $1) keeps a cached generic plan from using the indexes.
func pageConditions(after, search string, columns []string, args []any) ([]string, []any) {
	var conditions []string
	if after != "" {
		args = append(args, after)
		conditions = append(conditions, fmt.Sprintf("%s > $%d", columns[0], len(args)))
	}
	if search != "" {
		args = append(args, containsPattern(search))
		var matches []string
		for _, column := range columns[1:] {
			matches = append(matches, fmt.Sprintf(`lower(%s) LIKE lower($%d) ESCAPE '\'`, column, len(args)))
		}
		conditions = append(conditions, "("+strings.Join(matches, " OR ")+")")
	}
	return conditions, args
}

var repositorySearchColumns = []string{"r.id", "r.name", "r.description"}

func (p *Postgres) Catalog(ctx context.Context, user *model.User, search, after string, limit int) (model.Page[model.Repository], error) {
	if user != nil && user.Role == model.RoleAdmin && user.Active() {
		return p.ListRepositories(ctx, search, after, limit)
	}
	authenticated := user != nil && user.Active()
	args := []any{}
	visible := []string{"(g.subject_type = 'anonymous' AND g.permission = 'read')"}
	if authenticated {
		args = append(args, user.ID, user.Groups)
		visible = append(visible,
			"(g.subject_type = 'authenticated' AND g.permission = 'read')",
			"(g.subject_type = 'user' AND g.subject = $1)",
			"(g.subject_type = 'group' AND g.subject = ANY($2))")
	}
	conditions, args := pageConditions(after, search, repositorySearchColumns, args)
	conditions = append(conditions, `EXISTS (
			SELECT 1 FROM grants AS g
			WHERE g.repository_id = r.id AND g.permission IN ('read', 'write', 'owner')
			  AND (`+strings.Join(visible, " OR ")+`))`)
	args = append(args, limit+1)
	query := `SELECT ` + repositoryColumns + ` FROM repositories AS r WHERE ` + strings.Join(conditions, " AND ") +
		fmt.Sprintf(" ORDER BY r.id LIMIT $%d", len(args))
	return p.repositoryPage(ctx, query, append(args, limit)...)
}

func (p *Postgres) ListRepositories(ctx context.Context, search, after string, limit int) (model.Page[model.Repository], error) {
	conditions, args := pageConditions(after, search, repositorySearchColumns, nil)
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	args = append(args, limit+1)
	query := `SELECT ` + repositoryColumns + ` FROM repositories AS r` + where + fmt.Sprintf(" ORDER BY r.id LIMIT $%d", len(args))
	return p.repositoryPage(ctx, query, append(args, limit)...)
}

func (p *Postgres) GetRepositoryByName(ctx context.Context, name string) (model.Repository, error) {
	var repository model.Repository
	err := p.pool.QueryRow(ctx, "SELECT "+repositoryColumns+" FROM repositories AS r WHERE r.name = $1", name).Scan(
		&repository.ID, &repository.Name, &repository.Description, &repository.OwnerID, &repository.CreatedAt, &repository.UpdatedAt, &repository.Public,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Repository{}, ErrNotFound
	}
	if err != nil {
		return model.Repository{}, fmt.Errorf("get repository by name: %w", err)
	}
	return repository, nil
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
	if repository.Public {
		if err := setPublicTx(ctx, tx, actorID, repository.ID, true); err != nil {
			return model.Repository{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Repository{}, fmt.Errorf("commit repository create: %w", err)
	}
	return repository, nil
}

func (p *Postgres) RepositoryNameTaken(ctx context.Context, name, exceptID string) (bool, error) {
	var taken bool
	err := p.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM repositories WHERE name = $1 AND id <> $2)", name, exceptID).Scan(&taken)
	if err != nil {
		return false, fmt.Errorf("check repository name: %w", err)
	}
	return taken, nil
}

func (p *Postgres) UpdateRepository(ctx context.Context, actorID string, repository model.Repository, public *bool) (model.Repository, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.Repository{}, fmt.Errorf("begin repository update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var beforeName, beforeDescription string
	err = tx.QueryRow(ctx, "SELECT name, description FROM repositories WHERE id = $1 FOR UPDATE", repository.ID).Scan(&beforeName, &beforeDescription)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Repository{}, ErrNotFound
	}
	if err != nil {
		return model.Repository{}, fmt.Errorf("lock repository: %w", err)
	}
	if beforeName != repository.Name || beforeDescription != repository.Description {
		if _, err := tx.Exec(ctx, `
			UPDATE repositories SET name = $2, description = $3, updated_at = $4 WHERE id = $1`,
			repository.ID, repository.Name, repository.Description, time.Now().UTC()); err != nil {
			return model.Repository{}, classifyConflict("update repository", err)
		}
		if err := insertAudit(ctx, tx, actorID, "repository.update", "repository", repository.ID, beforeName, repository.Name); err != nil {
			return model.Repository{}, err
		}
	}
	if public != nil {
		if err := setPublicTx(ctx, tx, actorID, repository.ID, *public); err != nil {
			return model.Repository{}, err
		}
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

func (p *Postgres) ListUsers(ctx context.Context, search, after string, limit int) (model.Page[model.User], error) {
	conditions, args := pageConditions(after, search, []string{"id", "username", "display_name", "email"}, nil)
	where := " WHERE " + strings.Join(append([]string{"NOT deleted"}, conditions...), " AND ")
	args = append(args, limit+1)
	rows, err := p.pool.Query(ctx, "SELECT "+userColumns+" FROM users"+where+fmt.Sprintf(" ORDER BY id LIMIT $%d", len(args)), args...)
	if err != nil {
		return model.Page[model.User]{}, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	items := make([]model.User, 0, limit+1)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return model.Page[model.User]{}, err
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

const userColumns = "id, username, email, display_name, role, groups, source, disabled, created_at, updated_at, directory_id, directory_disabled, groups_synced_at, access_denied, deleted, mcp_disabled"

func scanUser(row pgx.Row) (model.User, error) {
	var user model.User
	err := row.Scan(&user.ID, &user.Username, &user.Email, &user.DisplayName, &user.Role, &user.Groups, &user.Source, &user.Disabled,
		&user.CreatedAt, &user.UpdatedAt, &user.DirectoryID, &user.DirectoryDisabled, &user.GroupsSyncedAt, &user.AccessDenied, &user.Deleted, &user.MCPDisabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("scan user: %w", err)
	}
	return user, nil
}

func (p *Postgres) GetUser(ctx context.Context, id string) (model.User, error) {
	return scanUser(p.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE id = $1", id))
}

func (p *Postgres) UpsertUser(ctx context.Context, user model.User) (model.User, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.User{}, fmt.Errorf("begin user upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := time.Now().UTC()
	var formerID, formerName string
	err = tx.QueryRow(ctx, "SELECT id, username FROM users WHERE username = $1 AND id <> $2 FOR UPDATE", user.Username, user.ID).Scan(&formerID, &formerName)
	switch {
	case err == nil:
		retired := formerName + "~" + formerID
		// Only the name is freed. The former record keeps its status: it may be a person who
		// was renamed in the directory and will take their new name at the next sign-in.
		if _, err := tx.Exec(ctx, "UPDATE users SET username = $2, updated_at = $3 WHERE id = $1", formerID, retired, now); err != nil {
			return model.User{}, fmt.Errorf("retire former identity: %w", err)
		}
		if err := insertAudit(ctx, tx, "", "user.supersede", "user", formerID, formerName, user.ID); err != nil {
			return model.User{}, err
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return model.User{}, fmt.Errorf("check username: %w", err)
	}
	stored, err := scanUser(tx.QueryRow(ctx, `
		INSERT INTO users(id, username, email, display_name, role, groups, source, disabled, created_at, updated_at, directory_id, directory_disabled, groups_synced_at, access_denied)
		VALUES ($1, $2, $3, $4, COALESCE(NULLIF($5, ''), 'user'), COALESCE($6, '{}'::text[]), $7, FALSE, $8, $8, $9, $10, $11, $12)
		ON CONFLICT (id) DO UPDATE SET
			username = EXCLUDED.username,
			email = EXCLUDED.email,
			display_name = EXCLUDED.display_name,
			groups = EXCLUDED.groups,
			source = EXCLUDED.source,
			directory_id = EXCLUDED.directory_id,
			directory_disabled = EXCLUDED.directory_disabled,
			groups_synced_at = EXCLUDED.groups_synced_at,
			access_denied = EXCLUDED.access_denied,
			updated_at = EXCLUDED.updated_at
		RETURNING `+userColumns,
		user.ID, user.Username, user.Email, user.DisplayName, user.Role, user.Groups, user.Source, now,
		user.DirectoryID, user.DirectoryDisabled, user.GroupsSyncedAt, user.AccessDenied))
	if err != nil {
		return model.User{}, fmt.Errorf("upsert user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return model.User{}, fmt.Errorf("commit user upsert: %w", err)
	}
	return stored, nil
}

func (p *Postgres) UpdateDirectoryState(ctx context.Context, id string, state DirectoryState) (model.User, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.User{}, fmt.Errorf("begin directory update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var wasDisabled bool
	err = tx.QueryRow(ctx, "SELECT directory_disabled FROM users WHERE id = $1 FOR UPDATE", id).Scan(&wasDisabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("lock user: %w", err)
	}
	groups := state.Groups
	if groups == nil {
		groups = []string{}
	}
	user, err := scanUser(tx.QueryRow(ctx, `
		UPDATE users SET directory_id = $2, groups = $3, directory_disabled = $4, groups_synced_at = $5
		WHERE id = $1 RETURNING `+userColumns, id, state.DirectoryID, groups, state.Disabled, state.SyncedAt))
	if err != nil {
		return model.User{}, err
	}
	if wasDisabled != state.Disabled {
		action := "user.directory_enable"
		if state.Disabled {
			action = "user.directory_disable"
		}
		if err := insertAudit(ctx, tx, "", action, "user", id, "", ""); err != nil {
			return model.User{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return model.User{}, fmt.Errorf("commit directory update: %w", err)
	}
	return user, nil
}

func (p *Postgres) GroupHasMembers(ctx context.Context, group string) (bool, error) {
	var exists bool
	if err := p.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE groups @> ARRAY[$1]::text[])", group).Scan(&exists); err != nil {
		return false, fmt.Errorf("check group members: %w", err)
	}
	return exists, nil
}

func (p *Postgres) HasActiveAdmin(ctx context.Context) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE role = 'admin' AND NOT disabled AND NOT directory_disabled AND NOT access_denied AND NOT deleted)").Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check administrators: %w", err)
	}
	return exists, nil
}

func (p *Postgres) ApplyDirectoryAccess(ctx context.Context, id string, denied bool, role model.Role) (model.User, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.User{}, fmt.Errorf("begin access update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var wasDenied bool
	var wasRole model.Role
	err = tx.QueryRow(ctx, "SELECT access_denied, role FROM users WHERE id = $1 FOR UPDATE", id).Scan(&wasDenied, &wasRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("lock user: %w", err)
	}
	user, err := scanUser(tx.QueryRow(ctx, "UPDATE users SET access_denied = $2, role = $3, updated_at = updated_at WHERE id = $1 RETURNING "+userColumns, id, denied, role))
	if err != nil {
		return model.User{}, err
	}
	if wasDenied != denied {
		action := "user.access_allow"
		if denied {
			action = "user.access_deny"
		}
		if err := insertAudit(ctx, tx, "", action, "user", id, "", ""); err != nil {
			return model.User{}, err
		}
	}
	if wasRole != role {
		if err := insertAudit(ctx, tx, "", "user.role_from_directory", "user", id, string(wasRole), string(role)); err != nil {
			return model.User{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return model.User{}, fmt.Errorf("commit access update: %w", err)
	}
	return user, nil
}

func (p *Postgres) ListAccess(ctx context.Context) ([]model.AccessEntry, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT subject_type, subject, name, detail, role, disabled, mcp_disabled, created_at FROM directory_access
		ORDER BY subject_type = 'user', lower(name), subject`)
	if err != nil {
		return nil, fmt.Errorf("read access list: %w", err)
	}
	defer rows.Close()
	entries := []model.AccessEntry{}
	for rows.Next() {
		var entry model.AccessEntry
		if err := rows.Scan(&entry.SubjectType, &entry.Subject, &entry.Name, &entry.Detail, &entry.Role, &entry.Disabled, &entry.MCPDisabled, &entry.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan access entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (p *Postgres) AddAccess(ctx context.Context, actorID string, entry model.AccessEntry) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin access add: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		INSERT INTO directory_access(subject_type, subject, name, detail, role, disabled, created_at) VALUES ($1, $2, $3, $4, COALESCE(NULLIF($5, ''), 'user'), $6, $7)
		ON CONFLICT (subject_type, subject) DO UPDATE SET name = EXCLUDED.name, detail = EXCLUDED.detail
		WHERE directory_access.name <> EXCLUDED.name OR directory_access.detail <> EXCLUDED.detail`,
		entry.SubjectType, entry.Subject, entry.Name, entry.Detail, entry.Role, entry.Disabled, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("add access: %w", err)
	}
	if tag.RowsAffected() > 0 {
		if err := insertAccessAudit(ctx, tx, actorID, "access.add", entry.SubjectType, entry.Subject, "", entry.Name); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit access add: %w", err)
	}
	return nil
}

func (p *Postgres) UpdateAccess(ctx context.Context, actorID string, subjectType model.SubjectType, subject string, role model.Role, disabled bool) (model.AccessEntry, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.AccessEntry{}, fmt.Errorf("begin access update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var entry model.AccessEntry
	err = tx.QueryRow(ctx, `
		SELECT subject_type, subject, name, detail, role, disabled, mcp_disabled, created_at FROM directory_access
		WHERE subject_type = $1 AND subject = $2 FOR UPDATE`, subjectType, subject).
		Scan(&entry.SubjectType, &entry.Subject, &entry.Name, &entry.Detail, &entry.Role, &entry.Disabled, &entry.MCPDisabled, &entry.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.AccessEntry{}, ErrNotFound
	}
	if err != nil {
		return model.AccessEntry{}, fmt.Errorf("read access entry: %w", err)
	}
	if entry.Role == role && entry.Disabled == disabled {
		return entry, nil
	}
	if _, err := tx.Exec(ctx, "UPDATE directory_access SET role = $3, disabled = $4 WHERE subject_type = $1 AND subject = $2", subjectType, subject, role, disabled); err != nil {
		return model.AccessEntry{}, fmt.Errorf("update access entry: %w", err)
	}
	before := fmt.Sprintf("%s:%t", entry.Role, !entry.Disabled)
	after := fmt.Sprintf("%s:%t", role, !disabled)
	if err := insertAccessAudit(ctx, tx, actorID, "access.update", subjectType, subject, before, after); err != nil {
		return model.AccessEntry{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.AccessEntry{}, fmt.Errorf("commit access update: %w", err)
	}
	entry.Role, entry.Disabled = role, disabled
	return entry, nil
}

func (p *Postgres) RemoveAccess(ctx context.Context, actorID string, subjectType model.SubjectType, subject string) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin access remove: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var name string
	err = tx.QueryRow(ctx, "DELETE FROM directory_access WHERE subject_type = $1 AND subject = $2 RETURNING name", subjectType, subject).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("remove access: %w", err)
	}
	if err := insertAccessAudit(ctx, tx, actorID, "access.remove", subjectType, subject, name, ""); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit access remove: %w", err)
	}
	return nil
}

func (p *Postgres) DeleteUser(ctx context.Context, actorID, id string) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin user delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var username string
	err = tx.QueryRow(ctx, "SELECT username FROM users WHERE id = $1 AND NOT deleted FOR UPDATE", id).Scan(&username)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock user: %w", err)
	}
	// The name is freed so a new account may take it; the record keeps it after "~".
	if _, err := tx.Exec(ctx, "UPDATE users SET deleted = TRUE, disabled = TRUE, username = $2, updated_at = $3 WHERE id = $1", id, username+"~"+id, time.Now().UTC()); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM grants WHERE subject_type = 'user' AND subject = $1", id); err != nil {
		return fmt.Errorf("delete user grants: %w", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1", id); err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	// The account's MCP tokens and their download links end with it.
	if _, err := tx.Exec(ctx, "UPDATE mcp_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL", id, time.Now().UTC()); err != nil {
		return fmt.Errorf("revoke user MCP tokens: %w", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM mcp_links WHERE user_id = $1", id); err != nil {
		return fmt.Errorf("delete user MCP links: %w", err)
	}
	if err := insertAudit(ctx, tx, actorID, "user.delete", "user", id, username, ""); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit user delete: %w", err)
	}
	return nil
}

func (p *Postgres) UpdateUser(ctx context.Context, actorID, id string, role model.Role, disabled bool) (model.User, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.User{}, fmt.Errorf("begin user update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before string
	var beforeDisabled bool
	err = tx.QueryRow(ctx, "SELECT role, disabled FROM users WHERE id = $1 FOR UPDATE", id).Scan(&before, &beforeDisabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, fmt.Errorf("lock user: %w", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET role = $2, disabled = $3, updated_at = $4 WHERE id = $1", id, role, disabled, time.Now().UTC()); err != nil {
		return model.User{}, fmt.Errorf("update user: %w", err)
	}
	if before != string(role) {
		if err := insertAudit(ctx, tx, actorID, "user.update", "user", id, before, string(role)); err != nil {
			return model.User{}, err
		}
	}
	if beforeDisabled != disabled {
		action := "user.enable"
		if disabled {
			action = "user.disable"
		}
		if err := insertAudit(ctx, tx, actorID, action, "user", id, "", ""); err != nil {
			return model.User{}, err
		}
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
	if err := setGrantTx(ctx, tx, actorID, grant); err != nil {
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
	if err := removeGrantTx(ctx, tx, actorID, grant); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit grant remove: %w", err)
	}
	return nil
}

// setGrantTx upserts a grant and audits it only when the permission really changes.
func setGrantTx(ctx context.Context, tx pgx.Tx, actorID string, grant model.Grant) error {
	var before string
	err := tx.QueryRow(ctx, `
		SELECT permission FROM grants WHERE repository_id = $1 AND subject_type = $2 AND subject = $3 FOR UPDATE`,
		grant.RepositoryID, grant.SubjectType, grant.Subject).Scan(&before)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read grant: %w", err)
	}
	if before == string(grant.Permission) {
		return nil
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO grants(repository_id, subject_type, subject, permission) VALUES ($1, $2, $3, $4)
		ON CONFLICT(repository_id, subject_type, subject) DO UPDATE SET permission = EXCLUDED.permission`,
		grant.RepositoryID, grant.SubjectType, grant.Subject, grant.Permission)
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23503" {
			return ErrNotFound
		}
		return fmt.Errorf("set grant: %w", err)
	}
	return insertGrantAudit(ctx, tx, actorID, "grant.set", grant, before, string(grant.Permission))
}

func removeGrantTx(ctx context.Context, tx pgx.Tx, actorID string, grant model.Grant) error {
	var before string
	err := tx.QueryRow(ctx, `
		DELETE FROM grants WHERE repository_id = $1 AND subject_type = $2 AND subject = $3
		RETURNING permission`, grant.RepositoryID, grant.SubjectType, grant.Subject).Scan(&before)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove grant: %w", err)
	}
	return insertGrantAudit(ctx, tx, actorID, "grant.remove", grant, before, "")
}

func setPublicTx(ctx context.Context, tx pgx.Tx, actorID, repositoryID string, public bool) error {
	grant := model.Grant{RepositoryID: repositoryID, SubjectType: model.SubjectAnonymous, Permission: model.PermissionRead}
	if public {
		return setGrantTx(ctx, tx, actorID, grant)
	}
	return removeGrantTx(ctx, tx, actorID, grant)
}

func (p *Postgres) GetSettings(ctx context.Context) (model.Settings, error) {
	settings := model.Settings{Theme: model.DefaultTheme, Language: model.DefaultLanguage}
	err := p.pool.QueryRow(ctx, "SELECT registration_enabled, theme, language FROM settings WHERE id = 'global'").Scan(&settings.RegistrationEnabled, &settings.Theme, &settings.Language)
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
		INSERT INTO settings(id, registration_enabled, theme, language) VALUES ('global', $1, $2, $3)
		ON CONFLICT(id) DO UPDATE SET registration_enabled = EXCLUDED.registration_enabled, theme = EXCLUDED.theme, language = EXCLUDED.language`,
		settings.RegistrationEnabled, settings.Theme, settings.Language); err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	if err := insertAudit(ctx, tx, actorID, "settings.update", "settings", "global", strconv.FormatBool(before.RegistrationEnabled)+":"+before.Theme+":"+before.Language, strconv.FormatBool(settings.RegistrationEnabled)+":"+settings.Theme+":"+settings.Language); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit settings update: %w", err)
	}
	return nil
}

func (p *Postgres) ListAudit(ctx context.Context, before int64, limit int) (model.Page[model.AuditEvent], error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, actor_id, action, target_type, target_id, before, after, subject_type, subject, created_at
		FROM audit_events WHERE ($1::bigint = 0 OR id < $1::bigint) ORDER BY id DESC LIMIT $2`, before, limit+1)
	if err != nil {
		return model.Page[model.AuditEvent]{}, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()
	items := make([]model.AuditEvent, 0, limit+1)
	for rows.Next() {
		var event model.AuditEvent
		if err := rows.Scan(&event.ID, &event.ActorID, &event.Action, &event.TargetType, &event.TargetID, &event.Before, &event.After, &event.SubjectType, &event.Subject, &event.CreatedAt); err != nil {
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

func (p *Postgres) RecordAudit(ctx context.Context, event model.AuditEvent) error {
	_, err := p.pool.Exec(ctx, auditInsert,
		event.ActorID, event.Action, event.TargetType, event.TargetID, event.Before, event.After, event.SubjectType, event.Subject, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	return nil
}

func (p *Postgres) CreateSession(ctx context.Context, session model.Session) error {
	// Expired sessions are never read again; drop them as new ones arrive (sessions_expiry_idx).
	if _, err := p.pool.Exec(ctx, "DELETE FROM sessions WHERE expires_at < $1", time.Now().UTC()); err != nil {
		return fmt.Errorf("remove expired sessions: %w", err)
	}
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

const auditInsert = `
	INSERT INTO audit_events(actor_id, action, target_type, target_id, before, after, subject_type, subject, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

func insertAudit(ctx context.Context, tx pgx.Tx, actorID, action, targetType, targetID, before, after string) error {
	_, err := tx.Exec(ctx, auditInsert, actorID, action, targetType, targetID, before, after, "", "", time.Now().UTC())
	if err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	return nil
}

// insertAccessAudit records a change to the Active Directory access list; the subject columns
// let the audit log show the account's or group's name.
func insertAccessAudit(ctx context.Context, tx pgx.Tx, actorID, action string, subjectType model.SubjectType, subject, before, after string) error {
	_, err := tx.Exec(ctx, auditInsert, actorID, action, "access", subject, before, after, subjectType, subject, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	return nil
}

func insertGrantAudit(ctx context.Context, tx pgx.Tx, actorID, action string, grant model.Grant, before, after string) error {
	_, err := tx.Exec(ctx, auditInsert, actorID, action, "repository", grant.RepositoryID, before, after, grant.SubjectType, grant.Subject, time.Now().UTC())
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
