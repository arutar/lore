package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/model"
)

const (
	repoA = "11111111-1111-4111-8111-111111111111"
	repoB = "22222222-2222-4222-8222-222222222222"
	repoC = "33333333-3333-4333-8333-333333333333"
)

// forEachStore runs the same assertions against the in-memory store and, when
// TEST_DATABASE_URL is set, against PostgreSQL, so the two can't drift apart unnoticed.
func forEachStore(t *testing.T, run func(t *testing.T, storage Store)) {
	t.Run("memory", func(t *testing.T) { run(t, NewMemory()) })
	t.Run("postgres", func(t *testing.T) {
		url := os.Getenv("TEST_DATABASE_URL")
		if url == "" {
			t.Skip("TEST_DATABASE_URL isn't set")
		}
		storage, err := NewPostgres(t.Context(), url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = storage.Close() })
		if _, err := storage.pool.Exec(t.Context(), "TRUNCATE users, repositories, grants, settings, audit_events, sessions, directory_access RESTART IDENTITY CASCADE"); err != nil {
			t.Fatal(err)
		}
		run(t, storage)
	})
}

func mustUser(t *testing.T, storage Store, user model.User) model.User {
	t.Helper()
	stored, err := storage.UpsertUser(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func mustRepository(t *testing.T, storage Store, repository model.Repository) {
	t.Helper()
	if _, err := storage.CreateRepository(t.Context(), "admin", repository); err != nil {
		t.Fatal(err)
	}
}

func auditActions(t *testing.T, storage Store) []model.AuditEvent {
	t.Helper()
	page, err := storage.ListAudit(t.Context(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func TestCreateRepositoryStoresVisibilityAtomically(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		mustRepository(t, storage, model.Repository{ID: repoA, Name: "public", OwnerID: "owner", Public: true})
		mustRepository(t, storage, model.Repository{ID: repoB, Name: "private", OwnerID: "owner"})
		public, _ := storage.GetRepository(t.Context(), repoA)
		private, _ := storage.GetRepository(t.Context(), repoB)
		if !public.Public || private.Public {
			t.Fatalf("public = %v, private = %v", public.Public, private.Public)
		}
		taken, err := storage.RepositoryNameTaken(t.Context(), "public", repoB)
		if err != nil || !taken {
			t.Fatalf("name taken by another repository = %v, %v", taken, err)
		}
		taken, err = storage.RepositoryNameTaken(t.Context(), "public", repoA)
		if err != nil || taken {
			t.Fatalf("a repository's own name counted as taken: %v, %v", taken, err)
		}
		// Retrying the same creation is idempotent.
		mustRepository(t, storage, model.Repository{ID: repoA, Name: "public", OwnerID: "owner", Public: true})
		if _, err := storage.CreateRepository(t.Context(), "admin", model.Repository{ID: repoC, Name: "public", OwnerID: "owner"}); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate name error = %v, want ErrConflict", err)
		}
	})
}

func TestUpdateRepositoryVisibility(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		mustRepository(t, storage, model.Repository{ID: repoA, Name: "assets", OwnerID: "owner", Public: true})
		mustRepository(t, storage, model.Repository{ID: repoB, Name: "engine", OwnerID: "owner"})

		updated, err := storage.UpdateRepository(t.Context(), "admin", model.Repository{ID: repoA, Name: "assets", Description: "new"}, nil)
		if err != nil || !updated.Public || updated.Description != "new" {
			t.Fatalf("omitted visibility must be kept: %+v, %v", updated, err)
		}

		// A failed rename must not change visibility either: one transaction.
		private := false
		if _, err := storage.UpdateRepository(t.Context(), "admin", model.Repository{ID: repoA, Name: "engine"}, &private); !errors.Is(err, ErrConflict) {
			t.Fatalf("rename onto an existing name = %v, want ErrConflict", err)
		}
		if current, _ := storage.GetRepository(t.Context(), repoA); !current.Public || current.Name != "assets" {
			t.Fatalf("failed update leaked a partial change: %+v", current)
		}

		updated, err = storage.UpdateRepository(t.Context(), "admin", model.Repository{ID: repoA, Name: "assets", Description: "new"}, &private)
		if err != nil || updated.Public {
			t.Fatalf("explicit private: %+v, %v", updated, err)
		}
		if _, err := storage.UpdateRepository(t.Context(), "admin", model.Repository{ID: repoC, Name: "missing"}, nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing repository = %v, want ErrNotFound", err)
		}
	})
}

