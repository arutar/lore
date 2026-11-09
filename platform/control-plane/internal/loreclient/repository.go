package loreclient

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	repositoryv1 "github.com/arutar/lore/platform/control-plane/gen/lore/repository/v1"
	"github.com/arutar/lore/platform/control-plane/internal/model"
)

type Config struct {
	Address     string
	TokenSource oauth2.TokenSource
	Secure      bool
}

type Client struct {
	connection   *grpc.ClientConn
	repositories repositoryv1.RepositoryServiceClient
	tokens       oauth2.TokenSource
}

func New(config Config) (*Client, error) {
	if config.Address == "" || config.TokenSource == nil {
		return nil, errors.New("Lore address and token source are required")
	}
	transport := insecure.NewCredentials()
	if config.Secure {
		transport = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	connection, err := grpc.NewClient(config.Address, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, err
	}
	return &Client{connection: connection, repositories: repositoryv1.NewRepositoryServiceClient(connection), tokens: config.TokenSource}, nil
}

func (c *Client) Close() error {
	return c.connection.Close()
}

func (c *Client) CreateRepository(ctx context.Context, repository model.Repository) error {
	id, err := decodeUUID(repository.ID)
	if err != nil {
		return err
	}
	token, err := c.tokens.Token()
	if err != nil {
		return err
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token.AccessToken)
	creator := repository.OwnerID
	_, err = c.repositories.RepositoryCreate(ctx, &repositoryv1.RepositoryCreateRequest{
		Id: id, Name: repository.Name, Description: repository.Description,
		DefaultBranchId: defaultBranchID(repository.ID), DefaultBranchName: "main", Creator: &creator,
	})
	return err
}

func defaultBranchID(repositoryID string) []byte {
	digest := sha256.Sum256([]byte(repositoryID + ":main"))
	id := append([]byte(nil), digest[:16]...)
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}

func decodeUUID(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	if err != nil || len(decoded) != 16 {
		return nil, errors.New("invalid repository UUID")
	}
	return decoded, nil
}
