package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/directory"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

type testClient struct {
	t       *testing.T
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
}

func newTestClient(t *testing.T) (*testClient, *store.Memory) {
	t.Helper()
	storage := store.NewMemory()
	auth, err := NewAuthenticator(context.Background(), storage, AuthConfig{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(storage, auth, Config{
		DevAuth: true,
		DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin},
	})
	return &testClient{t: t, handler: handler}, storage
}

func (c *testClient) request(method, path string, body any, withCSRF bool) (int, map[string]any) {
	c.t.Helper()
	var input bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		input = *bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, &input)
	if c.cookie != nil {
		request.AddCookie(c.cookie)
	}
	if withCSRF {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, request)
	var decoded map[string]any
	if response.Body.Len() > 0 {
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			c.t.Fatalf("decode response %d: %v", response.Code, err)
		}
	}
	return response.Code, decoded
}

func (c *testClient) login() {
	c.t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/web/v1/dev-login", nil)
	c.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		c.t.Fatalf("dev login status = %d", response.Code)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		c.t.Fatalf("session cookies = %d, want 1", len(cookies))
	}
	c.cookie = cookies[0]
	status, body := c.request(http.MethodGet, "/web/v1/session", nil, false)
	if status != http.StatusOK {
		c.t.Fatalf("session status = %d", status)
	}
	c.csrf, _ = body["csrfToken"].(string)
	if c.csrf == "" {
		c.t.Fatal("session has no CSRF token")
	}
}

func TestRepositoryCatalogAndAdministration(t *testing.T) {
	client, _ := newTestClient(t)

	status, _ := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "public"}, false)
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous create status = %d, want 401", status)
	}

	client.login()
	status, _ = client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "public", "public": true}, false)
	if status != http.StatusForbidden {
		t.Fatalf("create without CSRF status = %d, want 403", status)
	}
	status, publicRepository := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "public", "public": true}, true)
	if status != http.StatusCreated {
		t.Fatalf("public create status = %d, body = %v", status, publicRepository)
	}
	status, _ = client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "private"}, true)
	if status != http.StatusCreated {
		t.Fatalf("private create status = %d", status)
	}

	anonymous, _ := newTestClient(t)
	anonymous.handler = client.handler
	status, catalog := anonymous.request(http.MethodGet, "/web/v1/catalog", nil, false)
	if status != http.StatusOK {
		t.Fatalf("anonymous catalog status = %d", status)
	}
	items := catalog["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["name"] != "public" || items[0].(map[string]any)["public"] != true {
		t.Fatalf("anonymous catalog = %v, want only public repository", items)
	}

	publicID := publicRepository["id"].(string)
	status, _ = anonymous.request(http.MethodGet, "/web/v1/repositories/"+publicID, nil, false)
	if status != http.StatusOK {
		t.Fatalf("anonymous public repository status = %d", status)
	}
}

func TestBulkImportReportsRowErrors(t *testing.T) {
	client, storage := newTestClient(t)
	client.login()
	if _, err := storage.UpsertUser(context.Background(), model.User{ID: "member", Username: "member", Groups: []string{"ad-group-guid"}}); err != nil {
		t.Fatal(err)
	}
	status, created := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "assets"}, true)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	id := created["id"].(string)
	status, result := client.request(http.MethodPost, "/web/v1/repositories/"+id+"/grants/import", []map[string]any{
		{"subjectType": "group", "subject": "ad-group-guid", "permission": "write"},
		{"subjectType": "anonymous", "permission": "write"},
	}, true)
	if status != http.StatusOK || result["imported"] != float64(1) || len(result["errors"].([]any)) != 1 {
		t.Fatalf("grant import status = %d, result = %v", status, result)
	}
	status, users := client.request(http.MethodPost, "/web/v1/admin/users/import", []map[string]any{
		{"id": "imported", "username": "bob", "role": "user"},
		{"id": "admin", "username": "admin", "role": "user"},
	}, true)
	if status != http.StatusOK || users["imported"] != float64(1) || len(users["errors"].([]any)) != 1 {
		t.Fatalf("user import status = %d, result = %v", status, users)
	}

	request := httptest.NewRequest(http.MethodGet, "/web/v1/repositories/"+id+"/grants/export?format=csv", nil)
	request.AddCookie(client.cookie)
	response := httptest.NewRecorder()
	client.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ad-group-guid") {
		t.Fatalf("grant export status = %d, body = %q", response.Code, response.Body.String())
	}
}

