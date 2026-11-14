package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/directory"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

// withoutAliceEntry starts from an access list that doesn't name Alice personally.
func withoutAliceEntry(t *testing.T, f *identityFixture) {
	t.Helper()
	if err := f.storage.RemoveAccess(t.Context(), "", model.SubjectUser, aliceGUID); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryAccountWithoutAccessCantSignIn(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{})
	withoutAliceEntry(t, f)
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}, Groups: []string{artists}})

	if _, err := f.auth.authenticateLogin(t.Context(), f.alice()); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("sign-in without access = %v, want ErrAccountInactive", err)
	}
	stored, err := f.storage.GetUser(t.Context(), "alice-id")
	if err != nil || !stored.AccessDenied || stored.Active() {
		t.Fatalf("account wasn't stored as denied: %+v %v", stored, err)
	}
}

func TestGroupEntryAdmitsMembersAndRemovingItClosesTheirAccess(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{})
	withoutAliceEntry(t, f)
	// The directory reports nested groups too: Alice is in Artists through another group.
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}, Groups: []string{"bbbbbbbb-0000-4000-8000-000000000002", artists}})
	if err := f.storage.AddAccess(t.Context(), "admin-id", model.AccessEntry{SubjectType: model.SubjectGroup, Subject: artists, Name: "Artists"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.authenticateLogin(t.Context(), f.alice()); err != nil {
		t.Fatalf("group member sign-in: %v", err)
	}

	if err := f.storage.RemoveAccess(t.Context(), "admin-id", model.SubjectGroup, artists); err != nil {
		t.Fatal(err)
	}
	if err := f.auth.RecheckAccess(t.Context()); err != nil {
		t.Fatal(err)
	}
	stored, _ := f.storage.GetUser(t.Context(), "alice-id")
	if !stored.AccessDenied {
		t.Fatalf("removing the group didn't deny its member: %+v", stored)
	}
	if _, err := f.auth.authenticateLogin(t.Context(), f.alice()); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("sign-in after the group was removed = %v", err)
	}
	events, _ := f.storage.ListAudit(t.Context(), 0, 10)
	if events.Items[0].Action != "user.access_deny" || events.Items[1].Action != "access.remove" || events.Items[1].Subject != artists {
		t.Fatalf("access changes weren't audited: %+v", events.Items[:2])
	}
}

func TestLeavingTheGroupInTheDirectoryDeniesAccessAtTheNextRefresh(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{DirectoryRefresh: time.Nanosecond})
	withoutAliceEntry(t, f)
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}, Groups: []string{artists}})
	if err := f.storage.AddAccess(t.Context(), "", model.AccessEntry{SubjectType: model.SubjectGroup, Subject: artists, Name: "Artists"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.authenticateLogin(t.Context(), f.alice()); err != nil {
		t.Fatalf("group member sign-in: %v", err)
	}
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}})
	if _, err := f.auth.authenticateLogin(t.Context(), f.alice()); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("sign-in after leaving the group = %v", err)
	}
	// Back in the group: admitted again without an administrator.
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}, Groups: []string{artists}})
	if _, err := f.auth.authenticateLogin(t.Context(), f.alice()); err != nil {
		t.Fatalf("sign-in after rejoining the group: %v", err)
	}
}

func TestLocalAccountsDontNeedAnAccessEntry(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{})
	withoutAliceEntry(t, f)
	user, err := f.auth.authenticateLogin(t.Context(), f.token(map[string]any{"sub": "bob-id", "preferred_username": "bob"}))
	if err != nil || user.AccessDenied || user.Source != SourceLocal {
		t.Fatalf("local account = %+v %v", user, err)
	}
}

func TestAdmitsMatchesAccountOrAnyGroupIgnoringCase(t *testing.T) {
	entries := []model.AccessEntry{
		{SubjectType: model.SubjectGroup, Subject: "AAAAAAAA-0000-4000-8000-000000000001"},
		{SubjectType: model.SubjectUser, Subject: aliceGUID},
	}
	cases := []struct {
		account string
		groups  []string
		want    bool
	}{
		{"cccccccc-0000-4000-8000-000000000003", []string{artists}, true},
		{aliceGUID, nil, true},
		{"cccccccc-0000-4000-8000-000000000003", []string{"dddddddd-0000-4000-8000-000000000004"}, false},
		{"", nil, false},
	}
	for _, c := range cases {
		if got := model.Admits(entries, c.account, c.groups); got != c.want {
			t.Errorf("Admits(%s, %v) = %v, want %v", c.account, c.groups, got, c.want)
		}
	}
	if model.Admits(nil, aliceGUID, []string{artists}) {
		t.Error("an empty list admitted an account")
	}
}

