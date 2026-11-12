package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

type fakeBearerAuth struct{ user model.User }

func (f fakeBearerAuth) AuthenticateBearer(_ context.Context, token string) (model.User, error) {
	if token != "subject-token" {
		return model.User{}, context.Canceled
	}
	return f.user, nil
}

func TestTokenExchangeAppliesRepositoryACL(t *testing.T) {
	const id = "123e4567-e89b-42d3-a456-426614174000"
	storage := store.NewMemory()
	user := model.User{ID: "user", Username: "alice", Role: model.RoleUser, Groups: []string{"artists"}}
	storage.UpsertUser(t.Context(), user)
	storage.CreateRepository(t.Context(), user.ID, model.Repository{ID: id, Name: "assets", OwnerID: "service"})
	storage.SetGrant(t.Context(), user.ID, model.Grant{RepositoryID: id, SubjectType: model.SubjectGroup, Subject: "artists", Permission: model.PermissionWrite})
	exchange, err := NewTokenExchange(storage, fakeBearerAuth{user: user}, "http://localhost/web/v1/lore-oidc", "lore-native")
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{})
	handler := NewServer(storage, auth, Config{TokenExchange: exchange})

	form := url.Values{
		"grant_type": {tokenExchangeGrant}, "client_id": {"lore-native"}, "subject_token": {"subject-token"},
		"subject_token_type": {accessTokenType}, "requested_token_type": {accessTokenType}, "resource": {"lore-" + strings.ReplaceAll(id, "-", "")},
	}
	request := httptest.NewRequest(http.MethodPost, "/web/v1/lore-oidc/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("exchange status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	json.Unmarshal(response.Body.Bytes(), &body)
	signature, err := jose.ParseSigned(body.AccessToken, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := signature.Verify(exchange.key.Public())
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Subject     string   `json:"sub"`
		RootDomains []string `json:"root_domains"`
		Access      []struct {
			Resource    string   `json:"resource_id"`
			Permissions []string `json:"permission"`
		} `json:"lore_access"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user" || len(claims.RootDomains) != 1 || claims.RootDomains[0] != "localhost" || len(claims.Access) != 1 || claims.Access[0].Resource != "lore-"+strings.ReplaceAll(id, "-", "") || len(claims.Access[0].Permissions) != 2 || claims.Access[0].Permissions[0] != "read" || claims.Access[0].Permissions[1] != "write" {
		t.Fatalf("claims = %+v", claims)
	}
	auth.SetTokenExchange(exchange)
	verified, err := auth.AuthenticateBearer(t.Context(), body.AccessToken)
	if err != nil || verified.ID != user.ID {
		t.Fatalf("scoped token authentication = %+v, %v", verified, err)
	}
}

func TestTokenExchangeAnonymousReadOnly(t *testing.T) {
	const publicID = "123e4567-e89b-42d3-a456-426614174000"
	const privateID = "123e4567-e89b-42d3-a456-426614174001"
	storage := store.NewMemory()
	storage.CreateRepository(t.Context(), "owner", model.Repository{ID: publicID, Name: "public", OwnerID: "owner"})
	storage.CreateRepository(t.Context(), "owner", model.Repository{ID: privateID, Name: "private", OwnerID: "owner"})
	storage.SetGrant(t.Context(), "owner", model.Grant{RepositoryID: publicID, SubjectType: model.SubjectAnonymous, Permission: model.PermissionRead})
	exchange, err := NewTokenExchange(storage, fakeBearerAuth{}, "http://localhost/web/v1/lore-oidc", "lore-native")
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{})
	handler := NewServer(storage, auth, Config{TokenExchange: exchange})

	requestToken := func(resource string) (*httptest.ResponseRecorder, map[string]any) {
		form := url.Values{
			"grant_type": {anonymousGrant}, "client_id": {"lore-native"},
			"requested_token_type": {accessTokenType}, "resource": {resource},
		}
		request := httptest.NewRequest(http.MethodPost, "/web/v1/lore-oidc/token", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var claims map[string]any
		if response.Code == http.StatusOK {
			var body struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			signature, err := jose.ParseSigned(body.AccessToken, []jose.SignatureAlgorithm{jose.RS256})
			if err != nil {
				t.Fatal(err)
			}
			payload, err := signature.Verify(exchange.key.Public())
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(payload, &claims); err != nil {
				t.Fatal(err)
			}
		}
		return response, claims
	}

	response, claims := requestToken("lore-name:public")
	access := claims["lore_access"].([]any)[0].(map[string]any)
	permissions := access["permission"].([]any)
	if response.Code != http.StatusOK || claims["sub"] != "anonymous" || len(permissions) != 1 || permissions[0] != "read" {
		t.Fatalf("anonymous response = %d, claims = %+v", response.Code, claims)
	}
	response, _ = requestToken("lore-name:private")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("private name exchange status = %d, want 400", response.Code)
	}
	response, _ = requestToken("lore-" + strings.ReplaceAll(privateID, "-", ""))
	if response.Code != http.StatusForbidden {
		t.Fatalf("private ID exchange status = %d, want 403", response.Code)
	}
}

func TestTokenExchangePersistsSigningKey(t *testing.T) {
	path := t.TempDir() + "/signing.pem"
	first, err := NewTokenExchange(store.NewMemory(), fakeBearerAuth{}, "http://localhost", "lore-native", path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewTokenExchange(store.NewMemory(), fakeBearerAuth{}, "http://localhost", "lore-native", path)
	if err != nil {
		t.Fatal(err)
	}
	if first.key.KeyID != second.key.KeyID {
		t.Fatal("signing key changed after reload")
	}
}

func TestTokenExchangeDeniesMissingGrant(t *testing.T) {
	const id = "123e4567-e89b-42d3-a456-426614174000"
	storage := store.NewMemory()
	user := model.User{ID: "user", Username: "alice", Role: model.RoleUser}
	storage.CreateRepository(t.Context(), "owner", model.Repository{ID: id, Name: "assets", OwnerID: "owner"})
	exchange, _ := NewTokenExchange(storage, fakeBearerAuth{user: user}, "http://localhost/web/v1/lore-oidc", "lore-native")
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{})
	handler := NewServer(storage, auth, Config{TokenExchange: exchange})
	form := url.Values{
		"grant_type": {tokenExchangeGrant}, "client_id": {"lore-native"}, "subject_token": {"subject-token"},
		"subject_token_type": {accessTokenType}, "requested_token_type": {accessTokenType}, "resource": {"lore-" + strings.ReplaceAll(id, "-", "")},
	}
	request := httptest.NewRequest(http.MethodPost, "/web/v1/lore-oidc/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("exchange status = %d, want 403", response.Code)
	}
}
