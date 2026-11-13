package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKeycloakRealmAdminUpdatesRegistration(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/lore/protocol/openid-connect/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "service-token", "token_type": "Bearer"})
		case "/admin/realms/lore":
			if r.Method != http.MethodPut || r.Header.Get("Authorization") != "Bearer service-token" {
				t.Errorf("unexpected admin request: %s, authorization %q", r.Method, r.Header.Get("Authorization"))
			}
			var input map[string]bool
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["registrationAllowed"] != true {
				t.Errorf("unexpected realm update: %v, %v", input, err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	admin, err := NewKeycloakRealmAdmin(server.URL+"/realms/lore", server.URL+"/realms/lore/protocol/openid-connect/token", "lore-control-plane", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.SetRegistrationAllowed(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

func TestKeycloakRealmAdminRejectsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/realms/lore/protocol/openid-connect/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "service-token", "token_type": "Bearer"})
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	admin, err := NewKeycloakRealmAdmin(server.URL+"/realms/lore", server.URL+"/realms/lore/protocol/openid-connect/token", "lore-control-plane", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.SetRegistrationAllowed(context.Background(), true); err == nil {
		t.Fatal("expected Keycloak failure")
	}
}

func TestKeycloakLocalAccountLifecycle(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/lore/protocol/openid-connect/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "service-token", "token_type": "Bearer"})
		case "/admin/realms/lore/users":
			if r.Method != http.MethodPost {
				t.Errorf("create method = %s", r.Method)
			}
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["username"] != "person@example.com" {
				t.Errorf("create input = %v, %v", input, err)
			}
			w.Header().Set("Location", server.URL+"/admin/realms/lore/users/local-user")
			w.WriteHeader(http.StatusCreated)
		case "/admin/realms/lore/users/local-user":
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"local-user"}`))
				return
			}
			var input map[string]bool
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["enabled"] {
				t.Errorf("disable input = %v, %v", input, err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	admin, err := NewKeycloakRealmAdmin(server.URL+"/realms/lore", server.URL+"/realms/lore/protocol/openid-connect/token", "lore-control-plane", "secret")
	if err != nil {
		t.Fatal(err)
	}
	user, err := admin.CreateLocalUser(t.Context(), "person@example.com", "Person", "temporary-password")
	if err != nil || user.ID != "local-user" || user.Username != "person@example.com" {
		t.Fatalf("created user = %+v, %v", user, err)
	}
	if err := admin.SetLocalUserEnabled(t.Context(), "local-user", false); err != nil {
		t.Fatal(err)
	}
}

func TestKeycloakLogoutUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/lore/protocol/openid-connect/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "service-token", "token_type": "Bearer"})
		case "/admin/realms/lore/users/local-user/logout":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer service-token" {
				t.Errorf("unexpected logout request: %s, authorization %q", r.Method, r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	admin, err := NewKeycloakRealmAdmin(server.URL+"/realms/lore", server.URL+"/realms/lore/protocol/openid-connect/token", "lore-control-plane", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.LogoutUser(t.Context(), "local-user"); err != nil {
		t.Fatal(err)
	}
}

func TestKeycloakSyncFederatedUsers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/lore/protocol/openid-connect/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "service-token", "token_type": "Bearer"})
		case "/admin/realms/lore/components":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "ldap-provider", "providerId": "ldap"}})
		case "/admin/realms/lore/user-storage/ldap-provider/sync":
			if r.Method != http.MethodPost || r.URL.Query().Get("action") != "triggerFullSync" || r.Header.Get("Authorization") != "Bearer service-token" {
				t.Errorf("unexpected synchronization request: %s, query = %v", r.Method, r.URL.Query())
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	admin, err := NewKeycloakRealmAdmin(server.URL+"/realms/lore", server.URL+"/realms/lore/protocol/openid-connect/token", "lore-control-plane", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.SyncFederatedUsers(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestKeycloakGetFederatedUser(t *testing.T) {
	const guid = "0b9c2f4e-1d2a-4c3b-9e8f-7a6b5c4d3e2f"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/lore/protocol/openid-connect/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "service-token", "token_type": "Bearer"})
		case "/admin/realms/lore/users":
			query := r.URL.Query()
			if r.Method != http.MethodGet || query.Get("exact") != "true" || query.Get("search") != "" || r.Header.Get("Authorization") != "Bearer service-token" {
				t.Errorf("unexpected user lookup: %s, query = %v", r.Method, query)
			}
			accounts := []map[string]any{}
			switch {
			case query.Get("username") == "elessar":
				accounts = append(accounts, map[string]any{"id": "ad-user-id", "username": "elessar", "firstName": "AD", "lastName": "User",
					"enabled": true, "federationLink": "ldap", "attributes": map[string][]string{"LDAP_ID": {strings.ToUpper(guid)}}})
			case query.Get("username") == "local":
				accounts = append(accounts, map[string]any{"id": "local-id", "username": "local", "enabled": true})
			case query.Get("email") == "legacy@example.com":
				accounts = append(accounts, map[string]any{"id": "legacy-id", "username": "legacy-ad-user", "email": "legacy@example.com",
					"enabled": true, "federationLink": "ldap", "attributes": map[string][]string{"LDAP_ID": {guid}}})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(accounts)
		case "/admin/realms/lore/users/ad-user-id":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ad-user-id", "federationLink": "ldap", "attributes": map[string][]string{"LDAP_ID": {guid}}})
		case "/admin/realms/lore/users/local-id":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "local-id"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	admin, err := NewKeycloakRealmAdmin(server.URL+"/realms/lore", server.URL+"/realms/lore/protocol/openid-connect/token", "lore-control-plane", "secret")
	if err != nil {
		t.Fatal(err)
	}
	user, err := admin.GetFederatedUser(t.Context(), "elessar")
	if err != nil || user.ID != "ad-user-id" || user.Source != "ad" || user.DisplayName != "AD User" || user.DirectoryID != guid {
		t.Fatalf("federated user = %+v, %v", user, err)
	}
	if _, err := admin.GetFederatedUser(t.Context(), "local"); err != ErrNotFederated {
		t.Fatalf("local account error = %v", err)
	}
	legacy, err := admin.GetFederatedUser(t.Context(), "legacy@example.com")
	if err != nil || legacy.Username != "legacy-ad-user" {
		t.Fatalf("legacy email lookup = %+v, %v", legacy, err)
	}
	if _, err := admin.GetFederatedUser(t.Context(), "elessa"); err != ErrAccountNotFound {
		t.Fatalf("a partial name must not match: %v", err)
	}
	for id, want := range map[string]string{"ad-user-id": guid, "local-id": "", "deleted-id": ""} {
		if link, err := admin.DirectoryLink(t.Context(), id); err != nil || link != want {
			t.Fatalf("directory link of %s = %q, %v; want %q", id, link, err, want)
		}
	}
}
