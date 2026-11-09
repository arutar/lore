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
	repositories []model.Repository
	err          error
}

func (f *fakeRepositoryCreator) CreateRepository(_ context.Context, repository model.Repository) error {
	f.repositories = append(f.repositories, repository)
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
