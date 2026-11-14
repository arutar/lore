package httpapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"

	"github.com/arutar/lore/platform/control-plane/internal/directory"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

const (
	testIssuer = "https://idp.test/realms/lore"
	aliceGUID  = "1a2b3c4d-5e6f-4a1b-8c2d-3e4f5a6b7c8d"
	artists    = "aaaaaaaa-0000-4000-8000-000000000001"
)

// fakeDirectory is an Active Directory whose answers a test can change between calls.
type fakeDirectory struct {
	mu       sync.Mutex
	accounts map[string]directory.Account
	err      error
	calls    int
}

func (d *fakeDirectory) LookupAccount(_ context.Context, id string) (directory.Account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.err != nil {
		return directory.Account{}, d.err
	}
	account, ok := d.accounts[id]
	if !ok {
		return directory.Account{}, directory.ErrUserNotFound
	}
	return account, nil
}

func (d *fakeDirectory) set(id string, account directory.Account) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.accounts[id] = account
}

func (d *fakeDirectory) fail(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.err = err
}

type staticLinks map[string]string

func (l staticLinks) DirectoryLink(_ context.Context, id string) (string, error) { return l[id], nil }

type identityFixture struct {
	t         *testing.T
	key       *rsa.PrivateKey
	storage   *store.Memory
	auth      *Authenticator
	directory *fakeDirectory
}

func newIdentityFixture(t *testing.T, config AuthConfig) *identityFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &identityFixture{t: t, key: key, storage: store.NewMemory(), directory: &fakeDirectory{accounts: map[string]directory.Account{}}}
	if config.Directory == nil {
		config.Directory = fixture.directory
	}
	if config.ServiceClientID == "" {
		config.ServiceClientID = "lore-control-plane"
	}
	if config.BootstrapAdmin == "" {
		config.BootstrapAdmin = "admin"
	}
	config.SessionTTL = time.Hour
	fixture.auth, err = NewAuthenticator(t.Context(), fixture.storage, config)
	if err != nil {
		t.Fatal(err)
	}
	// Alice is admitted personally; the access tests below start from an empty list instead.
	if err := fixture.storage.AddAccess(t.Context(), "", model.AccessEntry{SubjectType: model.SubjectUser, Subject: aliceGUID, Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	keys := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}
	fixture.auth.serviceVerifier = oidc.NewVerifier(testIssuer, keys, &oidc.Config{ClientID: defaultAudience})
	fixture.auth.logoutVerifier = oidc.NewVerifier(testIssuer, keys, &oidc.Config{ClientID: defaultAudience, SkipExpiryCheck: true})
	return fixture
}

// token signs an access token like Keycloak issues to the native client.
func (f *identityFixture) token(claims map[string]any) string {
	f.t.Helper()
	base := map[string]any{
		"iss": testIssuer, "aud": []string{defaultAudience}, "azp": defaultNativeClientID,
		"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(),
	}
	for name, value := range claims {
		if value == nil {
			delete(base, name)
		} else {
			base[name] = value
		}
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	payload, _ := json.Marshal(base)
	signature, err := signer.Sign(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	serialized, _ := signature.CompactSerialize()
	return serialized
}

func (f *identityFixture) alice() string {
	return f.token(map[string]any{"sub": "alice-id", "preferred_username": "alice", "ldap_id": strings.ToUpper(aliceGUID)})
}

func TestLocalAdministratorSignsInWhileTheDirectoryIsDown(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{})
	f.directory.fail(errors.New("connection refused"))
	user, err := f.auth.signIn(t.Context(), identityClaims{Subject: "admin-id", Username: "admin"})
	if err != nil {
		t.Fatalf("local sign-in failed during an LDAP outage: %v", err)
	}
	if user.Source != SourceLocal || user.Role != model.RoleAdmin || f.directory.calls != 0 {
		t.Fatalf("local administrator = %+v, LDAP calls = %d", user, f.directory.calls)
	}
}

func TestBootstrapAdministratorIsAssignedOnceAndNeverToADirectoryAccount(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{})
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "admin"}})
	directoryAdmin, err := f.auth.signIn(t.Context(), identityClaims{Subject: "ad-admin-id", Username: "admin", DirectoryID: aliceGUID})
	if err != nil || directoryAdmin.Role != model.RoleUser || directoryAdmin.Source != SourceDirectory {
		t.Fatalf("an AD account named admin = %+v, %v", directoryAdmin, err)
	}
	local, err := f.auth.signIn(t.Context(), identityClaims{Subject: "admin-id", Username: "admin"})
	if err != nil || local.Role != model.RoleAdmin {
		t.Fatalf("local bootstrap administrator = %+v, %v", local, err)
	}
	// Another administrator demotes the bootstrap account; signing in again keeps it demoted.
	f.storage.UpsertUser(t.Context(), model.User{ID: "second-id", Username: "second", Source: SourceLocal})
	f.storage.UpdateUser(t.Context(), "admin-id", "second-id", model.RoleAdmin, false)
	f.storage.UpdateUser(t.Context(), "second-id", "admin-id", model.RoleUser, false)
	again, err := f.auth.signIn(t.Context(), identityClaims{Subject: "admin-id", Username: "admin"})
	if err != nil || again.Role != model.RoleUser {
		t.Fatalf("bootstrap re-promoted a demoted account: %+v, %v", again, err)
	}
}

