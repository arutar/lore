package loreclient

import (
	"context"
	"net"
	"reflect"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/grpc"

	modelv1 "github.com/arutar/lore/platform/control-plane/gen/lore/model/v1"
	thinclientv1 "github.com/arutar/lore/platform/control-plane/gen/lore/thin_client/v1"
)

// changesServer serves r1 (a branch's first revision: two files in a folder) and r2 (its
// child), whose diff mixes every kind of entry Lore sends.
type changesServer struct {
	thinclientv1.UnimplementedThinClientServiceServer
	diffs atomic.Int32
}

func (s *changesServer) RevisionInfo(_ context.Context, request *thinclientv1.RevisionInfoRequest) (*thinclientv1.RevisionInfoResponse, error) {
	switch string(request.GetSignature()) {
	case "r1":
		return &thinclientv1.RevisionInfoResponse{Revision: revision("r1", "alice", 1, "", "")}, nil
	case "r2":
		return &thinclientv1.RevisionInfoResponse{Revision: revision("r2", "bob", 2, "r1", "")}, nil
	}
	return &thinclientv1.RevisionInfoResponse{}, nil
}

func (s *changesServer) RevisionTree(request *thinclientv1.RevisionTreeRequest, stream thinclientv1.ThinClientService_RevisionTreeServer) error {
	for _, node := range []*thinclientv1.TreeNode{
		{Path: "art", NodeType: thinclientv1.NodeType_DIRECTORY},
		{Path: "art/a.png", NodeType: thinclientv1.NodeType_FILE},
		{Path: "art/b.png", NodeType: thinclientv1.NodeType_FILE},
	} {
		if err := stream.Send(&thinclientv1.RevisionTreeResponse{Payload: &thinclientv1.RevisionTreeResponse_Node{Node: node}}); err != nil {
			return err
		}
	}
	return nil
}

func (s *changesServer) RevisionDiff(request *thinclientv1.RevisionDiffRequest, stream thinclientv1.ThinClientService_RevisionDiffServer) error {
	s.diffs.Add(1)
	same := &modelv1.Address{Hash: []byte("same")}
	file := thinclientv1.NodeType_FILE
	for _, change := range []*thinclientv1.DiffChange{
		{Path: "art", Action: thinclientv1.Action_KEEP, NodeType: thinclientv1.NodeType_DIRECTORY},
		{Path: "art/a.png", Action: thinclientv1.Action_KEEP, NodeType: file, ContentFrom: &modelv1.Address{Hash: []byte("old")}, ContentTo: &modelv1.Address{Hash: []byte("new")}},
		{Path: "art/same.png", Action: thinclientv1.Action_KEEP, NodeType: file, ContentFrom: same, ContentTo: same},
		{Path: "art/b.png", Action: thinclientv1.Action_DELETE, NodeType: file},
		{Path: "art/new.png", Action: thinclientv1.Action_ADD, NodeType: file},
		{Path: "docs/b.png", PathFrom: "art/b2.png", Action: thinclientv1.Action_MOVE, NodeType: file},
		{Path: "linked/x.txt", Action: thinclientv1.Action_ADD, NodeType: file, LinkRepositoryIndex: 1},
		{Path: "docs", Action: thinclientv1.Action_ADD, NodeType: thinclientv1.NodeType_DIRECTORY},
	} {
		if err := stream.Send(&thinclientv1.RevisionDiffResponse{Payload: &thinclientv1.RevisionDiffResponse_Change{Change: change}}); err != nil {
			return err
		}
	}
	return nil
}

func TestRevisionChangesPageFilterAndCount(t *testing.T) {
	backend := &changesServer{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	thinclientv1.RegisterThinClientServiceServer(server, backend)
	go server.Serve(listener)
	defer server.Stop()
	client, err := New(Config{Address: listener.Addr().String(), TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "token"})})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	const repository = "123e4567-e89b-42d3-a456-426614174000"
	r2 := encodeHex("r2")

	all := []Change{
		{Kind: ChangeModified, Path: "art/a.png"},
		{Kind: ChangeDeleted, Path: "art/b.png"},
		{Kind: ChangeAdded, Path: "art/new.png"},
		{Kind: ChangeMoved, Path: "docs/b.png", From: "art/b2.png"},
	}
	first, err := client.Changes(t.Context(), repository, r2, ChangeFilter{}, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Items, all[:3]) || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	second, err := client.Changes(t.Context(), repository, r2, ChangeFilter{}, first.NextCursor, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second.Items, all[3:]) || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}

	// A filter covers the whole revision; the query matches a moved file's old path too.
	moved, err := client.Changes(t.Context(), repository, r2, ChangeFilter{Query: "B2"}, "", 10)
	if err != nil || !reflect.DeepEqual(moved.Items, all[3:]) {
		t.Fatalf("query page = %+v, %v", moved, err)
	}
	deleted, err := client.Changes(t.Context(), repository, r2, ChangeFilter{Kind: ChangeDeleted}, "", 10)
	if err != nil || !reflect.DeepEqual(deleted.Items, all[1:2]) {
		t.Fatalf("kind page = %+v, %v", deleted, err)
	}
	if _, err := client.Changes(t.Context(), repository, r2, ChangeFilter{}, "-1", 10); err == nil {
		t.Fatal("a negative cursor was accepted")
	}

	// A branch's first revision adds every file in it.
	root, err := client.Changes(t.Context(), repository, encodeHex("r1"), ChangeFilter{}, "", 10)
	if err != nil || !reflect.DeepEqual(root.Items, []Change{{Kind: ChangeAdded, Path: "art/a.png"}, {Kind: ChangeAdded, Path: "art/b.png"}}) {
		t.Fatalf("first revision = %+v, %v", root, err)
	}

	before := backend.diffs.Load()
	summary, err := client.ChangeSummary(t.Context(), repository, r2)
	if err != nil {
		t.Fatal(err)
	}
	want := ChangeSummary{Parent: encodeHex("r1"), Added: 1, Modified: 1, Deleted: 1, Moved: 1, Total: 4}
	if summary != want {
		t.Fatalf("summary = %+v, want %+v", summary, want)
	}
	if _, err := client.ChangeSummary(t.Context(), repository, r2); err != nil || backend.diffs.Load() != before+1 {
		t.Fatalf("a revision was counted again (err %v, diffs %d)", err, backend.diffs.Load()-before)
	}
}
