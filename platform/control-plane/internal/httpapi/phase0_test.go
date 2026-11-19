package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

// A repository-scoped token must stay what it is: it can't be presented as a subject
// token to mint a token for another repository or to renew itself.
func TestExchangeRejectsItsOwnTokensAsSubject(t *testing.T) {
	const first, second = "123e4567-e89b-42d3-a456-426614174000", "223e4567-e89b-42d3-a456-426614174000"
	storage := store.NewMemory()
	user := model.User{ID: "user", Username: "alice", Role: model.RoleUser}
	storage.UpsertUser(t.Context(), user)
	for _, id := range []string{first, second} {
		storage.CreateRepository(t.Context(), "owner", model.Repository{ID: id, Name: "r" + id[:1], OwnerID: "owner"})
		storage.SetGrant(t.Context(), "owner", model.Grant{RepositoryID: id, SubjectType: model.SubjectUser, Subject: user.ID, Permission: model.PermissionWrite})
	}
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{})
	exchange, err := NewTokenExchange(storage, fakeBearerAuth{user: user}, "http://localhost/web/v1/lore-oidc", "lore-native")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(storage, auth, Config{TokenExchange: exchange})
	form := url.Values{
		"grant_type": {tokenExchangeGrant}, "client_id": {"lore-native"}, "subject_token": {"subject-token"},
		"subject_token_type": {accessTokenType}, "requested_token_type": {accessTokenType}, "resource": {"lore-" + strings.ReplaceAll(first, "-", "")},
	}
	status, scoped := postExchange(t, handler, form)
	if status != http.StatusOK || scoped == "" {
		t.Fatalf("login token exchange status = %d", status)
	}

	// Production wiring: subject tokens go through LoginTokens, the Lore server's calls
	// through AuthenticateBearer.
	exchange.auth = auth.LoginTokens()
	auth.SetTokenExchange(exchange)
	for _, resource := range []string{first, second} {
		form.Set("subject_token", scoped)
		form.Set("resource", "lore-"+strings.ReplaceAll(resource, "-", ""))
		if status, token := postExchange(t, handler, form); status != http.StatusUnauthorized || token != "" {
			t.Fatalf("repository token accepted as a subject token for %s: status = %d", resource, status)
		}
	}
	if authenticated, err := auth.AuthenticateBearer(t.Context(), scoped); err != nil || authenticated.ID != user.ID {
		t.Fatalf("the Lore server's calls must still authenticate with the repository token: %v", err)
	}
}

func TestAnonymousTokenNeverAuthenticatesAsAUser(t *testing.T) {
	const id = "123e4567-e89b-42d3-a456-426614174000"
	storage := store.NewMemory()
	storage.UpsertUser(t.Context(), model.User{ID: anonymousSubject, Username: "squatter", Role: model.RoleAdmin})
	storage.CreateRepository(t.Context(), "owner", model.Repository{ID: id, Name: "public", OwnerID: "owner", Public: true})
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{})
	exchange, _ := NewTokenExchange(storage, auth.LoginTokens(), "http://localhost/web/v1/lore-oidc", "lore-native")
	auth.SetTokenExchange(exchange)
	form := url.Values{"grant_type": {anonymousGrant}, "client_id": {"lore-native"}, "requested_token_type": {accessTokenType}, "resource": {"lore-name:public"}}
	status, token := postExchange(t, NewServer(storage, auth, Config{TokenExchange: exchange}), form)
	if status != http.StatusOK {
		t.Fatalf("anonymous exchange status = %d", status)
	}
	if _, err := auth.AuthenticateBearer(t.Context(), token); err == nil {
		t.Fatal("an anonymous token authenticated as a stored account")
	}
}

func postExchange(t *testing.T, handler http.Handler, form url.Values) (int, string) {
	t.Helper()
	status, token, _ := exchangeForm(t, handler, form)
	return status, token
}

type countingCreator struct {
	fakeRepositoryCreator
	calls int
}

func (c *countingCreator) CreateRepository(context.Context, model.Repository) error {
	c.calls++
	return nil
}

func TestRepositoryVisibilityAndNameConflicts(t *testing.T) {
	const first, second = "123e4567-e89b-42d3-a456-426614174000", "223e4567-e89b-42d3-a456-426614174000"
	storage := store.NewMemory()
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{})
	lore := &countingCreator{}
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}, Repositories: lore,
	})}
	client.login()

	if status, _ := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"id": first, "name": "assets", "public": true}, true); status != http.StatusCreated {
		t.Fatalf("create status = %d", status)
	}
	// A description-only update must not make a public repository private.
	status, body := client.request(http.MethodPatch, "/web/v1/admin/repositories/"+first, map[string]any{"name": "assets", "description": "art"}, true)
	if status != http.StatusOK || body["public"] != true || body["description"] != "art" {
		t.Fatalf("update without public: status = %d, body = %v", status, body)
	}
	status, body = client.request(http.MethodPatch, "/web/v1/admin/repositories/"+first, map[string]any{"name": "assets", "public": false}, true)
	if status != http.StatusOK || body["public"] != false {
		t.Fatalf("explicit private: status = %d, body = %v", status, body)
	}

	// A name that is already taken is refused before Lore is asked to create anything.
	before := lore.calls
	if status, _ := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"id": second, "name": "assets"}, true); status != http.StatusConflict {
		t.Fatalf("duplicate name status = %d, want 409", status)
	}
	if lore.calls != before {
		t.Fatal("Lore was asked to create a repository whose name is already taken")
	}
	// Retrying the original request with the same ID stays idempotent.
	if status, _ := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"id": first, "name": "assets"}, true); status != http.StatusCreated {
		t.Fatalf("idempotent retry status = %d", status)
	}
}

