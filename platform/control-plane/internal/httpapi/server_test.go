package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
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
	// Import changes role and status of existing accounts only; it never creates an
	// account and never rewrites groups or the source.
	status, users := client.request(http.MethodPost, "/web/v1/admin/users/import", []map[string]any{
		{"id": "member", "username": "renamed", "role": "admin", "groups": []string{"admins-guid"}},
		{"id": "admin", "username": "admin", "role": "user"},
		{"id": "nobody", "username": "nobody", "role": "admin"},
	}, true)
	if status != http.StatusOK || users["imported"] != float64(1) || len(users["errors"].([]any)) != 2 {
		t.Fatalf("user import status = %d, result = %v", status, users)
	}
	member, _ := storage.GetUser(context.Background(), "member")
	if member.Role != model.RoleAdmin || member.Username != "member" || len(member.Groups) != 1 || member.Groups[0] != "ad-group-guid" {
		t.Fatalf("import changed more than role and status: %+v", member)
	}
	if _, err := storage.GetUser(context.Background(), "nobody"); err == nil {
		t.Fatal("import created an account")
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

var fakeGroups = []directory.Group{{ID: "ad-group-guid", Name: "Artists"}, {ID: "ad-group-2", Name: "Art Directors"}}

func (fakeSubjectDirectory) SearchGroups(_ context.Context, prefix string, limit int) ([]directory.Group, error) {
	result := []directory.Group{}
	for _, group := range fakeGroups {
		if strings.HasPrefix(strings.ToLower(group.Name), strings.ToLower(prefix)) && len(result) < limit {
			result = append(result, group)
		}
	}
	return result, nil
}

func (fakeSubjectDirectory) LookupGroup(_ context.Context, id string) (directory.Group, error) {
	for _, group := range fakeGroups {
		if group.ID == id {
			return group, nil
		}
	}
	return directory.Group{}, directory.ErrGroupNotFound
}

const elessarGUID = "0b9c2f4e-1d2a-4c3b-9e8f-7a6b5c4d3e2f"

func (fakeSubjectDirectory) LookupAccount(_ context.Context, id string) (directory.Account, error) {
	if id != elessarGUID {
		return directory.Account{}, directory.ErrUserNotFound
	}
	return directory.Account{User: directory.User{ID: id, Username: "elessar"}, Groups: []string{"ad-group-guid"}}, nil
}

func (fakeSubjectDirectory) FindUser(context.Context, string) (directory.User, error) {
	return directory.User{ID: elessarGUID, Username: "elessar", Email: "elessar@example.com"}, nil
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
	// Suggestions need a search of two characters and never return the full lists.
	status, subjects := client.request(http.MethodGet, "/web/v1/repositories/"+id+"/grants/subjects", nil, false)
	if status != http.StatusOK || len(subjects["items"].([]any)) != 0 {
		t.Fatalf("subjects without a search = %d, %v", status, subjects)
	}
	// Groups are offered for grants only once they're on the list of users and groups.
	storage.AddAccess(t.Context(), "admin", model.AccessEntry{SubjectType: model.SubjectGroup, Subject: "ad-group-guid", Name: "Artists"})
	storage.UpsertUser(t.Context(), model.User{ID: "u-arthur", Username: "arthur", DisplayName: "Arthur Dent", Email: "arthur@example.com", Source: "local"})
	storage.UpsertUser(t.Context(), model.User{ID: "svc", Username: "service-account-lore-control-plane", Source: SourceService})
	status, subjects = client.request(http.MethodGet, "/web/v1/repositories/"+id+"/grants/subjects?q=ar", nil, false)
	text := fmt.Sprintf("%v", subjects["items"])
	if status != http.StatusOK || !strings.Contains(text, "Artists") || !strings.Contains(text, "Arthur Dent") || strings.Contains(text, "service-account") {
		t.Fatalf("subjects status = %d, body = %v", status, subjects)
	}
	// A typed username, email or exact group name resolves to the stored ID.
	for _, typed := range []map[string]any{
		{"subjectType": "user", "subject": "ARTHUR@example.com", "permission": "read"},
		{"subjectType": "group", "subject": "artists", "permission": "write"},
	} {
		if status, body := client.request(http.MethodPut, "/web/v1/repositories/"+id+"/grants", typed, true); status != http.StatusOK {
			t.Fatalf("typed subject %v: %d %v", typed, status, body)
		}
	}
	status, grants := client.request(http.MethodGet, "/web/v1/repositories/"+id+"/grants/", nil, false)
	text = fmt.Sprintf("%v", grants["items"])
	if status != http.StatusOK || !strings.Contains(text, "subject:u-arthur") || !strings.Contains(text, "label:Arthur Dent") || !strings.Contains(text, "subject:ad-group-guid") || !strings.Contains(text, "label:Artists") {
		t.Fatalf("labeled grants = %v", grants)
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
	// Grant events name AD groups, not their objectGUID.
	if text := fmt.Sprintf("%v", audit["items"]); !strings.Contains(text, "subjectName:Artists") {
		t.Fatalf("group grant in the audit isn't named: %v", audit["items"])
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

type fakeAccountManager struct {
	deletedID        string
	enabled          *bool
	logoutID         string
	federatedMissing bool
	syncCalls        int
	wrongLink        bool
	links            map[string]string
	// emailAsUsername makes the identity provider keep directory accounts under their
	// email, as Keycloak does when the realm uses the email as the username.
	emailAsUsername bool
}

func (f *fakeAccountManager) DirectoryLink(_ context.Context, id string) (string, error) {
	return f.links[id], nil
}

func (f *fakeAccountManager) CreateLocalUser(_ context.Context, email, displayName, _ string) (model.User, error) {
	return model.User{ID: "local-user", Username: email, Email: email, DisplayName: displayName, Role: model.RoleUser, Source: "local"}, nil
}

func (f *fakeAccountManager) SetLocalUserEnabled(_ context.Context, _ string, enabled bool) error {
	f.enabled = &enabled
	return nil
}

func (f *fakeAccountManager) DeleteLocalUser(_ context.Context, id string) error {
	f.deletedID = id
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
	link := elessarGUID
	if f.wrongLink {
		link = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	}
	name := username
	if f.emailAsUsername {
		name = "elessar@example.com"
	}
	return model.User{ID: "ad-user-id", Username: name, Email: "elessar@example.com", DisplayName: "AD User", Role: model.RoleUser, Source: "ad", DirectoryID: link}, nil
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
	if err != nil || stored.Source != "ad" || stored.DirectoryID != elessarGUID || stored.GroupsSyncedAt == nil {
		t.Fatalf("stored AD user = %+v, %v", stored, err)
	}
	// Adding an account from the directory by hand admits it.
	entries, _ := storage.ListAccess(t.Context())
	if stored.AccessDenied || len(entries) != 1 || entries[0].Subject != elessarGUID || entries[0].Name != "elessar" {
		t.Fatalf("provisioned account wasn't admitted: %+v %+v", stored, entries)
	}
}

// An account added from the directory is stored under its directory username, the name its
// token carries at sign-in, even when the identity provider keeps it under its email; otherwise
// it can't be found by login until the person first signs in.
func TestProvisionADUserKeepsTheDirectoryUsername(t *testing.T) {
	storage := store.NewMemory()
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin},
		Accounts: &fakeAccountManager{emailAsUsername: true}, Directory: fakeSubjectDirectory{},
	})}
	client.login()
	status, provisioned := client.request(http.MethodPost, "/web/v1/admin/users/provision", map[string]any{"username": "elessar"}, true)
	if status != http.StatusCreated || provisioned["username"] != "elessar" {
		t.Fatalf("AD provisioning status = %d, user = %v", status, provisioned)
	}
	stored, err := storage.GetUser(t.Context(), "ad-user-id")
	if err != nil || stored.Username != "elessar" || stored.Email != "elessar@example.com" {
		t.Fatalf("stored AD user = %+v, %v", stored, err)
	}
}

func TestProvisionADUserRefusesAMismatchedDirectoryLink(t *testing.T) {
	storage := store.NewMemory()
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin},
		Accounts: &fakeAccountManager{wrongLink: true}, Directory: fakeSubjectDirectory{},
	})}
	client.login()
	status, body := client.request(http.MethodPost, "/web/v1/admin/users/provision", map[string]any{"username": "elessar"}, true)
	if status != http.StatusConflict {
		t.Fatalf("mismatched link status = %d, body = %v", status, body)
	}
	if _, err := storage.GetUser(t.Context(), "ad-user-id"); err == nil {
		t.Fatal("an account linked to another directory entry was stored")
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