func TestAuditIsNewestFirstAndNamesGrantSubjects(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		mustRepository(t, storage, model.Repository{ID: repoA, Name: "assets", OwnerID: "owner"})
		grant := model.Grant{RepositoryID: repoA, SubjectType: model.SubjectUser, Subject: "alice", Permission: model.PermissionRead}
		for range 2 { // the second, unchanged call must not add an event
			if err := storage.SetGrant(t.Context(), "admin", grant); err != nil {
				t.Fatal(err)
			}
		}
		grant.Permission = model.PermissionWrite
		_ = storage.SetGrant(t.Context(), "admin", grant)
		_ = storage.RemoveGrant(t.Context(), "admin", grant)
		_ = storage.RemoveGrant(t.Context(), "admin", grant)

		events := auditActions(t, storage)
		want := []struct{ action, before, after string }{
			{"grant.remove", "write", ""}, {"grant.set", "read", "write"}, {"grant.set", "", "read"}, {"repository.create", "", "assets"},
		}
		if len(events) != len(want) {
			t.Fatalf("events = %+v", events)
		}
		for index, expected := range want {
			event := events[index]
			if event.Action != expected.action || event.Before != expected.before || event.After != expected.after {
				t.Fatalf("event %d = %+v, want %+v", index, event, expected)
			}
			if index < 3 && (event.SubjectType != model.SubjectUser || event.Subject != "alice") {
				t.Fatalf("grant event %d has no subject: %+v", index, event)
			}
		}

		first, err := storage.ListAudit(t.Context(), 0, 3)
		if err != nil || len(first.Items) != 3 || first.NextCursor == "" {
			t.Fatalf("first page = %+v, %v", first, err)
		}
		second, err := storage.ListAudit(t.Context(), first.Items[2].ID, 3)
		if err != nil || len(second.Items) != 1 || second.Items[0].Action != "repository.create" || second.NextCursor != "" {
			t.Fatalf("second page = %+v, %v", second, err)
		}

		if err := storage.RecordAudit(t.Context(), model.AuditEvent{ActorID: "admin", Action: "user.create", TargetType: "user", TargetID: "bob", After: "user"}); err != nil {
			t.Fatal(err)
		}
		if newest := auditActions(t, storage)[0]; newest.Action != "user.create" || newest.TargetID != "bob" || newest.CreatedAt.IsZero() {
			t.Fatalf("recorded event = %+v", newest)
		}
	})
}

func TestUserUpdateAuditsOnlyRealChanges(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		mustUser(t, storage, model.User{ID: "bob", Username: "bob"})
		steps := []struct {
			role     model.Role
			disabled bool
		}{{model.RoleUser, false}, {model.RoleAdmin, false}, {model.RoleAdmin, true}, {model.RoleUser, false}}
		for _, step := range steps {
			if _, err := storage.UpdateUser(t.Context(), "admin", "bob", step.role, step.disabled); err != nil {
				t.Fatal(err)
			}
		}
		var actions []string
		for _, event := range auditActions(t, storage) {
			actions = append([]string{event.Action}, actions...)
		}
		want := []string{"user.update", "user.disable", "user.update", "user.enable"}
		if len(actions) != len(want) {
			t.Fatalf("actions = %v, want %v", actions, want)
		}
		for index := range want {
			if actions[index] != want[index] {
				t.Fatalf("actions = %v, want %v", actions, want)
			}
		}
	})
}

