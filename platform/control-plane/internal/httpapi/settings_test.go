package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

type registrationRealmStub struct {
	called bool
	err    error
}

func (s *registrationRealmStub) SetRegistrationAllowed(context.Context, bool) error {
	s.called = true
	return s.err
}

func TestGlobalThemeSetting(t *testing.T) {
	client, _ := newTestClient(t)
	client.login()
	status, _ := client.request(http.MethodPatch, "/web/v1/admin/settings", map[string]any{"registrationEnabled": false, "theme": "ocean"}, true)
	if status != http.StatusOK {
		t.Fatalf("theme update status = %d", status)
	}
	status, config := client.request(http.MethodGet, "/web/v1/config", nil, false)
	if status != http.StatusOK || config["theme"] != "ocean" {
		t.Fatalf("public theme = %v, status = %d", config["theme"], status)
	}
	status, _ = client.request(http.MethodPatch, "/web/v1/admin/settings", map[string]any{"registrationEnabled": false, "theme": "unknown"}, true)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid theme status = %d", status)
	}
}

func TestRegistrationDoesNotReportSuccessWhenKeycloakFails(t *testing.T) {
	storage := store.NewMemory()
	auth, err := NewAuthenticator(context.Background(), storage, AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	realm := &registrationRealmStub{err: errors.New("unavailable")}
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}, RegistrationRealm: realm,
	})}
	client.login()
	status, _ := client.request(http.MethodPatch, "/web/v1/admin/settings", map[string]any{"registrationEnabled": true, "theme": "midnight"}, true)
	if status != http.StatusBadGateway || !realm.called {
		t.Fatalf("registration sync status = %d, called = %v", status, realm.called)
	}
	settings, err := storage.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.RegistrationEnabled {
		t.Fatal("local registration setting changed despite Keycloak failure")
	}
}