type fakeSubjectDirectory struct{}

func (fakeSubjectDirectory) ListGroups(context.Context) ([]directory.Group, error) {
	return []directory.Group{{ID: "ad-group-guid", Name: "Artists"}}, nil
}

func (fakeSubjectDirectory) GroupIDs(context.Context, string) ([]string, error) {
	return []string{"ad-group-guid"}, nil
}

func (fakeSubjectDirectory) FindUser(context.Context, string) (directory.User, error) {
	return directory.User{Username: "elessar", Email: "elessar@example.com"}, nil
}

func (fakeSubjectDirectory) SearchUsers(_ context.Context, query string) ([]directory.User, error) {
	users := []directory.User{
		{Username: "elessar", Email: "elessar@example.com", DisplayName: "Elessar"},
		{Username: "elsa", DisplayName: "Elsa"},
	}
	query = strings.ToLower(query)
	result := make([]directory.User, 0, len(users))
	for _, user := range users {
		if strings.HasPrefix(strings.ToLower(user.Username), query) {
			result = append(result, user)
		}
	}
	return result, nil
}

func TestGrantSubjectsAndReadableAudit(t *testing.T) {
	storage := store.NewMemory()
	auth, err := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}, Directory: fakeSubjectDirectory{},
	})}
	client.login()
	status, created := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "assets"}, true)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	id := created["id"].(string)
	status, subjects := client.request(http.MethodGet, "/web/v1/repositories/"+id+"/grants/subjects", nil, false)
	if status != http.StatusOK || !strings.Contains(fmt.Sprintf("%v", subjects["items"]), "Artists") {
		t.Fatalf("subjects status = %d, body = %v", status, subjects)
	}
	status, _ = client.request(http.MethodPut, "/web/v1/repositories/"+id+"/grants", map[string]any{
		"subjectType": "user", "subject": "missing", "permission": "read",
	}, true)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown subject status = %d, want 400", status)
	}
	status, repositories := client.request(http.MethodGet, "/web/v1/admin/repositories", nil, false)
	if status != http.StatusOK || !strings.Contains(fmt.Sprintf("%v", repositories["items"]), "ownerName") || !strings.Contains(fmt.Sprintf("%v", repositories["items"]), "admin") {
		t.Fatalf("repository owners = %v", repositories)
	}
	status, audit := client.request(http.MethodGet, "/web/v1/admin/audit", nil, false)
	if status != http.StatusOK || !strings.Contains(fmt.Sprintf("%v", audit["items"]), "actorName") || !strings.Contains(fmt.Sprintf("%v", audit["items"]), "targetName") {
		t.Fatalf("audit identities = %v", audit)
	}
}

func TestPrivateRepositoryDoesNotLeak(t *testing.T) {
	client, _ := newTestClient(t)
	client.login()
	status, created := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "secret"}, true)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}

	anonymous := &testClient{t: t, handler: client.handler}
	status, _ = anonymous.request(http.MethodGet, "/web/v1/repositories/"+created["id"].(string), nil, false)
	if status != http.StatusNotFound {
		t.Fatalf("private repository status = %d, want 404", status)
	}
}

func TestCatalogPagination(t *testing.T) {
	client, _ := newTestClient(t)
	client.login()
	for _, name := range []string{"one", "two", "three"} {
		status, _ := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": name, "public": true}, true)
		if status != http.StatusCreated {
			t.Fatalf("create %s status = %d", name, status)
		}
	}

	seen := make([]string, 0, 3)
	cursor := ""
	for {
		path := "/web/v1/catalog?pageSize=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		status, page := client.request(http.MethodGet, path, nil, false)
		if status != http.StatusOK {
			t.Fatalf("catalog status = %d", status)
		}
		for _, item := range page["items"].([]any) {
			seen = append(seen, item.(map[string]any)["name"].(string))
		}
		cursor, _ = page["nextCursor"].(string)
		if cursor == "" {
			break
		}
	}
	if len(seen) != 3 {
		t.Fatalf("paginated catalog returned %d repositories, want 3", len(seen))
	}
}

