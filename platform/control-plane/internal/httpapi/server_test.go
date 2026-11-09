package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	if len(items) != 1 || items[0].(map[string]any)["name"] != "public" {
		t.Fatalf("anonymous catalog = %v, want only public repository", items)
	}

	publicID := publicRepository["id"].(string)
	status, _ = anonymous.request(http.MethodGet, "/web/v1/repositories/"+publicID, nil, false)
	if status != http.StatusOK {
		t.Fatalf("anonymous public repository status = %d", status)
	}
}

func TestBulkImportReportsRowErrors(t *testing.T) {
	client, _ := newTestClient(t)
	client.login()
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
