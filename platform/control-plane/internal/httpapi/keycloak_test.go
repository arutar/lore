package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
