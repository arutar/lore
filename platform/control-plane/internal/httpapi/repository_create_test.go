package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

type fakeRepositoryCreator struct {
	deleted      string
	repositories []model.Repository
	err          error
}

func (f *fakeRepositoryCreator) CreateRepository(_ context.Context, repository model.Repository) error {
	f.repositories = append(f.repositories, repository)
	return f.err
}

func (f *fakeRepositoryCreator) DeleteRepository(_ context.Context, id, token string) error {
	if token == "" {
		return status.Error(codes.Unauthenticated, "missing token")
	}
	f.deleted = id
	return f.err
}

func TestCreateRepositoryUsesStableIDInLore(t *testing.T) {
	storage := store.NewMemory()
	creator := &fakeRepositoryCreator{}
	auth, _ := NewAuthenticator(context.Background(), storage, AuthConfig{SessionTTL: time.Hour})
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}, Repositories: creator,
	})}
	client.login()
	const id = "123e4567-e89b-42d3-a456-426614174000"
	statusCode, _ := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"id": id, "name": "assets"}, true)
	if statusCode != http.StatusCreated || len(creator.repositories) != 1 || creator.repositories[0].ID != id || creator.repositories[0].OwnerID != "admin" {
		t.Fatalf("create status = %d, Lore calls = %v", statusCode, creator.repositories)
	}
}

func TestCreateRepositoryDoesNotReportSuccessWhenLoreFails(t *testing.T) {
	storage := store.NewMemory()
	creator := &fakeRepositoryCreator{err: status.Error(codes.Unavailable, "offline")}
	auth, _ := NewAuthenticator(context.Background(), storage, AuthConfig{SessionTTL: time.Hour})
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}, Repositories: creator,
	})}
	client.login()
	statusCode, _ := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "assets"}, true)
	if statusCode != http.StatusServiceUnavailable {
		t.Fatalf("create status = %d, want 503", statusCode)
	}
	if _, err := storage.GetRepository(context.Background(), creator.repositories[0].ID); err != store.ErrNotFound {
		t.Fatalf("repository was stored after Lore failure: %v", err)
	}
}
func TestDeleteRepositoryRequiresConfirmationAndSuccessfulLoreDelete(t *testing.T) {
	storage := store.NewMemory()
	actor := model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}
	native := &fakeRepositoryCreator{}
	auth, _ := NewAuthenticator(t.Context(), storage, AuthConfig{SessionTTL: time.Hour})
	exchange, err := NewTokenExchange(storage, fakeBearerAuth{user: actor}, "http://localhost/web/v1/lore-oidc", "lore-native")
	if err != nil {
		t.Fatal(err)
	}
	client := &testClient{t: t, handler: NewServer(storage, auth, Config{DevAuth: true, DevUser: actor, Repositories: native, TokenExchange: exchange})}
	client.login()
	const id = "123e4567-e89b-42d3-a456-426614174000"
	const path = "/web/v1/admin/repositories/" + id
	code, _ := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]string{"id": id, "name": "disposable"}, true)
	if code != 201 {
		t.Fatalf("create %d", code)
	}
	for _, tc := range []struct {
		name string
		csrf bool
		want int
	}{{"disposable", false, 403}, {"wrong", true, 409}} {
		code, _ = client.request(http.MethodDelete, path, map[string]string{"name": tc.name}, tc.csrf)
		if code != tc.want || native.deleted != "" {
			t.Fatalf("delete %d, native %q", code, native.deleted)
		}
	}
	ordinary := &testClient{t: t, handler: NewServer(storage, auth, Config{
		DevAuth: true, DevUser: model.User{ID: "reader", Username: "reader", Role: model.RoleUser}, Repositories: native, TokenExchange: exchange,
	})}
	ordinary.login()
	code, _ = ordinary.request(http.MethodDelete, path, map[string]string{"name": "disposable"}, true)
	if code != http.StatusForbidden || native.deleted != "" {
		t.Fatalf("ordinary user delete %d; native %q", code, native.deleted)
	}
	native.err = status.Error(codes.Unavailable, "offline")
	code, _ = client.request(http.MethodDelete, path, map[string]string{"name": "disposable"}, true)
	if code != 503 {
		t.Fatalf("failed native delete %d", code)
	}
	if _, err := storage.GetRepository(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	native.err = nil
	code, _ = client.request(http.MethodDelete, path, map[string]string{"name": "disposable"}, true)
	if code != 204 || native.deleted != id {
		t.Fatalf("delete %d, native %q", code, native.deleted)
	}
	if _, err := storage.GetRepository(t.Context(), id); err != store.ErrNotFound {
		t.Fatalf("after deletion: %v", err)
	}
}