func TestCatalogAppliesGrants(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		ctx := context.Background()
		mustRepository(t, storage, model.Repository{ID: repoA, Name: "public", OwnerID: "owner", Public: true})
		mustRepository(t, storage, model.Repository{ID: repoB, Name: "team", OwnerID: "owner"})
		mustRepository(t, storage, model.Repository{ID: repoC, Name: "secret", OwnerID: "owner"})
		_ = storage.SetGrant(ctx, "admin", model.Grant{RepositoryID: repoB, SubjectType: model.SubjectGroup, Subject: "artists", Permission: model.PermissionWrite})
		names := func(user *model.User) string {
			page, err := storage.Catalog(ctx, user, "", "", 10)
			if err != nil {
				t.Fatal(err)
			}
			result := ""
			for _, repository := range page.Items {
				result += repository.Name + " "
			}
			return result
		}
		cases := []struct {
			name string
			user *model.User
			want string
		}{
			{"anonymous", nil, "public "},
			{"stranger", &model.User{ID: "eve", Role: model.RoleUser}, "public "},
			{"group member", &model.User{ID: "alice", Role: model.RoleUser, Groups: []string{"artists"}}, "public team "},
			{"owner", &model.User{ID: "owner", Role: model.RoleUser}, "public team secret "},
			{"administrator", &model.User{ID: "root", Role: model.RoleAdmin}, "public team secret "},
		}
		for _, testCase := range cases {
			if got := names(testCase.user); got != testCase.want {
				t.Errorf("%s sees %q, want %q", testCase.name, got, testCase.want)
			}
		}
		_ = storage.SetGrant(ctx, "admin", model.Grant{RepositoryID: repoC, SubjectType: model.SubjectAuthenticated, Permission: model.PermissionRead})
		if got := names(&model.User{ID: "eve", Role: model.RoleUser}); got != "public secret " {
			t.Errorf("signed-in user sees %q after an authenticated grant", got)
		}
		if got := names(nil); got != "public " {
			t.Errorf("anonymous sees %q after an authenticated grant", got)
		}
	})
}

func TestDirectoryStateAndAdministrators(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		if exists, err := storage.HasActiveAdmin(t.Context()); err != nil || exists {
			t.Fatalf("empty store has an administrator: %v, %v", exists, err)
		}
		mustUser(t, storage, model.User{ID: "alice", Username: "alice", Source: "ad", DirectoryID: "guid", Groups: []string{"g1"}})
		if _, err := storage.UpdateUser(t.Context(), "", "alice", model.RoleAdmin, false); err != nil {
			t.Fatal(err)
		}
		if exists, _ := storage.HasActiveAdmin(t.Context()); !exists {
			t.Fatal("administrator not found")
		}
		syncedAt := time.Now().UTC().Truncate(time.Microsecond)
		user, err := storage.UpdateDirectoryState(t.Context(), "alice", DirectoryState{DirectoryID: "guid", Groups: []string{"g2"}, Disabled: true, SyncedAt: syncedAt})
		if err != nil || !user.DirectoryDisabled || user.Active() || len(user.Groups) != 1 || user.Groups[0] != "g2" || user.GroupsSyncedAt == nil || !user.GroupsSyncedAt.Equal(syncedAt) {
			t.Fatalf("directory state = %+v, %v", user, err)
		}
		if user.Role != model.RoleAdmin || user.Disabled {
			t.Fatalf("directory sync changed administrator-managed fields: %+v", user)
		}
		if exists, _ := storage.HasActiveAdmin(t.Context()); exists {
			t.Fatal("an administrator disabled in the directory still counts as active")
		}
		// A routine re-sign-in keeps role and Disabled, but takes the new directory state.
		user = mustUser(t, storage, model.User{ID: "alice", Username: "alice", Source: "ad", DirectoryID: "guid", Groups: []string{"g3"}, GroupsSyncedAt: &syncedAt})
		if user.Role != model.RoleAdmin || user.DirectoryDisabled || user.Groups[0] != "g3" {
			t.Fatalf("upsert = %+v", user)
		}
		if _, err := storage.UpdateDirectoryState(t.Context(), "missing", DirectoryState{SyncedAt: syncedAt}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing user = %v", err)
		}
	})
}

