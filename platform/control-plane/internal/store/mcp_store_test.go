package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/model"
)

func TestMCPTokensLinksAndSwitches(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		ctx := t.Context()
		mustUser(t, storage, model.User{ID: "alice", Username: "alice"})
		mustUser(t, storage, model.User{ID: "bob", Username: "bob"})

		user, err := storage.SetUserMCP(ctx, "admin", "alice", true)
		if err != nil || !user.MCPDisabled {
			t.Fatalf("switch off: %+v, %v", user, err)
		}
		if stored, _ := storage.GetUser(ctx, "alice"); !stored.MCPDisabled {
			t.Fatal("the switch wasn't stored")
		}
		// Signing in again keeps the switch.
		if stored := mustUser(t, storage, model.User{ID: "alice", Username: "alice", Email: "a@example"}); !stored.MCPDisabled {
			t.Fatal("an update of the account reset the switch")
		}
		if _, err := storage.SetUserMCP(ctx, "admin", "missing", true); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown account: %v", err)
		}

		if err := storage.AddAccess(ctx, "admin", model.AccessEntry{SubjectType: model.SubjectGroup, Subject: "artists", Name: "Artists", Role: model.RoleUser}); err != nil {
			t.Fatal(err)
		}
		entry, err := storage.SetAccessMCP(ctx, "admin", model.SubjectGroup, "artists", true)
		if err != nil || !entry.MCPDisabled {
			t.Fatalf("group switch: %+v, %v", entry, err)
		}
		entries, _ := storage.ListAccess(ctx)
		if len(entries) != 1 || !entries[0].MCPDisabled {
			t.Fatalf("access list = %+v", entries)
		}
		if entry, _ := storage.UpdateAccess(ctx, "admin", model.SubjectGroup, "artists", model.RoleAdmin, false); !entry.MCPDisabled {
			t.Fatal("changing the role reset the MCP switch")
		}

		created := time.Now().UTC().Truncate(time.Microsecond)
		token := model.MCPToken{ID: "t1", UserID: "bob", Name: "agent", Hint: "lore_mcp_abcd", Repositories: []string{repoA}, CreatedAt: created}
		if err := storage.CreateMCPToken(ctx, token, "hash-1"); err != nil {
			t.Fatal(err)
		}
		if err := storage.CreateMCPToken(ctx, model.MCPToken{ID: "t2", UserID: "alice", Name: "other", CreatedAt: created.Add(time.Second)}, "hash-2"); err != nil {
			t.Fatal(err)
		}
		if err := storage.CreateMCPToken(ctx, model.MCPToken{ID: "t3", UserID: "alice", Name: "dup", CreatedAt: created}, "hash-1"); !errors.Is(err, ErrConflict) {
			t.Fatalf("a second token with the same hash: %v", err)
		}
		found, err := storage.GetMCPTokenByHash(ctx, "hash-1")
		if err != nil || found.ID != "t1" || len(found.Repositories) != 1 || found.Repositories[0] != repoA || found.ExpiresAt != nil {
			t.Fatalf("by hash = %+v, %v", found, err)
		}
		if mine, _ := storage.ListMCPTokens(ctx, "bob"); len(mine) != 1 || mine[0].ID != "t1" {
			t.Fatalf("bob's tokens = %+v", mine)
		}
		if all, _ := storage.ListMCPTokens(ctx, ""); len(all) != 2 || all[0].ID != "t2" {
			t.Fatalf("every token, newest first = %+v", all)
		}
		used := created.Add(time.Minute)
		if err := storage.TouchMCPToken(ctx, "t1", used); err != nil {
			t.Fatal(err)
		}
		if found, _ := storage.GetMCPToken(ctx, "t1"); found.LastUsedAt == nil || !found.LastUsedAt.Equal(used) {
			t.Fatalf("last use = %v", found.LastUsedAt)
		}

		link := model.MCPLink{TokenID: "t1", UserID: "bob", RepositoryID: repoA, Path: "a/b.txt", Revision: "abc", ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)}
		if err := storage.CreateMCPLink(ctx, "link-1", link); err != nil {
			t.Fatal(err)
		}
		got, err := storage.GetMCPLink(ctx, "link-1", time.Now())
		if err != nil || !got.ExpiresAt.Equal(link.ExpiresAt) {
			t.Fatalf("link = %+v, %v", got, err)
		}
		got.ExpiresAt = link.ExpiresAt
		if got != link {
			t.Fatalf("link = %+v, %v", got, err)
		}
		if _, err := storage.GetMCPLink(ctx, "link-1", time.Now().Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expired link: %v", err)
		}

		if err := storage.RevokeMCPToken(ctx, "admin", "t1"); err != nil {
			t.Fatal(err)
		}
		if err := storage.RevokeMCPToken(ctx, "admin", "t1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("revoking twice: %v", err)
		}
		if revoked, _ := storage.GetMCPToken(ctx, "t1"); revoked.RevokedAt == nil || revoked.Usable(time.Now()) {
			t.Fatalf("revoked token = %+v", revoked)
		}
		if _, err := storage.GetMCPLink(ctx, "link-1", time.Now()); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a revoked token's link still works: %v", err)
		}
		if mine, _ := storage.ListMCPTokens(ctx, "bob"); len(mine) != 0 {
			t.Fatalf("revoked tokens are listed: %+v", mine)
		}

		actions := map[string]bool{}
		for _, event := range auditActions(t, storage) {
			actions[event.Action] = true
		}
		for _, action := range []string{"user.mcp", "access.mcp", "mcp.token.create", "mcp.token.revoke"} {
			if !actions[action] {
				t.Errorf("audit has no %s", action)
			}
		}
	})
}

