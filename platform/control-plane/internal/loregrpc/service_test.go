package loregrpc

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	userv1 "github.com/arutar/lore/platform/control-plane/gen/lore/user/v1"
	"github.com/arutar/lore/platform/control-plane/internal/deletions"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

type fakeAuth struct{ user model.User }

func (f fakeAuth) AuthenticateBearer(_ context.Context, token string) (model.User, error) {
	if token != "valid" {
		return model.User{}, ErrUnauthenticated
	}
	return f.user, nil
}

func testServices(t *testing.T) (userv1.UserServiceClient, userv1.RepositoryRegistryServiceClient, context.Context, *store.Memory) {
	t.Helper()
	return testServicesAs(t, model.User{ID: "user", Username: "alice", Role: model.RoleAdmin})
}

func testServicesAs(t *testing.T, caller model.User) (userv1.UserServiceClient, userv1.RepositoryRegistryServiceClient, context.Context, *store.Memory) {
	t.Helper()
	storage := store.NewMemory()
	storage.UpsertUser(context.Background(), model.User{ID: "user", Username: "alice", DisplayName: "Alice", Role: model.RoleUser})
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	New(storage, fakeAuth{user: caller}).Register(server)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	ctx, cancel := context.WithTimeout(metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer valid"), 5*time.Second)
	t.Cleanup(cancel)
	return userv1.NewUserServiceClient(connection), userv1.NewRepositoryRegistryServiceClient(connection), ctx, storage
}

func TestDirectoryAndRegistryRoundTrip(t *testing.T) {
	users, registry, ctx, _ := testServices(t)
	found, err := users.UserFind(ctx, &userv1.UserFindRequest{Query: &userv1.UserFindRequest_Username{Username: "alice"}})
	if err != nil || len(found.User) != 1 || found.User[0].Id != "user" {
		t.Fatalf("UserFind = %v, %v", found, err)
	}
	id := make([]byte, 16)
	id[15] = 42
	if _, err := registry.RepositoryRegister(ctx, &userv1.RepositoryRegisterRequest{Id: id, Name: "assets"}); err != nil {
		t.Fatal(err)
	}
	stream, err := registry.RepositoryList(ctx, &userv1.RepositoryListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	item, err := stream.Recv()
	if err != nil || item.Permission[0] != "owner" || item.Id[15] != 42 {
		t.Fatalf("RepositoryList = %v, %v", item, err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("stream end = %v, want EOF", err)
	}
	if _, err := registry.RepositoryUnregister(ctx, &userv1.RepositoryUnregisterRequest{Id: id}); err != nil {
		t.Fatal(err)
	}
}

// While the WebUI deletes a repository, Lore's unregistration leaves the row: the WebUI removes it
// once Lore confirms, and keeps it listed when Lore fails part way.
func TestUnregisterLeavesARepositoryTheWebUIIsDeleting(t *testing.T) {
	storage := store.NewMemory()
	tracker := &deletions.Tracker{}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	New(storage, fakeAuth{user: model.User{ID: "user", Username: "alice", Role: model.RoleAdmin}}).WithDeletions(tracker).Register(server)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	ctx, cancel := context.WithTimeout(metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer valid"), 5*time.Second)
	t.Cleanup(cancel)
	registry := userv1.NewRepositoryRegistryServiceClient(connection)
	id := make([]byte, 16)
	id[15] = 9
	const key = "00000000-0000-0000-0000-000000000009"
	if _, err := registry.RepositoryRegister(ctx, &userv1.RepositoryRegisterRequest{Id: id, Name: "assets"}); err != nil {
		t.Fatal(err)
	}
	done := tracker.Begin(key)
	if _, err := registry.RepositoryUnregister(ctx, &userv1.RepositoryUnregisterRequest{Id: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.GetRepository(context.Background(), key); err != nil {
		t.Fatalf("the row went while the WebUI deletes it: %v", err)
	}
	done()
	if _, err := registry.RepositoryUnregister(ctx, &userv1.RepositoryUnregisterRequest{Id: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.GetRepository(context.Background(), key); err != store.ErrNotFound {
		t.Fatalf("a deletion started elsewhere removes the row: %v", err)
	}
}

func TestRegistryAllowsOnlyAdministratorsAndTheServiceAccount(t *testing.T) {
	id := make([]byte, 16)
	id[15] = 7
	for _, testCase := range []struct {
		name   string
		caller model.User
		want   codes.Code
	}{
		{"user", model.User{ID: "user", Username: "alice", Role: model.RoleUser}, codes.PermissionDenied},
		{"service account", model.User{ID: "svc", Username: "service-account-lore-control-plane", Role: model.RoleUser, Source: serviceSource}, codes.OK},
		{"administrator", model.User{ID: "user", Username: "alice", Role: model.RoleAdmin}, codes.OK},
	} {
		_, registry, ctx, storage := testServicesAs(t, testCase.caller)
		_, err := registry.RepositoryRegister(ctx, &userv1.RepositoryRegisterRequest{Id: id, Name: "assets"})
		if status.Code(err) != testCase.want {
			t.Fatalf("%s: RepositoryRegister error = %v, want %s", testCase.name, err, testCase.want)
		}
		if _, err := storage.GetRepository(context.Background(), "00000000-0000-0000-0000-000000000007"); (err == nil) != (testCase.want == codes.OK) {
			t.Fatalf("%s: repository stored = %v", testCase.name, err == nil)
		}
	}
	_, registry, ctx, _ := testServices(t)
	if _, err := registry.RepositoryRegister(ctx, &userv1.RepositoryRegisterRequest{Id: id, Name: "../escape"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid name error = %v", err)
	}
}

func TestRegistryRejectsMissingToken(t *testing.T) {
	_, registry, _, _ := testServices(t)
	_, err := registry.RepositoryRegister(context.Background(), &userv1.RepositoryRegisterRequest{Id: make([]byte, 16), Name: "assets"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("RepositoryRegister error = %v, want Unauthenticated", err)
	}
}