func TestUpsertRetiresAFormerIdentityWithTheSameUsername(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		mustUser(t, storage, model.User{ID: "old", Username: "carol"})
		mustRepository(t, storage, model.Repository{ID: repoA, Name: "assets", OwnerID: "owner"})
		_ = storage.SetGrant(t.Context(), "admin", model.Grant{RepositoryID: repoA, SubjectType: model.SubjectUser, Subject: "old", Permission: model.PermissionWrite})
		fresh := mustUser(t, storage, model.User{ID: "new", Username: "carol"})
		if fresh.ID != "new" || fresh.Username != "carol" || fresh.Disabled {
			t.Fatalf("new identity = %+v", fresh)
		}
		former, err := storage.GetUser(t.Context(), "old")
		if err != nil || former.Disabled || former.Username != "carol~old" {
			t.Fatalf("former identity = %+v, %v", former, err)
		}
		grants, _ := storage.ListGrants(t.Context(), repoA)
		for _, grant := range grants {
			if grant.Subject == "new" {
				t.Fatal("grant moved to the new identity")
			}
		}
		if newest := auditActions(t, storage)[0]; newest.Action != "user.supersede" || newest.TargetID != "old" || newest.After != "new" {
			t.Fatalf("supersede wasn't audited: %+v", newest)
		}
	})
}