func TestLocalAdministratorTakesOverTheRoleFromADirectoryAdmin(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{})
	// An AD account called admin signed in first and was made administrator by hand.
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "admin"}})
	if _, err := f.auth.signIn(t.Context(), identityClaims{Subject: "ad-admin-id", Username: "admin", DirectoryID: aliceGUID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.storage.UpdateUser(t.Context(), "", "ad-admin-id", model.RoleAdmin, false); err != nil {
		t.Fatal(err)
	}
	// setup then created the local admin in Keycloak, which now wins the name.
	local, err := f.auth.signIn(t.Context(), identityClaims{Subject: "admin-id", Username: "admin"})
	if err != nil || local.Role != model.RoleAdmin || local.Username != "admin" || local.Source != SourceLocal {
		t.Fatalf("local administrator = %+v, %v", local, err)
	}
	former, _ := f.storage.GetUser(t.Context(), "ad-admin-id")
	if former.Username == "admin" {
		t.Fatalf("the directory account kept the name: %+v", former)
	}
	// Only the bootstrap name takes over: another local account doesn't inherit anything.
	f.storage.UpsertUser(t.Context(), model.User{ID: "bob-ad", Username: "bob", Source: SourceDirectory, DirectoryID: "x"})
	f.storage.UpdateUser(t.Context(), "", "bob-ad", model.RoleAdmin, false)
	bob, err := f.auth.signIn(t.Context(), identityClaims{Subject: "bob-local", Username: "bob"})
	if err != nil || bob.Role != model.RoleUser {
		t.Fatalf("a non-bootstrap local account inherited the role: %+v, %v", bob, err)
	}
}

func TestDirectoryChangesReachTheNativeClientWithinTheRefreshInterval(t *testing.T) {
	const repositoryID = "123e4567-e89b-42d3-a456-426614174000"
	f := newIdentityFixture(t, AuthConfig{DirectoryRefresh: time.Nanosecond})
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}, Groups: []string{artists}})
	f.storage.CreateRepository(t.Context(), "admin", model.Repository{ID: repositoryID, Name: "assets", OwnerID: "admin"})
	f.storage.SetGrant(t.Context(), "admin", model.Grant{RepositoryID: repositoryID, SubjectType: model.SubjectGroup, Subject: artists, Permission: model.PermissionWrite})
	exchange, err := NewTokenExchange(f.storage, f.auth.LoginTokens(), "http://localhost/web/v1/lore-oidc", "lore-native")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(f.storage, f.auth, Config{TokenExchange: exchange})
	exchangeFor := func(token string) (int, string) {
		form := url.Values{"grant_type": {tokenExchangeGrant}, "client_id": {"lore-native"}, "subject_token": {token}, "subject_token_type": {accessTokenType},
			"requested_token_type": {accessTokenType}, "resource": {"lore-" + strings.ReplaceAll(repositoryID, "-", "")}}
		status, _, code := exchangeForm(t, handler, form)
		return status, code
	}
	if status, code := exchangeFor(f.alice()); status != http.StatusOK {
		t.Fatalf("group member exchange = %d %s", status, code)
	}
	stored, _ := f.storage.GetUser(t.Context(), "alice-id")
	if stored.DirectoryID != aliceGUID || stored.Source != SourceDirectory {
		t.Fatalf("directory link wasn't stored: %+v", stored)
	}

	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}})
	if status, code := exchangeFor(f.alice()); status != http.StatusForbidden || code != "access_denied" {
		t.Fatalf("after removal from the group: %d %s", status, code)
	}

	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}, Groups: []string{artists}, Disabled: true})
	if status, code := exchangeFor(f.alice()); status != http.StatusUnauthorized || code != "invalid_grant" {
		t.Fatalf("account disabled in AD: %d %s", status, code)
	}
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}, Groups: []string{artists}})
	if status, _ := exchangeFor(f.alice()); status != http.StatusOK {
		t.Fatalf("re-enabled account: %d", status)
	}
	events, _ := f.storage.ListAudit(t.Context(), 0, 10)
	if events.Items[0].Action != "user.directory_enable" || events.Items[1].Action != "user.directory_disable" {
		t.Fatalf("directory status changes weren't audited: %+v", events.Items[:2])
	}
}