func TestDeletingAnAccountRevokesItsMCPTokens(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		ctx := t.Context()
		mustUser(t, storage, model.User{ID: "gone", Username: "gone"})
		mustUser(t, storage, model.User{ID: "stays", Username: "stays"})
		now := time.Now().UTC().Truncate(time.Microsecond)
		for _, token := range []model.MCPToken{{ID: "g1", UserID: "gone", Name: "a", CreatedAt: now}, {ID: "s1", UserID: "stays", Name: "b", CreatedAt: now}} {
			if err := storage.CreateMCPToken(ctx, token, "hash-"+token.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := storage.CreateMCPLink(ctx, "link", model.MCPLink{TokenID: "g1", UserID: "gone", RepositoryID: repoA, Path: "f", Revision: "r", ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if err := storage.DeleteUser(ctx, "admin", "gone"); err != nil {
			t.Fatal(err)
		}
		if token, _ := storage.GetMCPToken(ctx, "g1"); token.RevokedAt == nil {
			t.Fatal("a deleted account's token wasn't revoked")
		}
		if token, _ := storage.GetMCPToken(ctx, "s1"); token.RevokedAt != nil {
			t.Fatal("another account's token was revoked")
		}
		if _, err := storage.GetMCPLink(ctx, "link", time.Now()); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a deleted account's link still works: %v", err)
		}
		page, err := storage.SearchMCPTokens(ctx, "", "", 10)
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != "s1" || page.Items[0].UserLogin != "stays" {
			t.Fatalf("search = %+v, %v", page, err)
		}
	})
}

func TestSearchMCPTokensFindsByTokenAndOwnerAndPages(t *testing.T) {
	forEachStore(t, func(t *testing.T, storage Store) {
		ctx := t.Context()
		mustUser(t, storage, model.User{ID: "u1", Username: "mlopez", DisplayName: "Maria Lopez", Email: "maria@example.com"})
		mustUser(t, storage, model.User{ID: "u2", Username: "kchen", DisplayName: "Kim Chen", Email: "kim@example.com"})
		base := time.Now().UTC().Truncate(time.Microsecond)
		tokens := []model.MCPToken{
			{ID: "a", UserID: "u1", Name: "Claude Code", CreatedAt: base},
			{ID: "b", UserID: "u2", Name: "Cursor 100%", CreatedAt: base.Add(time.Second)},
			{ID: "c", UserID: "u1", Name: "nightly_job", CreatedAt: base.Add(2 * time.Second)},
			{ID: "d", UserID: "u2", Name: "Hermes", CreatedAt: base.Add(2 * time.Second)},
		}
		for _, token := range tokens {
			if err := storage.CreateMCPToken(ctx, token, "hash-"+token.ID); err != nil {
				t.Fatal(err)
			}
		}
		ids := func(page model.Page[model.MCPToken]) string {
			var out []string
			for _, token := range page.Items {
				out = append(out, token.ID)
			}
			return strings.Join(out, ",")
		}
		for query, want := range map[string]string{
			"": "d,c,b,a", "LOPEZ": "c,a", "kim@": "d,b", "claude": "a", "100%": "b", "_job": "c", "%": "b", "nobody": "",
		} {
			page, err := storage.SearchMCPTokens(ctx, query, "", 10)
			if err != nil || ids(page) != want {
				t.Errorf("search %q = %q, %v, want %q", query, ids(page), err, want)
			}
		}
		var seen []string
		cursor := ""
		for range 5 {
			page, err := storage.SearchMCPTokens(ctx, "", cursor, 3)
			if err != nil {
				t.Fatal(err)
			}
			seen = append(seen, ids(page))
			if cursor = page.NextCursor; cursor == "" {
				break
			}
		}
		if strings.Join(seen, "|") != "d,c,b|a" {
			t.Fatalf("pages = %v", seen)
		}
		if _, err := storage.SearchMCPTokens(ctx, "", "not-a-cursor", 3); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("bad cursor: %v", err)
		}
	})
}