func TestSearchAndLookups(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		mustRepository(t, storage, model.Repository{ID: repoA, Name: "aurora/game-content", Description: "Уровни и персонажи", OwnerID: "owner", Public: true})
		mustRepository(t, storage, model.Repository{ID: repoB, Name: "marketing/press-kit", Description: "100% press", OwnerID: "owner"})
		page, err := storage.ListRepositories(t.Context(), "ПЕРСОНАЖ", "", 10)
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != repoA {
			t.Fatalf("search by Cyrillic description = %+v, %v", page, err)
		}
		if page, _ = storage.ListRepositories(t.Context(), "%", "", 10); len(page.Items) != 1 || page.Items[0].ID != repoB {
			t.Fatalf("a percent sign must match literally: %+v", page.Items)
		}
		if page, _ = storage.ListRepositories(t.Context(), "_", "", 10); len(page.Items) != 0 {
			t.Fatalf("an underscore must match literally: %+v", page.Items)
		}
		if page, _ = storage.ListRepositories(t.Context(), `\`, "", 10); len(page.Items) != 0 {
			t.Fatalf("a backslash must match literally: %+v", page.Items)
		}
		if page, _ = storage.Catalog(t.Context(), nil, "press", "", 10); len(page.Items) != 0 {
			t.Fatalf("anonymous search found a private repository: %+v", page.Items)
		}
		if page, _ = storage.Catalog(t.Context(), nil, "AURORA", "", 10); len(page.Items) != 1 {
			t.Fatalf("anonymous search for a public repository = %+v", page.Items)
		}
		repository, err := storage.GetRepositoryByName(t.Context(), "aurora/game-content")
		if err != nil || repository.ID != repoA || !repository.Public {
			t.Fatalf("by name = %+v, %v", repository, err)
		}
		if _, err := storage.GetRepositoryByName(t.Context(), "aurora"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("partial name = %v", err)
		}

		mustUser(t, storage, model.User{ID: "u1", Username: "a.vorontsova", DisplayName: "Анна Воронцова", Email: "anna@studio.example", Groups: []string{"artists"}})
		mustUser(t, storage, model.User{ID: "u2", Username: "olivia@northwind.example", DisplayName: "Olivia Bennett", Email: "olivia@northwind.example"})
		for query, want := range map[string]string{"анна": "u1", "NORTHWIND": "u2", "vorontsova": "u1"} {
			users, err := storage.ListUsers(t.Context(), query, "", 10)
			if err != nil || len(users.Items) != 1 || users.Items[0].ID != want {
				t.Fatalf("user search %q = %+v, %v", query, users.Items, err)
			}
		}
		if members, _ := storage.GroupHasMembers(t.Context(), "artists"); !members {
			t.Fatal("group members not found")
		}
		if members, _ := storage.GroupHasMembers(t.Context(), "nobody"); members {
			t.Fatal("empty group has members")
		}

		settings, _ := storage.GetSettings(t.Context())
		if settings.Language != model.DefaultLanguage {
			t.Fatalf("default language = %q", settings.Language)
		}
		settings.Language = "ru"
		if err := storage.UpdateSettings(t.Context(), "admin", settings); err != nil {
			t.Fatal(err)
		}
		if settings, _ = storage.GetSettings(t.Context()); settings.Language != "ru" {
			t.Fatalf("language = %q", settings.Language)
		}
	})
}

func TestCatalogSearchPagesThroughGroupGrants(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		ids := []string{repoA, repoB, repoC}
		for index, id := range ids {
			mustRepository(t, storage, model.Repository{ID: id, Name: fmt.Sprintf("art/level-%d", index), OwnerID: "owner"})
			if err := storage.SetGrant(t.Context(), "owner", model.Grant{RepositoryID: id, SubjectType: model.SubjectGroup, Subject: "artists", Permission: model.PermissionRead}); err != nil {
				t.Fatal(err)
			}
		}
		mustRepository(t, storage, model.Repository{ID: "44444444-4444-4444-8444-444444444444", Name: "art/level-secret", OwnerID: "owner"})
		user := &model.User{ID: "u-art", Username: "art", Role: model.RoleUser, Groups: []string{"artists"}}
		var seen []string
		cursor := ""
		for {
			page, err := storage.Catalog(t.Context(), user, "LEVEL", cursor, 2)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range page.Items {
				seen = append(seen, item.ID)
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if len(seen) != 3 || seen[0] != repoA || seen[2] != repoC {
			t.Fatalf("paged group catalog = %v", seen)
		}
	})
}

func TestExpiredSessionsAreRemoved(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		mustUser(t, storage, model.User{ID: "u-s", Username: "s"})
		past := time.Now().UTC().Add(-time.Hour)
		if err := storage.CreateSession(t.Context(), model.Session{IDHash: "old", CSRFToken: "x", UserID: "u-s", ExpiresAt: past}); err != nil {
			t.Fatal(err)
		}
		if err := storage.CreateSession(t.Context(), model.Session{IDHash: "new", CSRFToken: "x", UserID: "u-s", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.GetSession(t.Context(), "new", time.Now().UTC()); err != nil {
			t.Fatalf("live session = %v", err)
		}
		var remaining int
		switch s := storage.(type) {
		case *Postgres:
			if err := s.pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions WHERE id_hash = 'old'").Scan(&remaining); err != nil {
				t.Fatal(err)
			}
		case *Memory:
			s.mu.RLock()
			_, ok := s.sessions["old"]
			s.mu.RUnlock()
			if ok {
				remaining = 1
			}
		}
		if remaining != 0 {
			t.Fatal("an expired session was kept")
		}
	})
}

func TestPostgresPlansEachQueryForItsValues(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL isn't set")
	}
	storage, err := NewPostgres(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	var mode string
	if err := storage.pool.QueryRow(t.Context(), "SHOW plan_cache_mode").Scan(&mode); err != nil || mode != "force_custom_plan" {
		t.Fatalf("plan_cache_mode = %q, %v; search indexes would be skipped by generic plans", mode, err)
	}
}

func TestAccessListAndDeniedAccounts(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		const group = "aaaaaaaa-0000-4000-8000-000000000001"
		const account = "bbbbbbbb-0000-4000-8000-000000000002"
		ctx := t.Context()
		if err := storage.AddAccess(ctx, "admin", model.AccessEntry{SubjectType: model.SubjectUser, Subject: account, Name: "Zed"}); err != nil {
			t.Fatal(err)
		}
		if err := storage.AddAccess(ctx, "admin", model.AccessEntry{SubjectType: model.SubjectGroup, Subject: group, Name: "Artists", Detail: "CN=Artists,DC=example,DC=org"}); err != nil {
			t.Fatal(err)
		}
		// Adding it again with the same name changes nothing and isn't audited twice.
		if err := storage.AddAccess(ctx, "admin", model.AccessEntry{SubjectType: model.SubjectGroup, Subject: group, Name: "Artists", Detail: "CN=Artists,DC=example,DC=org"}); err != nil {
			t.Fatal(err)
		}
		entries, err := storage.ListAccess(ctx)
		if err != nil || len(entries) != 2 || entries[0].Subject != group || entries[1].Name != "Zed" || entries[0].CreatedAt.IsZero() {
			t.Fatalf("access list = %+v %v", entries, err)
		}
		if entries[0].Role != model.RoleUser || entries[0].Disabled {
			t.Fatalf("a new entry should be an enabled user entry: %+v", entries[0])
		}
		updated, err := storage.UpdateAccess(ctx, "admin", model.SubjectGroup, group, model.RoleAdmin, true)
		if err != nil || updated.Role != model.RoleAdmin || !updated.Disabled {
			t.Fatalf("UpdateAccess = %+v %v", updated, err)
		}
		if _, err := storage.UpdateAccess(ctx, "admin", model.SubjectGroup, group, model.RoleAdmin, false); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.UpdateAccess(ctx, "admin", model.SubjectUser, "missing", model.RoleUser, false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("updating a missing entry = %v", err)
		}
		// Adding an existing entry again keeps its role.
		if err := storage.AddAccess(ctx, "admin", model.AccessEntry{SubjectType: model.SubjectGroup, Subject: group, Name: "Artists", Detail: "CN=Artists,DC=example,DC=org"}); err != nil {
			t.Fatal(err)
		}
		if entries, _ = storage.ListAccess(ctx); entries[0].Role != model.RoleAdmin || entries[0].Disabled {
			t.Fatalf("re-adding changed the entry: %+v", entries[0])
		}
		if err := storage.RemoveAccess(ctx, "admin", model.SubjectUser, account); err != nil {
			t.Fatal(err)
		}
		if err := storage.RemoveAccess(ctx, "admin", model.SubjectUser, account); !errors.Is(err, ErrNotFound) {
			t.Fatalf("removing a missing entry = %v", err)
		}
		audit, _ := storage.ListAudit(ctx, 0, 10)
		if len(audit.Items) != 5 || audit.Items[0].Action != "access.remove" || audit.Items[0].Before != "Zed" || audit.Items[0].Subject != account {
			t.Fatalf("access audit = %+v", audit.Items)
		}

		// A denied administrator doesn't count as an active one.
		mustUser(t, storage, model.User{ID: "ad-admin", Username: "ad-admin", Source: "ad", DirectoryID: account, AccessDenied: true})
		if _, err := storage.UpdateUser(ctx, "", "ad-admin", model.RoleAdmin, false); err != nil {
			t.Fatal(err)
		}
		if active, _ := storage.HasActiveAdmin(ctx); active {
			t.Fatal("a denied administrator counted as active")
		}
		user, err := storage.ApplyDirectoryAccess(ctx, "ad-admin", false, model.RoleAdmin)
		if err != nil || user.AccessDenied || user.Role != model.RoleAdmin {
			t.Fatalf("ApplyDirectoryAccess = %+v %v", user, err)
		}
		if active, _ := storage.HasActiveAdmin(ctx); !active {
			t.Fatal("an admitted administrator didn't count as active")
		}
		if stored, _ := storage.GetUser(ctx, "ad-admin"); stored.AccessDenied {
			t.Fatal("the flag wasn't stored")
		}
	})
}

func TestDeletedAccountsAreHiddenAndLoseTheirGrants(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		ctx := t.Context()
		mustUser(t, storage, model.User{ID: "tom", Username: "tom@example.com", Source: "local"})
		mustRepository(t, storage, model.Repository{ID: repoA, Name: "assets", OwnerID: "owner"})
		if err := storage.SetGrant(ctx, "owner", model.Grant{RepositoryID: repoA, SubjectType: model.SubjectUser, Subject: "tom", Permission: model.PermissionRead}); err != nil {
			t.Fatal(err)
		}
		if err := storage.DeleteUser(ctx, "admin", "tom"); err != nil {
			t.Fatal(err)
		}
		if err := storage.DeleteUser(ctx, "admin", "tom"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete = %v", err)
		}
		page, _ := storage.ListUsers(ctx, "", "", 10)
		for _, user := range page.Items {
			if user.ID == "tom" {
				t.Fatal("deleted account is listed")
			}
		}
		user, err := storage.GetUser(ctx, "tom")
		if err != nil || !user.Deleted || user.Username != "tom@example.com~tom" {
			t.Fatalf("deleted record = %+v %v", user, err)
		}
		grants, _ := storage.ListGrants(ctx, repoA)
		for _, grant := range grants {
			if grant.Subject == "tom" {
				t.Fatal("grant survived the deletion")
			}
		}
		// The name is free for a new account.
		mustUser(t, storage, model.User{ID: "tom-2", Username: "tom@example.com", Source: "local"})
	})
}