func TestAdministratorManagesTheAccessList(t *testing.T) {
	storage := store.NewMemory()
	auth, err := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}, Directory: fakeSubjectDirectory{},
	})}
	client.login()
	// Elessar signed in before anyone gave him access.
	storage.UpsertUser(t.Context(), model.User{ID: "elessar-id", Username: "elessar", Source: SourceDirectory, DirectoryID: elessarGUID, AccessDenied: true})

	status, found := client.request(http.MethodGet, "/web/v1/admin/access/subjects?q=el", nil, false)
	if status != http.StatusOK || !strings.Contains(fmt.Sprintf("%v", found["items"]), "Elessar") {
		t.Fatalf("subject search = %d %v", status, found)
	}
	status, list := client.request(http.MethodPost, "/web/v1/admin/access", map[string]any{"subjectType": "user", "subject": strings.ToUpper(elessarGUID)}, true)
	if status != http.StatusOK || !strings.Contains(fmt.Sprintf("%v", list["items"]), "elessar") {
		t.Fatalf("add = %d %v", status, list)
	}
	if user, _ := storage.GetUser(t.Context(), "elessar-id"); user.AccessDenied {
		t.Fatal("adding the account didn't admit it")
	}
	if status, _ := client.request(http.MethodPost, "/web/v1/admin/access", map[string]any{"subjectType": "user", "subject": "not-a-guid"}, true); status != http.StatusBadRequest {
		t.Fatalf("invalid subject = %d", status)
	}
	if status, _ := client.request(http.MethodPost, "/web/v1/admin/access", map[string]any{"subjectType": "anonymous", "subject": elessarGUID}, true); status != http.StatusBadRequest {
		t.Fatalf("invalid subject type = %d", status)
	}

	if status, _ := client.request(http.MethodDelete, "/web/v1/admin/access/user/"+elessarGUID, nil, true); status != http.StatusNoContent {
		t.Fatalf("remove = %d", status)
	}
	if user, _ := storage.GetUser(t.Context(), "elessar-id"); !user.AccessDenied {
		t.Fatal("removing the entry didn't deny the account")
	}
	status, audit := client.request(http.MethodGet, "/web/v1/admin/audit", nil, false)
	text := fmt.Sprintf("%v", audit["items"])
	if status != http.StatusOK || !strings.Contains(text, "access.add") || !strings.Contains(text, "access.remove") {
		t.Fatalf("audit = %d %v", status, text)
	}
}

func TestRefusedSignInAlsoEndsTheIdentityProviderSession(t *testing.T) {
	a := &Authenticator{endSessionEndpoint: "https://idp.test/realms/lore/protocol/openid-connect/logout",
		config: AuthConfig{ClientID: "lore-web", PostLogoutRedirectURL: "http://lore.test:8080"}}
	target := a.refusedSignInURL("raw-id-token", "no_access")
	for _, part := range []string{"id_token_hint=raw-id-token", "client_id=lore-web", "post_logout_redirect_uri=http%3A%2F%2Flore.test%3A8080%2F", "state=signin%3Ano_access"} {
		if !strings.Contains(target, part) {
			t.Fatalf("logout URL %q lacks %s", target, part)
		}
	}
	// Without an end-session endpoint the browser still gets the explanation.
	if got := (&Authenticator{}).refusedSignInURL("raw-id-token", "disabled"); got != "/?signin=disabled" {
		t.Fatalf("fallback = %q", got)
	}
}

