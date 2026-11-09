package loreclient

import (
	"context"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	repositoryv1 "github.com/arutar/lore/platform/control-plane/gen/lore/repository/v1"
)

func TestLiveRepositoryGet(t *testing.T) {
	address := os.Getenv("LORE_LIVE_ADDR")
	token := os.Getenv("LORE_LIVE_TOKEN")
	id := os.Getenv("LORE_LIVE_REPOSITORY_ID")
	if address == "" || token == "" || id == "" {
		t.Skip("live Lore endpoint isn't configured")
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+token), 15*time.Second)
	defer cancel()
	response, err := repositoryv1.NewRepositoryServiceClient(connection).RepositoryGet(ctx, &repositoryv1.RepositoryGetRequest{
		Query: &repositoryv1.RepositoryGetRequest_Id{Id: mustUUID(t, id)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Repository.GetId() == nil || response.Repository.GetName() == "" {
		t.Fatalf("empty repository response: %v", response)
	}
}

func mustUUID(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := decodeUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
