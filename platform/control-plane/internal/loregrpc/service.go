package loregrpc

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	userv1 "github.com/arutar/lore/platform/control-plane/gen/lore/user/v1"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/policy"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type Authenticator interface {
	AuthenticateBearer(ctx context.Context, token string) (model.User, error)
}

type Service struct {
	userv1.UnimplementedUserServiceServer
	userv1.UnimplementedRepositoryRegistryServiceServer
	store store.Store
	auth  Authenticator
}

func New(storage store.Store, auth Authenticator) *Service {
	return &Service{store: storage, auth: auth}
}

func NewServer(storage store.Store, auth Authenticator) *grpc.Server {
	server := grpc.NewServer()
	New(storage, auth).Register(server)
	return server
}

func (s *Service) Register(server grpc.ServiceRegistrar) {
	userv1.RegisterUserServiceServer(server, s)
	userv1.RegisterRepositoryRegistryServiceServer(server, s)
}

func (s *Service) UserGet(ctx context.Context, request *userv1.UserGetRequest) (*userv1.UserGetResponse, error) {
	if _, err := s.authenticate(ctx); err != nil {
		return nil, err
	}
	response := &userv1.UserGetResponse{User: make([]*userv1.User, 0, len(request.Id))}
	for _, id := range request.Id {
		user, err := s.store.GetUser(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, status.Error(codes.Internal, "user lookup failed")
		}
		response.User = append(response.User, directoryUser(user))
	}
	return response, nil
}

func (s *Service) UserFind(ctx context.Context, request *userv1.UserFindRequest) (*userv1.UserFindResponse, error) {
	if _, err := s.authenticate(ctx); err != nil {
		return nil, err
	}
	response := &userv1.UserFindResponse{}
	cursor := ""
	for {
		page, err := s.store.ListUsers(ctx, cursor, 100)
		if err != nil {
			return nil, status.Error(codes.Internal, "user lookup failed")
		}
		for _, user := range page.Items {
			switch query := request.Query.(type) {
			case *userv1.UserFindRequest_Username:
				if user.Username == query.Username {
					response.User = append(response.User, directoryUser(user))
				}
			case *userv1.UserFindRequest_DisplayName:
				if user.DisplayName == query.DisplayName {
					response.User = append(response.User, directoryUser(user))
				}
			default:
				return nil, status.Error(codes.InvalidArgument, "username or display_name is required")
			}
		}
		if page.NextCursor == "" {
			return response, nil
		}
		cursor = page.NextCursor
	}
}

func (s *Service) RepositoryList(request *userv1.RepositoryListRequest, stream grpc.ServerStreamingServer[userv1.RepositoryListResponse]) error {
	user, err := s.authenticate(stream.Context())
	if err != nil {
		return err
	}
	cursor := ""
	for {
		page, err := s.store.Catalog(stream.Context(), &user, cursor, 100)
		if err != nil {
			return status.Error(codes.Internal, "repository lookup failed")
		}
		for _, repository := range page.Items {
			grants, err := s.store.ListGrants(stream.Context(), repository.ID)
			if err != nil {
				return status.Error(codes.Internal, "permission lookup failed")
			}
			id, err := decodeUUID(repository.ID)
			if err != nil {
				return status.Error(codes.Internal, "repository ID is invalid")
			}
			permission := "read"
			switch {
			case policy.CanManage(&user, grants):
				permission = "owner"
			case policy.CanWrite(&user, grants):
				permission = "write"
			}
			if err := stream.Send(&userv1.RepositoryListResponse{Id: id, Permission: []string{permission}}); err != nil {
				return err
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

func (s *Service) RepositoryRegister(ctx context.Context, request *userv1.RepositoryRegisterRequest) (*userv1.RepositoryRegisterResponse, error) {
	user, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	id, err := formatUUID(request.Id)
	if err != nil || request.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "valid repository ID and name are required")
	}
	existing, err := s.store.GetRepository(ctx, id)
	if err == nil {
		if existing.Name != request.Name {
			return nil, status.Error(codes.AlreadyExists, "repository ID is registered under another name")
		}
		grants, err := s.store.ListGrants(ctx, id)
		if err != nil {
			return nil, status.Error(codes.Internal, "permission lookup failed")
		}
		if !policy.CanManage(&user, grants) {
			return nil, status.Error(codes.PermissionDenied, "repository owner access is required")
		}
		return &userv1.RepositoryRegisterResponse{}, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.Internal, "repository lookup failed")
	}
	_, err = s.store.CreateRepository(ctx, user.ID, model.Repository{ID: id, Name: request.Name, OwnerID: user.ID})
	if errors.Is(err, store.ErrConflict) {
		return nil, status.Error(codes.AlreadyExists, "repository name is already registered")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "repository registration failed")
	}
	return &userv1.RepositoryRegisterResponse{}, nil
}

func (s *Service) RepositoryUnregister(ctx context.Context, request *userv1.RepositoryUnregisterRequest) (*userv1.RepositoryUnregisterResponse, error) {
	user, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	id, err := formatUUID(request.Id)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "repository ID is invalid")
	}
	repository, err := s.store.GetRepository(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return &userv1.RepositoryUnregisterResponse{}, nil
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "repository lookup failed")
	}
	grants, err := s.store.ListGrants(ctx, repository.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, "permission lookup failed")
	}
	if !policy.CanManage(&user, grants) {
		return nil, status.Error(codes.PermissionDenied, "repository owner access is required")
	}
	if err := s.store.DeleteRepository(ctx, user.ID, id); err != nil {
		return nil, status.Error(codes.Internal, "repository unregistration failed")
	}
	return &userv1.RepositoryUnregisterResponse{}, nil
}

func (s *Service) authenticate(ctx context.Context) (model.User, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 {
		return model.User{}, status.Error(codes.Unauthenticated, "bearer token is required")
	}
	scheme, token, found := strings.Cut(values[0], " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || s.auth == nil {
		return model.User{}, status.Error(codes.Unauthenticated, "bearer token is required")
	}
	user, err := s.auth.AuthenticateBearer(ctx, token)
	if err != nil || user.Disabled {
		return model.User{}, status.Error(codes.Unauthenticated, "bearer token is invalid")
	}
	return user, nil
}

func directoryUser(user model.User) *userv1.User {
	return &userv1.User{Id: user.ID, Username: user.Username, DisplayName: user.DisplayName}
}

func decodeUUID(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	if err != nil || len(decoded) != 16 {
		return nil, errors.New("invalid UUID")
	}
	return decoded, nil
}

func formatUUID(value []byte) (string, error) {
	if len(value) != 16 {
		return "", errors.New("invalid UUID")
	}
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
