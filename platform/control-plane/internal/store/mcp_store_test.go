package store

import (
	"errors"
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
