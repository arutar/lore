package loreclient

import (
	"context"
	"net"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	repositoryv1 "github.com/arutar/lore/platform/control-plane/gen/lore/repository/v1"
	"github.com/arutar/lore/platform/control-plane/internal/model"
)

type repositoryServer struct {
	repositoryv1.UnimplementedRepositoryServiceServer
	request *repositoryv1.RepositoryCreateRequest
	token   string
}

func (s *repositoryServer) RepositoryCreate(ctx context.Context, request *repositoryv1.RepositoryCreateRequest) (*repositoryv1.RepositoryCreateResponse, error) {
	s.request = request
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) == 1 {
		s.token = values[0]
	}
	return &repositoryv1.RepositoryCreateResponse{}, nil
}

func TestCreateRepositoryRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	backend := &repositoryServer{}
	repositoryv1.RegisterRepositoryServiceServer(server, backend)
	go server.Serve(listener)
	defer server.Stop()

	client, err := New(Config{Address: listener.Addr().String(), TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "service-token"})})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	const id = "123e4567-e89b-42d3-a456-426614174000"
	if err := client.CreateRepository(t.Context(), model.Repository{ID: id, Name: "assets", Description: "Art", OwnerID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if backend.token != "Bearer service-token" || backend.request.Name != "assets" || backend.request.GetCreator() != "owner" {
		t.Fatalf("request = %v, token = %q", backend.request, backend.token)
	}
	if len(backend.request.Id) != 16 || len(backend.request.DefaultBranchId) != 16 || backend.request.DefaultBranchName != "main" {
		t.Fatalf("invalid repository or branch identity: %v", backend.request)
	}
	if got := defaultBranchID(id); string(got) != string(backend.request.DefaultBranchId) {
		t.Fatal("default branch ID isn't deterministic")
	}
}