type fakeGroupDirectory struct {
	err   error
	calls int
}

func (f *fakeGroupDirectory) GroupIDs(context.Context, string) ([]string, error) {
	f.calls++
	return nil, f.err
}

func TestGroupIDsSupportsExternalEmailAndLDAPOutages(t *testing.T) {
	external := &fakeGroupDirectory{err: errors.New("LDAP must not be called")}
	auth := Authenticator{config: AuthConfig{GroupDirectory: external}}
	groups, err := auth.groupIDs(t.Context(), "external@example.com", []string{"external-group"})
	if err != nil || len(groups) != 1 || groups[0] != "external-group" || external.calls != 0 {
		t.Fatalf("external email groups = %v, %v, LDAP calls = %d", groups, err, external.calls)
	}

	missing := &fakeGroupDirectory{err: directory.ErrUserNotFound}
	auth.config.GroupDirectory = missing
	groups, err = auth.groupIDs(t.Context(), "local-admin", []string{"local-group"})
	if err != nil || len(groups) != 1 || groups[0] != "local-group" || missing.calls != 1 {
		t.Fatalf("local account groups = %v, %v, LDAP calls = %d", groups, err, missing.calls)
	}

	unavailable := &fakeGroupDirectory{err: errors.New("LDAP unavailable")}
	auth.config.GroupDirectory = unavailable
	if _, err = auth.groupIDs(t.Context(), "ad-user", nil); err == nil {
		t.Fatal("LDAP outage must not be hidden")
	}
}

type fakeAccountManager struct {
	enabled          *bool
	logoutID         string
	federatedMissing bool
	syncCalls        int
}

func (f *fakeAccountManager) CreateLocalUser(_ context.Context, email, displayName, _ string) (model.User, error) {
	return model.User{ID: "local-user", Username: email, Email: email, DisplayName: displayName, Role: model.RoleUser, Source: "local"}, nil
}

func (f *fakeAccountManager) SetLocalUserEnabled(_ context.Context, _ string, enabled bool) error {
	f.enabled = &enabled
	return nil
}

func (f *fakeAccountManager) LogoutUser(_ context.Context, id string) error {
	f.logoutID = id
	return nil
}

func (f *fakeAccountManager) GetFederatedUser(_ context.Context, username string) (model.User, error) {
	if f.federatedMissing && f.syncCalls == 0 {
		return model.User{}, ErrAccountNotFound
	}
	if username != "elessar" && username != "elessar@example.com" {
		return model.User{}, ErrAccountNotFound
	}
	return model.User{ID: "ad-user-id", Username: username, DisplayName: "AD User", Role: model.RoleUser, Source: "ad"}, nil
}

func (f *fakeAccountManager) SyncFederatedUsers(context.Context) error {
	f.syncCalls++
	return nil
}

func TestProvisionADUser(t *testing.T) {
	storage := store.NewMemory()
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	accounts := &fakeAccountManager{}
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin},
		Accounts: accounts, Directory: fakeSubjectDirectory{},
	})}
	client.login()
	status, provisioned := client.request(http.MethodPost, "/web/v1/admin/users/provision", map[string]any{"username": "elessar"}, true)
	groups, ok := provisioned["groups"].([]any)
	if status != http.StatusCreated || provisioned["id"] != "ad-user-id" || !ok || len(groups) != 1 || groups[0] != "ad-group-guid" {
		t.Fatalf("AD provisioning status = %d, user = %v", status, provisioned)
	}
	stored, err := storage.GetUser(t.Context(), "ad-user-id")
	if err != nil || stored.Source != "ad" {
		t.Fatalf("stored AD user = %+v, %v", stored, err)
	}
}

