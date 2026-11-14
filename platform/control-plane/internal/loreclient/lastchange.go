package loreclient

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	thinclientv1 "github.com/arutar/lore/platform/control-plane/gen/lore/thin_client/v1"
)

// LastChange is the newest revision that changed a path; for a folder, the newest that changed
// anything under it. Author is the account ID Lore recorded (the caller shows the name).
type LastChange struct {
	Revision    string    `json:"revision"`
	Number      uint64    `json:"number"`
	Author      string    `json:"author"`
	Message     string    `json:"message"`
	CommittedAt time.Time `json:"committedAt"`
}

// lastChangeWalkLimit bounds the revisions one walk reads. A longer history leaves the paths
// it didn't reach without a last change instead of holding a request for minutes.
const lastChangeWalkLimit = 20000

// changeIndex knows, for one repository, the last change of every path changed in the
// revisions walked so far. Revisions never change, so the index only grows: when the branch
// moves on, only the new revisions are read.
type changeIndex struct {
	mu      sync.Mutex
	head    string
	walked  map[string]bool
	changes map[string]LastChange
	// root is the first revision; a path no later revision changed was last changed there.
	// Known only once a walk reached it (complete).
	root     *LastChange
	complete bool
}

// LastChanges returns the last change of each of paths as of head (a revision signature of the
// repository's branch). Paths no walk could attribute are left out.
func (c *Client) LastChanges(ctx context.Context, repositoryID, head string, paths []string) (map[string]LastChange, error) {
	id, err := decodeUUID(repositoryID)
	if err != nil {
		return nil, err
	}
	headSignature, err := hex.DecodeString(head)
	if err != nil || len(headSignature) == 0 {
		return nil, errors.New("invalid revision signature")
	}
	ctx, err = c.repositoryContext(ctx, id)
	if err != nil {
		return nil, err
	}
	value, _ := c.lastChanges.LoadOrStore(repositoryID, &changeIndex{walked: map[string]bool{}, changes: map[string]LastChange{}})
	index := value.(*changeIndex)
	index.mu.Lock()
	defer index.mu.Unlock()
	if index.head != head {
		if err := index.walk(ctx, thinclientv1.NewThinClientServiceClient(c.connection), headSignature); err != nil {
			return nil, err
		}
		index.head = head
	}
	result := make(map[string]LastChange, len(paths))
	for _, path := range paths {
		path = strings.Trim(path, "/")
		if change, ok := index.changes[path]; ok {
			result[path] = change
		} else if index.complete && index.root != nil {
			result[path] = *index.root
		}
	}
	return result, nil
}

// walk reads the revisions reachable from head that no earlier walk read, newest first. A
// revision's changes are its diff against its parent; a merge is credited only with what
// differs from both parents (what the merge itself decided), the rest belongs to the revisions
// on the side it came from, which the walk reaches through the other parent.
func (index *changeIndex) walk(ctx context.Context, client thinclientv1.ThinClientServiceClient, head []byte) error {
	queue := &revisionQueue{}
	queued := map[string]bool{}
	push := func(signature []byte) error {
		key := hex.EncodeToString(signature)
		if index.walked[key] || queued[key] {
			return nil
		}
		info, err := client.RevisionInfo(ctx, &thinclientv1.RevisionInfoRequest{Query: &thinclientv1.RevisionInfoRequest_Signature{Signature: signature}})
		if err != nil {
			return err
		}
		if info.Revision == nil {
			return errors.New("Lore returned an empty revision")
		}
		queued[key] = true
		heap.Push(queue, info.Revision)
		return nil
	}
	if err := push(head); err != nil {
		return err
	}
	firstWalk := len(index.walked) == 0
	for read := 0; queue.Len() > 0; read++ {
		if read >= lastChangeWalkLimit {
			if firstWalk {
				index.complete = false
			}
			return nil
		}
		revision := heap.Pop(queue).(*thinclientv1.Revision)
		change := LastChange{
			Revision: hex.EncodeToString(revision.Signature), Number: revision.Number, Author: revision.CreatedBy,
			Message: revision.CommitMessage, CommittedAt: time.UnixMilli(int64(revision.Timestamp)).UTC(),
		}
		self, other := revision.GetParentSelf().GetSignature(), revision.GetParentOther().GetSignature()
		if len(self) == 0 {
			if index.root == nil || change.CommittedAt.Before(index.root.CommittedAt) {
				root := change
				index.root = &root
			}
		} else {
			changed, err := diffPaths(ctx, client, self, revision.Signature)
			if err != nil {
				return err
			}
			if len(other) > 0 {
				fromOther, err := diffPaths(ctx, client, other, revision.Signature)
				if err != nil {
					return err
				}
				for path := range changed {
					if !fromOther[path] {
						delete(changed, path)
					}
				}
				if err := push(other); err != nil {
					return err
				}
			}
			for path := range changed {
				index.credit(path, change)
			}
			if err := push(self); err != nil {
				return err
			}
		}
		index.walked[change.Revision] = true
	}
	if firstWalk {
		index.complete = true
	}
	return nil
}

// credit records change for path and the folders above it, unless they have a newer one.
func (index *changeIndex) credit(path string, change LastChange) {
	for path != "" && path != "." {
		if current, ok := index.changes[path]; !ok || current.CommittedAt.Before(change.CommittedAt) {
			index.changes[path] = change
		}
		slash := strings.LastIndex(path, "/")
		if slash < 0 {
			return
		}
		path = path[:slash]
	}
}

// diffPaths returns the paths that differ between two revisions (both ends of a move or copy).
func diffPaths(ctx context.Context, client thinclientv1.ThinClientServiceClient, from, to []byte) (map[string]bool, error) {
	stream, err := client.RevisionDiff(ctx, &thinclientv1.RevisionDiffRequest{
		QueryFrom: &thinclientv1.RevisionDiffRequest_SignatureFrom{SignatureFrom: from},
		QueryTo:   &thinclientv1.RevisionDiffRequest_SignatureTo{SignatureTo: to},
	})
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return paths, nil
		}
		if err != nil {
			return nil, err
		}
		change := response.GetChange()
		if change == nil || change.LinkRepositoryIndex != 0 {
			continue
		}
		if change.Action == thinclientv1.Action_KEEP && change.ContentFrom != nil && change.ContentTo != nil &&
			bytes.Equal(change.ContentFrom.Hash, change.ContentTo.Hash) {
			continue
		}
		if path := strings.Trim(change.Path, "/"); path != "" {
			paths[path] = true
		}
		if path := strings.Trim(change.PathFrom, "/"); path != "" {
			paths[path] = true
		}
	}
}

// revisionQueue orders revisions newest first.
type revisionQueue []*thinclientv1.Revision

func (q revisionQueue) Len() int           { return len(q) }
func (q revisionQueue) Less(i, j int) bool { return q[i].Timestamp > q[j].Timestamp }
func (q revisionQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *revisionQueue) Push(value any)    { *q = append(*q, value.(*thinclientv1.Revision)) }
func (q *revisionQueue) Pop() any {
	old := *q
	item := old[len(old)-1]
	*q = old[:len(old)-1]
	return item
}