func TestGroupRoleAndPersonalEntryDecideADirectoryAccount(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{DirectoryRefresh: time.Nanosecond})
	withoutAliceEntry(t, f)
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}, Groups: []string{artists}})
	f.storage.AddAccess(t.Context(), "", model.AccessEntry{SubjectType: model.SubjectGroup, Subject: artists, Name: "Lore-Admins", Role: model.RoleAdmin})
	user, err := f.auth.authenticateLogin(t.Context(), f.alice())
	if err != nil || user.Role != model.RoleAdmin {
		t.Fatalf("member of an administrator group = %+v %v", user, err)
	}

	// A personal entry switched off shuts this one member out, whatever the group says.
	f.storage.AddAccess(t.Context(), "", model.AccessEntry{SubjectType: model.SubjectUser, Subject: aliceGUID, Name: "alice", Disabled: true})
	if err := f.auth.RecheckAccess(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.authenticateLogin(t.Context(), f.alice()); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("personally disabled member = %v", err)
	}

	// Switched on again with the user role: the group still makes her an administrator.
	f.storage.UpdateAccess(t.Context(), "", model.SubjectUser, aliceGUID, model.RoleUser, false)
	f.auth.RecheckAccess(t.Context())
	if user, err := f.auth.authenticateLogin(t.Context(), f.alice()); err != nil || user.Role != model.RoleAdmin {
		t.Fatalf("highest role should win: %+v %v", user, err)
	}

	// The group demoted: back to a plain user.
	f.storage.UpdateAccess(t.Context(), "", model.SubjectGroup, artists, model.RoleUser, false)
	f.auth.RecheckAccess(t.Context())
	if user, _ := f.storage.GetUser(t.Context(), "alice-id"); user.Role != model.RoleUser || user.AccessDenied {
		t.Fatalf("after demoting the group: %+v", user)
	}
}

func TestDirectoryAdministratorCantLockThemselvesOut(t *testing.T) {
	const adminGroup = "cccccccc-0000-4000-8000-000000000003"
	storage := store.NewMemory()
	auth, err := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	storage.AddAccess(t.Context(), "", model.AccessEntry{SubjectType: model.SubjectGroup, Subject: adminGroup, Name: "Lore-Admins", Role: model.RoleAdmin})
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, Directory: fakeSubjectDirectory{},
		DevUser: model.User{ID: "boss", Username: "boss", Role: model.RoleAdmin, Source: SourceDirectory, DirectoryID: elessarGUID, Groups: []string{adminGroup}},
	})}
	client.login()
	if status, _ := client.request(http.MethodPatch, "/web/v1/admin/access/group/"+adminGroup, map[string]any{"role": "user", "disabled": false}, true); status != http.StatusConflict {
		t.Fatalf("demoting one's own administrator group = %d", status)
	}
	if status, _ := client.request(http.MethodDelete, "/web/v1/admin/access/group/"+adminGroup, nil, true); status != http.StatusConflict {
		t.Fatalf("removing one's own administrator group = %d", status)
	}
	// With a personal administrator entry the group can go.
	if status, _ := client.request(http.MethodPost, "/web/v1/admin/access", map[string]any{"subjectType": "user", "subject": elessarGUID, "role": "admin"}, true); status != http.StatusOK {
		t.Fatalf("personal administrator entry = %d", status)
	}
	if status, _ := client.request(http.MethodDelete, "/web/v1/admin/access/group/"+adminGroup, nil, true); status != http.StatusNoContent {
		t.Fatalf("removing the group once a personal entry exists = %d", status)
	}
}