func TestDirectoryOutageKeepsKnownGroupsOnlyForTheGracePeriod(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{DirectoryRefresh: time.Minute, DirectoryOutageGrace: time.Hour})
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}, Groups: []string{artists}})
	if _, err := f.auth.AuthenticateBearer(t.Context(), f.alice()); err != nil {
		t.Fatal(err)
	}
	syncedAgo := func(age time.Duration) {
		f.storage.UpdateDirectoryState(t.Context(), "alice-id", store.DirectoryState{DirectoryID: aliceGUID, Groups: []string{artists}, SyncedAt: time.Now().Add(-age)})
		f.auth.directoryAttempts.Clear()
	}

	f.directory.fail(errors.New("connection refused"))
	syncedAgo(2 * time.Minute)
	user, err := f.auth.AuthenticateBearer(t.Context(), f.alice())
	if err != nil || len(user.Groups) != 1 || user.Groups[0] != artists {
		t.Fatalf("within the grace period: %+v, %v", user, err)
	}
	// Repeated requests during the outage don't each wait on the directory.
	calls := f.directory.calls
	for range 5 {
		if _, err := f.auth.AuthenticateBearer(t.Context(), f.alice()); err != nil {
			t.Fatalf("repeated request within the grace period: %v", err)
		}
	}
	if f.directory.calls != calls {
		t.Fatalf("directory called %d more times during an outage", f.directory.calls-calls)
	}

	syncedAgo(2 * time.Hour)
	if _, err := f.auth.AuthenticateBearer(t.Context(), f.alice()); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("past the grace period: %v, want ErrDirectoryUnavailable", err)
	}
	if _, err := f.auth.AuthenticateBearer(t.Context(), f.alice()); !errors.Is(err, ErrDirectoryUnavailable) {
		t.Fatalf("backed-off request past the grace period: %v, want ErrDirectoryUnavailable", err)
	}

	// A deleted account isn't an outage: it loses access at once.
	f.directory.fail(nil)
	f.directory.mu.Lock()
	delete(f.directory.accounts, aliceGUID)
	f.directory.mu.Unlock()
	syncedAgo(2 * time.Minute)
	if _, err := f.auth.AuthenticateBearer(t.Context(), f.alice()); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("deleted account: %v", err)
	}
}

func TestBearerTokensMustBeIssuedForLoreToTheNativeOrServiceClient(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{})
	cases := []struct {
		name   string
		claims map[string]any
		ok     bool
	}{
		{"native client", map[string]any{"sub": "bob-id", "preferred_username": "bob"}, true},
		{"web client ID token", map[string]any{"sub": "bob-id", "aud": []string{"lore-web"}, "azp": "lore-web"}, false},
		{"other client with Lore audience", map[string]any{"sub": "bob-id", "azp": "some-other-app"}, false},
		{"no authorized party", map[string]any{"sub": "bob-id", "azp": nil}, false},
		{"expired", map[string]any{"sub": "bob-id", "exp": time.Now().Add(-time.Minute).Unix()}, false},
	}
	for _, testCase := range cases {
		_, err := f.auth.LoginTokens().AuthenticateBearer(t.Context(), f.token(testCase.claims))
		if (err == nil) != testCase.ok {
			t.Errorf("%s: error = %v, want accepted = %v", testCase.name, err, testCase.ok)
		}
	}
	service, err := f.auth.AuthenticateBearer(t.Context(), f.token(map[string]any{"sub": "svc-id", "preferred_username": "service-account-lore-control-plane", "azp": "lore-control-plane"}))
	if err != nil || service.Source != SourceService || service.Role != model.RoleUser {
		t.Fatalf("service account = %+v, %v", service, err)
	}
	// Logout accepts an expired token, but only one the native client received.
	if id, err := f.auth.AuthenticateBearerForLogout(t.Context(), f.token(map[string]any{"sub": "bob-id", "exp": time.Now().Add(-48 * time.Hour).Unix()})); err != nil || id != "bob-id" {
		t.Fatalf("expired native token for logout = %q, %v", id, err)
	}
	if _, err := f.auth.AuthenticateBearerForLogout(t.Context(), f.token(map[string]any{"sub": "bob-id", "azp": "lore-control-plane"})); err == nil {
		t.Fatal("logout accepted a token of another client")
	}
}

