package loreclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	modelv1 "github.com/arutar/lore/platform/control-plane/gen/lore/model/v1"
	repositoryv1 "github.com/arutar/lore/platform/control-plane/gen/lore/repository/v1"
	revisionv1 "github.com/arutar/lore/platform/control-plane/gen/lore/revision/v1"
	storagev1 "github.com/arutar/lore/platform/control-plane/gen/lore/storage/v1"
	thinclientv1 "github.com/arutar/lore/platform/control-plane/gen/lore/thin_client/v1"
	lockv1 "github.com/arutar/lore/platform/control-plane/gen/urc/lock"
)

type contentServer struct {
	repositoryv1.UnimplementedRepositoryServiceServer
	revisionv1.UnimplementedRevisionServiceServer
	thinclientv1.UnimplementedThinClientServiceServer
	lockv1.UnimplementedLockServiceServer
	branch    []byte
	signature []byte
	hash      []byte
}

type storageContentServer struct {
	storagev1.UnimplementedStorageServiceServer
	content *contentServer
}

func (s *contentServer) RepositoryGet(context.Context, *repositoryv1.RepositoryGetRequest) (*repositoryv1.RepositoryGetResponse, error) {
	return &repositoryv1.RepositoryGetResponse{Repository: &modelv1.Repository{DefaultBranchId: s.branch}}, nil
}

func (s *contentServer) RevisionTree(request *thinclientv1.RevisionTreeRequest, stream thinclientv1.ThinClientService_RevisionTreeServer) error {
	if err := stream.Send(&thinclientv1.RevisionTreeResponse{Payload: &thinclientv1.RevisionTreeResponse_Header{Header: &thinclientv1.RevisionTreeHeader{Signature: s.signature, Identifier: &modelv1.RevisionIdentifier{BranchId: s.branch, Number: 1}}}}); err != nil {
		return err
	}
	return stream.Send(&thinclientv1.RevisionTreeResponse{Payload: &thinclientv1.RevisionTreeResponse_Node{Node: &thinclientv1.TreeNode{Path: "README.md", NodeType: thinclientv1.NodeType_FILE, Address: &modelv1.Address{Hash: s.hash, Context: []byte("context")}, Size: 5}}})
}

func (s *contentServer) RevisionList(context.Context, *revisionv1.RevisionListRequest) (*revisionv1.RevisionListResponse, error) {
	return &revisionv1.RevisionListResponse{Items: []*modelv1.RevisionItem{{Number: 1, Signature: s.signature}}}, nil
}

func (s *contentServer) RevisionInfo(context.Context, *thinclientv1.RevisionInfoRequest) (*thinclientv1.RevisionInfoResponse, error) {
	return &thinclientv1.RevisionInfoResponse{Revision: &thinclientv1.Revision{Number: 1, Signature: s.signature, CommitMessage: "Initial commit", CreatedBy: "author", CommittedBy: "committer", Timestamp: 1700000000000}}, nil
}

func (s *storageContentServer) Get(stream storagev1.StorageService_GetServer) error {
	address, err := stream.Recv()
	if err != nil {
		return err
	}
	if len(address.Context) == 0 {
		return errors.New("content context is missing")
	}
	return stream.Send(&storagev1.GetResponse{Address: address, Fragment: &modelv1.Fragment{SizePayload: 5, SizeContent: 5}, Payload: []byte("hello")})
}

func (s *contentServer) Query(ctx context.Context, _ *lockv1.QueryRequest) (*lockv1.QueryResponse, error) {
	if values := metadata.ValueFromIncomingContext(ctx, "urc-repository-id-bin"); len(values) != 1 || len(values[0]) != 16 {
		return nil, io.ErrUnexpectedEOF
	}
	return &lockv1.QueryResponse{Result: []*lockv1.Lock{{Owner: "user", LockedAt: timestamppb.New(time.Unix(1700000000, 0)), Resource: &lockv1.Resource{Branch: s.branch, Description: "README.md", Hash: s.hash}}}}, nil
}

func TestRepositoryContentRPCs(t *testing.T) {
	backend := &contentServer{branch: []byte("0123456789abcdef"), signature: []byte("revision-signature"), hash: []byte("0123456789abcdef0123456789abcdef")}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	repositoryv1.RegisterRepositoryServiceServer(server, backend)
	revisionv1.RegisterRevisionServiceServer(server, backend)
	thinclientv1.RegisterThinClientServiceServer(server, backend)
	storagev1.RegisterStorageServiceServer(server, &storageContentServer{content: backend})
	lockv1.RegisterLockServiceServer(server, backend)
	go server.Serve(listener)
	defer server.Stop()

	const repositoryID = "123e4567-e89b-42d3-a456-426614174000"
	client, err := New(Config{Address: listener.Addr().String(), TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "service-token"})})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	tree, err := client.Tree(t.Context(), repositoryID, "", "")
	if err != nil || len(tree.Items) != 1 || tree.Items[0].Path != "README.md" || tree.Revision == "" {
		t.Fatalf("tree = %+v, err = %v", tree, err)
	}
	history, err := client.History(t.Context(), repositoryID, "")
	if err != nil || len(history.Items) != 1 || history.Items[0].Message != "Initial commit" || history.Items[0].Author != "author" {
		t.Fatalf("history = %+v, err = %v", history, err)
	}
	locks, err := client.Locks(t.Context(), repositoryID)
	if err != nil || len(locks) != 1 || locks[0].Path != "README.md" {
		t.Fatalf("locks = %+v, err = %v", locks, err)
	}
	download, err := client.Download(t.Context(), repositoryID, "", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	defer download.Body.Close()
	payload, err := io.ReadAll(download.Body)
	if err != nil || string(payload) != "hello" || download.Name != "README.md" || download.Size != 5 {
		t.Fatalf("download = %q, name = %q, size = %d, err = %v", payload, download.Name, download.Size, err)
	}
	if _, err := client.Download(t.Context(), repositoryID, "", "../secret"); err == nil {
		t.Fatal("path traversal was accepted")
	}
}

func TestContentDecompression(t *testing.T) {
	payload := bytes.Repeat([]byte("Lore content "), 100)
	lz4Payload := make([]byte, lz4.CompressBlockBound(len(payload)))
	compressedSize, err := lz4.CompressBlock(payload, lz4Payload, nil)
	if err != nil || compressedSize == 0 {
		t.Fatalf("LZ4 compression failed: %v", err)
	}
	lz4Payload = lz4Payload[:compressedSize]
	var zstdPayload bytes.Buffer
	zstdWriter, err := zstd.NewWriter(&zstdPayload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zstdWriter.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zstdWriter.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		flags      uint32
		compressed []byte
	}{{"lz4", 2, lz4Payload}, {"zstd", 8, zstdPayload.Bytes()}} {
		t.Run(test.name, func(t *testing.T) {
			actual, err := expandPayload(&modelv1.Fragment{Flags: test.flags, SizeContent: uint64(len(payload))}, test.compressed)
			if err != nil || !bytes.Equal(actual, payload) {
				t.Fatalf("decompression failed: %v", err)
			}
		})
	}
}