func TestProvisionADUserSynchronizesMissingAccount(t *testing.T) {
	storage := store.NewMemory()
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	accounts := &fakeAccountManager{federatedMissing: true}
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin},
		Accounts: accounts, Directory: fakeSubjectDirectory{},
	})}
	client.login()
	status, _ := client.request(http.MethodPost, "/web/v1/admin/users/provision", map[string]any{"username": "elessar"}, true)
	if status != http.StatusCreated || accounts.syncCalls != 1 {
		t.Fatalf("AD provisioning status = %d, synchronization calls = %d", status, accounts.syncCalls)
	}
}

func TestSearchADUsers(t *testing.T) {
	storage := store.NewMemory()
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin},
		Directory: fakeSubjectDirectory{},
	})}
	client.login()
	status, result := client.request(http.MethodGet, "/web/v1/admin/users/search?q=EL", nil, false)
	items, ok := result["items"].([]any)
	if status != http.StatusOK || !ok || len(items) != 2 {
		t.Fatalf("AD search status = %d, result = %v", status, result)
	}
	first := items[0].(map[string]any)
	if first["username"] != "elessar" || first["displayName"] != "Elessar" {
		t.Fatalf("AD search result = %v", first)
	}
	status, _ = client.request(http.MethodGet, "/web/v1/admin/users/search?q=e", nil, false)
	if status != http.StatusBadRequest {
		t.Fatalf("one-character AD search status = %d, want 400", status)
	}
}

func TestNativeLogoutUsesOnlyTheBearerTokenOwner(t *testing.T) {
	storage := store.NewMemory()
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	accounts := &fakeAccountManager{}
	handler := NewServer(storage, auth, Config{
		Accounts: accounts, BearerAuth: fakeBearerAuth{user: model.User{ID: "user", Username: "user"}},
	})
	request := httptest.NewRequest(http.MethodPost, "/web/v1/native-logout", nil)
	request.Header.Set("Authorization", "Bearer subject-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || accounts.logoutID != "user" {
		t.Fatalf("native logout status = %d, account = %q", response.Code, accounts.logoutID)
	}

	request = httptest.NewRequest(http.MethodPost, "/web/v1/native-logout", nil)
	request.Header.Set("Authorization", "Bearer someone-else")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer status = %d", response.Code)
	}
}

func TestLocalAccountCreationAndDisable(t *testing.T) {
	storage := store.NewMemory()
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	accounts := &fakeAccountManager{}
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}, Accounts: accounts,
	})}
	client.login()
	status, created := client.request(http.MethodPost, "/web/v1/admin/users", map[string]any{
		"email": "person@example.com", "displayName": "Person", "password": "temporary-password", "role": "user",
	}, true)
	if status != http.StatusCreated || created["username"] != "person@example.com" {
		t.Fatalf("create local account status = %d, body = %v", status, created)
	}
	status, _ = client.request(http.MethodPatch, "/web/v1/admin/users/local-user", map[string]any{"role": "user", "disabled": true}, true)
	if status != http.StatusOK || accounts.enabled == nil || *accounts.enabled {
		t.Fatalf("disable local account status = %d, enabled = %v", status, accounts.enabled)
	}
}

func TestLogoutReturnsKeycloakEndSessionURL(t *testing.T) {
	storage := store.NewMemory()
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	auth.endSessionEndpoint = "http://localhost:8080/auth/realms/lore/protocol/openid-connect/logout"
	auth.config.ClientID = "lore-web"
	auth.config.PostLogoutRedirectURL = "http://localhost:8080/"
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin},
	})}
	client.login()
	stored, err := storage.GetSession(t.Context(), hashToken(client.cookie.Value), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stored.IDToken = "id-token"
	if err := storage.CreateSession(t.Context(), stored); err != nil {
		t.Fatal(err)
	}
	status, body := client.request(http.MethodPost, "/web/v1/logout", nil, true)
	logoutURL, _ := body["logoutUrl"].(string)
	if status != http.StatusOK || !strings.Contains(logoutURL, "client_id=lore-web") || !strings.Contains(logoutURL, "id_token_hint=id-token") || !strings.Contains(logoutURL, "post_logout_redirect_uri=") {
		t.Fatalf("logout status = %d, body = %v", status, body)
	}
	status, session := client.request(http.MethodGet, "/web/v1/session", nil, false)
	if status != http.StatusOK || session["authenticated"] != false {
		t.Fatalf("session after logout = %v", session)
	}
}