func TestMissingDirectoryClaimFallsBackToTheIdentityProviderLink(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{DirectoryLinks: staticLinks{"alice-id": aliceGUID}})
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}, Groups: []string{artists}})
	user, err := f.auth.AuthenticateBearer(t.Context(), f.token(map[string]any{"sub": "alice-id", "preferred_username": "alice"}))
	if err != nil || user.DirectoryID != aliceGUID || len(user.Groups) != 1 {
		t.Fatalf("realm without the ldap_id mapper: %+v, %v", user, err)
	}
	// A local account named like an AD account doesn't inherit its groups.
	local, err := f.auth.AuthenticateBearer(t.Context(), f.token(map[string]any{"sub": "other-id", "preferred_username": "alice2", "groups": []string{"keycloak-group"}}))
	if err != nil || local.Federated() || len(local.Groups) != 1 || local.Groups[0] != "keycloak-group" {
		t.Fatalf("local account = %+v, %v", local, err)
	}
}

func TestRecreatedIdentityDoesNotInheritAccessByName(t *testing.T) {
	const repositoryID = "123e4567-e89b-42d3-a456-426614174000"
	f := newIdentityFixture(t, AuthConfig{})
	if _, err := f.auth.signIn(t.Context(), identityClaims{Subject: "old-id", Username: "carol@example.com"}); err != nil {
		t.Fatal(err)
	}
	f.storage.CreateRepository(t.Context(), "admin", model.Repository{ID: repositoryID, Name: "assets", OwnerID: "admin"})
	f.storage.SetGrant(t.Context(), "admin", model.Grant{RepositoryID: repositoryID, SubjectType: model.SubjectUser, Subject: "old-id", Permission: model.PermissionWrite})

	recreated, err := f.auth.signIn(t.Context(), identityClaims{Subject: "new-id", Username: "carol@example.com"})
	if err != nil {
		t.Fatalf("sign-in after the account was recreated: %v", err)
	}
	former, _ := f.storage.GetUser(t.Context(), "old-id")
	if former.Username == "carol@example.com" {
		t.Fatalf("former identity still holds the name: %+v", former)
	}
	grants, _ := f.storage.ListGrants(t.Context(), repositoryID)
	for _, grant := range grants {
		if grant.Subject == recreated.ID {
			t.Fatal("the recreated identity inherited a grant by name")
		}
	}
}

func TestWebSessionEndsWhenTheAccountIsDisabledInTheDirectory(t *testing.T) {
	f := newIdentityFixture(t, AuthConfig{DirectoryRefresh: time.Nanosecond})
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}})
	user, err := f.auth.signIn(t.Context(), identityClaims{Subject: "alice-id", Username: "alice", DirectoryID: aliceGUID})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if err := f.auth.createSession(t.Context(), recorder, user.ID); err != nil {
		t.Fatal(err)
	}
	cookie := recorder.Result().Cookies()[0]
	handler := NewServer(f.storage, f.auth, Config{})
	authenticated := func() bool {
		request := httptest.NewRequest(http.MethodGet, "/web/v1/session", nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var body struct {
			Authenticated bool `json:"authenticated"`
		}
		json.Unmarshal(response.Body.Bytes(), &body)
		return body.Authenticated
	}
	if !authenticated() {
		t.Fatal("session isn't authenticated")
	}
	f.directory.set(aliceGUID, directory.Account{User: directory.User{ID: aliceGUID, Username: "alice"}, Disabled: true})
	if authenticated() {
		t.Fatal("the web session survived disabling the account in AD")
	}
}

func TestObjectGUIDNormalization(t *testing.T) {
	if got := directory.NormalizeGUID("{1A2B3C4D-5E6F-4A1B-8C2D-3E4F5A6B7C8D}"); got != "" {
		t.Fatalf("braces must be rejected, got %q", got)
	}
	if got := directory.NormalizeGUID(" 1A2B3C4D5E6F4A1B8C2D3E4F5A6B7C8D "); got != aliceGUID {
		t.Fatalf("normalized = %q", got)
	}
}

func TestLocalAccountNameDropsThePlaceholderLastName(t *testing.T) {
	claims := identityClaims{Subject: "id", Email: "maria@example.com", Name: "Maria Lopez -"}
	claims.normalize()
	if claims.Name != "Maria Lopez" {
		t.Fatalf("name = %q, want the display name without Keycloak's placeholder last name", claims.Name)
	}
	claims = identityClaims{Subject: "id", Email: "x@example.com", Name: "-"}
	claims.normalize()
	if claims.Name != "-" && claims.Name != "x@example.com" {
		t.Fatalf("name = %q", claims.Name)
	}
}