func TestAuditEndpointIsNewestFirstAndReadable(t *testing.T) {
	const id = "123e4567-e89b-42d3-a456-426614174000"
	client, storage := newTestClient(t)
	client.login()
	storage.UpsertUser(t.Context(), model.User{ID: "u-alice", Username: "alice", DisplayName: "Alice Artist"})
	client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"id": id, "name": "assets"}, true)
	client.request(http.MethodPut, "/web/v1/repositories/"+id+"/grants/", map[string]any{"subjectType": "user", "subject": "u-alice", "permission": "write"}, true)
	client.request(http.MethodPatch, "/web/v1/admin/users/u-alice", map[string]any{"role": "user", "disabled": true}, true)

	status, body := client.request(http.MethodGet, "/web/v1/admin/audit?pageSize=2", nil, false)
	items, _ := body["items"].([]any)
	if status != http.StatusOK || len(items) != 2 || body["nextCursor"] == "" {
		t.Fatalf("audit status = %d, body = %v", status, body)
	}
	newest, grant := items[0].(map[string]any), items[1].(map[string]any)
	if newest["action"] != "user.disable" || newest["targetName"] != "Alice Artist" {
		t.Fatalf("newest event = %v", newest)
	}
	if grant["action"] != "grant.set" || grant["subjectType"] != "user" || grant["subjectName"] != "Alice Artist" || grant["after"] != "write" {
		t.Fatalf("grant event doesn't say who received access: %v", grant)
	}
	cursor, _ := body["nextCursor"].(string)
	_, older := client.request(http.MethodGet, "/web/v1/admin/audit?pageSize=2&cursor="+cursor, nil, false)
	olderItems, _ := older["items"].([]any)
	if len(olderItems) != 1 || olderItems[0].(map[string]any)["action"] != "repository.create" {
		t.Fatalf("older page = %v", older)
	}
}

func exchangeForm(t *testing.T, handler http.Handler, form url.Values) (int, string, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/web/v1/lore-oidc/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return response.Code, body.AccessToken, body.Error
}

func TestSecurityHeaders(t *testing.T) {
	storage := store.NewMemory()
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{})
	for _, https := range []bool{false, true} {
		response := httptest.NewRecorder()
		NewServer(storage, auth, Config{SecureHeaders: https}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/web/v1/config", nil))
		header := response.Header()
		if !strings.Contains(header.Get("Content-Security-Policy"), "frame-ancestors 'none'") || header.Get("X-Frame-Options") != "DENY" || header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("security headers = %v", header)
		}
		if (header.Get("Strict-Transport-Security") != "") != https {
			t.Fatalf("HSTS with https = %v: %q", https, header.Get("Strict-Transport-Security"))
		}
	}
}

// The WebUI's own export must load back unchanged: JSON and CSV.
func TestUserExportImportRoundTrip(t *testing.T) {
	client, storage := newTestClient(t)
	client.login()
	storage.UpsertUser(t.Context(), model.User{ID: "ad-1", Username: "ivanov", Source: "ad", DirectoryID: "1a2b3c4d-5e6f-4a1b-8c2d-3e4f5a6b7c8d", Groups: []string{"g1"}})
	storage.UpsertUser(t.Context(), model.User{ID: "local-1", Username: "olivia@example.com", Source: "local"})
	for _, format := range []string{"json", "csv"} {
		request := httptest.NewRequest(http.MethodGet, "/web/v1/admin/users/export?format="+format, nil)
		request.AddCookie(client.cookie)
		exported := httptest.NewRecorder()
		client.handler.ServeHTTP(exported, request)
		if exported.Code != http.StatusOK {
			t.Fatalf("%s export status = %d", format, exported.Code)
		}
		request = httptest.NewRequest(http.MethodPost, "/web/v1/admin/users/import", strings.NewReader(exported.Body.String()))
		request.AddCookie(client.cookie)
		request.Header.Set("X-CSRF-Token", client.csrf)
		if format == "csv" {
			request.Header.Set("Content-Type", "text/csv")
		}
		imported := httptest.NewRecorder()
		client.handler.ServeHTTP(imported, request)
		var result bulkResult
		json.Unmarshal(imported.Body.Bytes(), &result)
		if imported.Code != http.StatusOK || result.Imported != 3 || len(result.Errors) != 0 {
			t.Fatalf("%s round trip: status = %d, body = %s", format, imported.Code, imported.Body.String())
		}
		user, _ := storage.GetUser(t.Context(), "ad-1")
		if user.Source != "ad" || user.DirectoryID == "" || len(user.Groups) != 1 {
			t.Fatalf("%s round trip changed a directory account: %+v", format, user)
		}
	}
}