func TestMembersListAndGrantsOnlyForTheList(t *testing.T) {
	const animators = "dddddddd-0000-4000-8000-000000000004"
	storage := store.NewMemory()
	auth, err := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}, Directory: fakeSubjectDirectory{},
	})}
	client.login()
	storage.AddAccess(t.Context(), "", model.AccessEntry{SubjectType: model.SubjectGroup, Subject: artists, Name: "Artists"})
	storage.UpsertUser(t.Context(), model.User{ID: "maria", Username: "mlopez", Source: SourceDirectory, DirectoryID: "eeeeeeee-0000-4000-8000-000000000005", Groups: []string{artists}})
	storage.UpsertUser(t.Context(), model.User{ID: "zed", Username: "zed", Source: SourceDirectory, DirectoryID: "ffffffff-0000-4000-8000-000000000006", AccessDenied: true})
	storage.UpsertUser(t.Context(), model.User{ID: "tom", Username: "tom@example.com", Source: SourceLocal})

	status, members := client.request(http.MethodGet, "/web/v1/admin/members", nil, false)
	text := fmt.Sprintf("%v", members)
	if status != http.StatusOK || !strings.Contains(fmt.Sprintf("%v", members["groups"]), "mlopez") || strings.Contains(text, "zed") || !strings.Contains(fmt.Sprintf("%v", members["local"]), "tom@example.com") {
		t.Fatalf("members = %d %s", status, text)
	}

	status, created := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "assets"}, true)
	if status != http.StatusCreated {
		t.Fatalf("create = %d", status)
	}
	grants := "/web/v1/repositories/" + created["id"].(string) + "/grants/"
	if status, _ := client.request(http.MethodPut, grants, map[string]any{"subjectType": "group", "subject": artists, "permission": "write"}, true); status != http.StatusOK {
		t.Fatalf("grant to a listed group = %d", status)
	}
	if status, _ := client.request(http.MethodPut, grants, map[string]any{"subjectType": "group", "subject": animators, "permission": "read"}, true); status != http.StatusBadRequest {
		t.Fatalf("grant to a group that isn't on the list = %d", status)
	}
	if status, _ := client.request(http.MethodPut, grants, map[string]any{"subjectType": "user", "subject": "zed", "permission": "read"}, true); status != http.StatusBadRequest {
		t.Fatalf("grant to an account without access = %d", status)
	}
	if status, _ := client.request(http.MethodPut, grants, map[string]any{"subjectType": "user", "subject": "maria", "permission": "write"}, true); status != http.StatusOK {
		t.Fatalf("grant to a member of a listed group = %d", status)
	}
	// Directory accounts are changed through the list, not through the account.
	if status, _ := client.request(http.MethodPatch, "/web/v1/admin/users/maria", map[string]any{"role": "admin", "disabled": false}, true); status != http.StatusConflict {
		t.Fatalf("editing a directory account directly = %d", status)
	}
}

func TestDeletingALocalAccount(t *testing.T) {
	storage := store.NewMemory()
	auth, err := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour, BootstrapAdmin: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	accounts := &fakeAccountManager{}
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "boss", Username: "boss", Role: model.RoleAdmin, Source: SourceLocal}, Accounts: accounts,
	})}
	client.login()
	storage.UpsertUser(t.Context(), model.User{ID: "tom", Username: "tom@example.com", Source: SourceLocal})
	storage.UpsertUser(t.Context(), model.User{ID: "builtin", Username: "admin", Source: SourceLocal})
	storage.UpsertUser(t.Context(), model.User{ID: "maria", Username: "mlopez", Source: SourceDirectory, DirectoryID: aliceGUID})
	status, created := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "assets"}, true)
	if status != http.StatusCreated {
		t.Fatalf("create = %d", status)
	}
	storage.SetGrant(t.Context(), "boss", model.Grant{RepositoryID: created["id"].(string), SubjectType: model.SubjectUser, Subject: "tom", Permission: model.PermissionWrite})

	for _, refused := range []string{"boss", "builtin", "maria"} {
		if status, _ := client.request(http.MethodDelete, "/web/v1/admin/users/"+refused, nil, true); status != http.StatusConflict {
			t.Fatalf("deleting %s = %d, want 409", refused, status)
		}
	}
	if status, _ := client.request(http.MethodDelete, "/web/v1/admin/users/tom", nil, true); status != http.StatusNoContent {
		t.Fatalf("delete = %d", status)
	}
	if accounts.deletedID != "tom" {
		t.Fatalf("the identity provider account wasn't deleted: %q", accounts.deletedID)
	}
	page, _ := storage.ListUsers(t.Context(), "tom", "", 10)
	grants, _ := storage.ListGrants(t.Context(), created["id"].(string))
	for _, grant := range grants {
		if grant.Subject == "tom" {
			t.Fatal("the deleted account kept its grant")
		}
	}
	stored, _ := storage.GetUser(t.Context(), "tom")
	if len(page.Items) != 0 || !stored.Deleted || stored.Active() {
		t.Fatalf("deleted account still listed or active: %+v %+v", page.Items, stored)
	}
	if status, _ := client.request(http.MethodDelete, "/web/v1/admin/users/tom", nil, true); status != http.StatusNotFound {
		t.Fatalf("deleting twice = %d", status)
	}
	_, audit := client.request(http.MethodGet, "/web/v1/admin/audit", nil, false)
	if !strings.Contains(fmt.Sprintf("%v", audit["items"]), "user.delete") {
		t.Fatal("deletion wasn't audited")
	}
}
