package loreclient

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/grpc"

	thinclientv1 "github.com/arutar/lore/platform/control-plane/gen/lore/thin_client/v1"
)

// historyServer serves a small history:
//
//	r1 (alice, root: a.txt, art/tex.png, docs/x.md)
//	r2 (bob, parent r1: a.txt)
//	r3 (carol, parent r1, on another working copy: art/tex.png)
//	r4 (bob, merge of r2 and r3: takes r3's tex.png, changes nothing itself but docs/x.md)
//	r5 (alice, parent r4: renames docs/x.md to docs/y.md)  - added by the test later
type historyServer struct {
	thinclientv1.UnimplementedThinClientServiceServer
	revisions map[string]*thinclientv1.Revision
	diffs     map[string][]string
	diffCalls atomic.Int32
}

func (s *historyServer) RevisionInfo(_ context.Context, request *thinclientv1.RevisionInfoRequest) (*thinclientv1.RevisionInfoResponse, error) {
	return &thinclientv1.RevisionInfoResponse{Revision: s.revisions[string(request.GetSignature())]}, nil
}

func (s *historyServer) RevisionDiff(request *thinclientv1.RevisionDiffRequest, stream thinclientv1.ThinClientService_RevisionDiffServer) error {
	s.diffCalls.Add(1)
	for _, path := range s.diffs[string(request.GetSignatureFrom())+">"+string(request.GetSignatureTo())] {
		change := &thinclientv1.DiffChange{Path: path, Action: thinclientv1.Action_ADD}
		if path == "docs/y.md" {
			change = &thinclientv1.DiffChange{Path: path, PathFrom: "docs/x.md", Action: thinclientv1.Action_MOVE}
		}
		if err := stream.Send(&thinclientv1.RevisionDiffResponse{Payload: &thinclientv1.RevisionDiffResponse_Change{Change: change}}); err != nil {
			return err
		}
	}
	return nil
}

func revision(signature, author string, timestamp uint64, self, other string) *thinclientv1.Revision {
	result := &thinclientv1.Revision{Signature: []byte(signature), CreatedBy: author, CommitMessage: "message " + signature, Timestamp: timestamp, Number: timestamp}
	if self != "" {
		result.ParentSelf = &thinclientv1.Revision_Parent{Signature: []byte(self)}
	}
	if other != "" {
		result.ParentOther = &thinclientv1.Revision_Parent{Signature: []byte(other)}
	}
	return result
}

func TestLastChangesFollowMergesAndGrowWithTheBranch(t *testing.T) {
	backend := &historyServer{
		revisions: map[string]*thinclientv1.Revision{
			"r1": revision("r1", "alice", 1, "", ""),
			"r2": revision("r2", "bob", 2, "r1", ""),
			"r3": revision("r3", "carol", 3, "r1", ""),
			"r4": revision("r4", "bob", 4, "r2", "r3"),
			"r5": revision("r5", "alice", 5, "r4", ""),
		},
		diffs: map[string][]string{
			"r1>r2": {"a.txt"},
			"r1>r3": {"art/tex.png"},
			"r2>r4": {"art/tex.png", "docs/x.md"}, // the merge against its own side: carol's texture and its own edit
			"r3>r4": {"a.txt", "docs/x.md"},       // against the other side: bob's a.txt and its own edit
			"r4>r5": {"docs/y.md"},
		},
	}
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
	hexOf := encodeHex

	changes, err := client.LastChanges(t.Context(), repository, hexOf("r4"), []string{"a.txt", "art/tex.png", "art", "docs/x.md", "docs", "never-changed.txt"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a.txt": "bob", "art/tex.png": "carol", "art": "carol", "docs/x.md": "bob", "docs": "bob", "never-changed.txt": "alice"}
	for path, author := range want {
		if changes[path].Author != author {
			t.Errorf("%s: last changed by %q, want %q (all: %+v)", path, changes[path].Author, author, changes)
		}
	}
	if changes["art/tex.png"].Revision != hexOf("r3") {
		t.Errorf("a change that came in through a merge is credited to the merge: %+v", changes["art/tex.png"])
	}

	// The branch moves on: only the new revision is read, and the index follows it.
	before := backend.diffCalls.Load()
	changes, err = client.LastChanges(t.Context(), repository, hexOf("r5"), []string{"docs/y.md", "docs", "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if calls := backend.diffCalls.Load() - before; calls != 1 {
		t.Errorf("moving on by one revision read %d diffs, want 1", calls)
	}
	if changes["docs/y.md"].Author != "alice" || changes["docs"].Author != "alice" || changes["a.txt"].Author != "bob" {
		t.Errorf("after r5: %+v", changes)
	}
	// Asking again for the same revision reads nothing.
	before = backend.diffCalls.Load()
	if _, err := client.LastChanges(t.Context(), repository, hexOf("r5"), []string{"a.txt"}); err != nil || backend.diffCalls.Load() != before {
		t.Errorf("a repeated request read the history again (err %v)", err)
	}
}

func encodeHex(value string) string {
	const digits = "0123456789abcdef"
	result := make([]byte, 0, len(value)*2)
	for _, b := range []byte(value) {
		result = append(result, digits[b>>4], digits[b&15])
	}
	return string(result)
}
