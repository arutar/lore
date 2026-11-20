package loreclient

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"

	thinclientv1 "github.com/arutar/lore/platform/control-plane/gen/lore/thin_client/v1"
)

// Change kinds, as the WebUI shows them.
const (
	ChangeAdded    = "added"
	ChangeModified = "modified"
	ChangeDeleted  = "deleted"
	ChangeMoved    = "moved"
	ChangeCopied   = "copied"
)

// Change is one file a revision changed against its parent. From is the old path of a moved
// or copied file.
type Change struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	From string `json:"from,omitempty"`
}

// ChangeFilter narrows a revision's changes: Kind keeps one kind, Query keeps paths that
// contain it (ignoring case). Both empty keep everything.
type ChangeFilter struct {
	Kind  string
	Query string
}

func (f ChangeFilter) keeps(change Change) bool {
	if f.Kind != "" && change.Kind != f.Kind {
		return false
	}
	if f.Query == "" {
		return true
	}
	query := strings.ToLower(f.Query)
	return strings.Contains(strings.ToLower(change.Path), query) || strings.Contains(strings.ToLower(change.From), query)
}

// ChangePage is a page of a revision's changes in Lore's order. NextCursor continues it.
type ChangePage struct {
	Items      []Change `json:"items"`
	NextCursor string   `json:"nextCursor,omitempty"`
}

// ChangeSummary counts a revision's changes by kind.
type ChangeSummary struct {
	Parent   string `json:"parent,omitempty"`
	Added    uint64 `json:"added"`
	Modified uint64 `json:"modified"`
	Deleted  uint64 `json:"deleted"`
	Moved    uint64 `json:"moved"`
	Copied   uint64 `json:"copied"`
	Total    uint64 `json:"total"`
}

// maxChangePage bounds one page; a revision may change hundreds of thousands of files.
const maxChangePage = 1000

// changeSummaryCacheSize bounds the summaries kept. Revisions never change, so a summary
// stays right; counting one again only costs another pass over its changes.
const changeSummaryCacheSize = 2048

type summaryCache struct {
	mu      sync.Mutex
	entries map[string]ChangeSummary
}

func (s *summaryCache) get(key string) (ChangeSummary, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	summary, ok := s.entries[key]
	return summary, ok
}

func (s *summaryCache) put(key string, summary ChangeSummary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[string]ChangeSummary{}
	}
	if len(s.entries) >= changeSummaryCacheSize {
		for other := range s.entries {
			delete(s.entries, other)
			break
		}
	}
	s.entries[key] = summary
}

// Changes returns up to limit of the files revision changed against its parent that filter
// keeps, starting after cursor (from a previous page). Lore streams a revision's changes in a
// fixed order, so a page is found by reading the stream up to it.
func (c *Client) Changes(ctx context.Context, repositoryID, revision string, filter ChangeFilter, cursor string, limit int) (ChangePage, error) {
	if limit <= 0 || limit > maxChangePage {
		limit = maxChangePage
	}
	skip := 0
	if cursor != "" {
		value, err := decodeCursor(cursor)
		if err != nil {
			return ChangePage{}, err
		}
		skip = value
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	page := ChangePage{Items: []Change{}}
	matched := 0
	_, err := c.eachChange(ctx, repositoryID, revision, func(change Change) bool {
		if !filter.keeps(change) {
			return true
		}
		matched++
		if matched <= skip {
			return true
		}
		if len(page.Items) == limit {
			page.NextCursor = encodeCursor(skip + limit)
			return false
		}
		page.Items = append(page.Items, change)
		return true
	})
	if err != nil {
		return ChangePage{}, err
	}
	return page, nil
}

// ChangeSummary counts the files revision changed against its parent, by kind.
func (c *Client) ChangeSummary(ctx context.Context, repositoryID, revision string) (ChangeSummary, error) {
	key := repositoryID + "/" + revision
	if summary, ok := c.summaries.get(key); ok {
		return summary, nil
	}
	var summary ChangeSummary
	parent, err := c.eachChange(ctx, repositoryID, revision, func(change Change) bool {
		summary.Total++
		switch change.Kind {
		case ChangeAdded:
			summary.Added++
		case ChangeModified:
			summary.Modified++
		case ChangeDeleted:
			summary.Deleted++
		case ChangeMoved:
			summary.Moved++
		case ChangeCopied:
			summary.Copied++
		}
		return true
	})
	if err != nil {
		return ChangeSummary{}, err
	}
	summary.Parent = parent
	c.summaries.put(key, summary)
	return summary, nil
}

// eachChange calls visit with each file revision changed against its own-branch parent, until
// visit returns false. A branch's first revision has no parent: every file in it is added.
// Folders are left out; a merge is compared with the parent on its own branch. Returns the
// parent's signature, empty for a first revision.
func (c *Client) eachChange(ctx context.Context, repositoryID, revision string, visit func(Change) bool) (string, error) {
	id, err := decodeUUID(repositoryID)
	if err != nil {
		return "", err
	}
	signature, err := hex.DecodeString(revision)
	if err != nil || len(signature) == 0 {
		return "", errors.New("invalid revision signature")
	}
	ctx, err = c.repositoryContext(ctx, id)
	if err != nil {
		return "", err
	}
	client := thinclientv1.NewThinClientServiceClient(c.connection)
	info, err := client.RevisionInfo(ctx, &thinclientv1.RevisionInfoRequest{Query: &thinclientv1.RevisionInfoRequest_Signature{Signature: signature}})
	if err != nil {
		return "", err
	}
	if info.Revision == nil {
		return "", errors.New("Lore returned an empty revision")
	}
	if info.Revision.ParentSelf == nil {
		return "", eachAddedFile(ctx, client, signature, visit)
	}
	parent := info.Revision.ParentSelf.Signature
	stream, err := client.RevisionDiff(ctx, &thinclientv1.RevisionDiffRequest{
		QueryFrom: &thinclientv1.RevisionDiffRequest_SignatureFrom{SignatureFrom: parent},
		QueryTo:   &thinclientv1.RevisionDiffRequest_SignatureTo{SignatureTo: signature},
	})
	if err != nil {
		return "", err
	}
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(parent), nil
		}
		if err != nil {
			return "", err
		}
		change, ok := fileChange(response.GetChange())
		if ok && !visit(change) {
			return hex.EncodeToString(parent), nil
		}
	}
}

// fileChange turns a diff entry into a Change; false for folders, entries of linked
// repositories and paths whose content didn't change.
func fileChange(entry *thinclientv1.DiffChange) (Change, bool) {
	if entry == nil || entry.LinkRepositoryIndex != 0 || entry.NodeType == thinclientv1.NodeType_DIRECTORY {
		return Change{}, false
	}
	change := Change{Path: strings.Trim(entry.Path, "/")}
	switch entry.Action {
	case thinclientv1.Action_ADD:
		change.Kind = ChangeAdded
	case thinclientv1.Action_DELETE:
		change.Kind = ChangeDeleted
	case thinclientv1.Action_MOVE:
		change.Kind, change.From = ChangeMoved, strings.Trim(entry.PathFrom, "/")
	case thinclientv1.Action_COPY:
		change.Kind, change.From = ChangeCopied, strings.Trim(entry.PathFrom, "/")
	default:
		if entry.ContentFrom != nil && entry.ContentTo != nil && string(entry.ContentFrom.Hash) == string(entry.ContentTo.Hash) {
			return Change{}, false
		}
		change.Kind = ChangeModified
	}
	return change, change.Path != ""
}

func eachAddedFile(ctx context.Context, client thinclientv1.ThinClientServiceClient, signature []byte, visit func(Change) bool) error {
	stream, err := client.RevisionTree(ctx, &thinclientv1.RevisionTreeRequest{Query: &thinclientv1.RevisionTreeRequest_Signature{Signature: signature}})
	if err != nil {
		return err
	}
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		node := response.GetNode()
		if node == nil || node.NodeType == thinclientv1.NodeType_DIRECTORY {
			continue
		}
		if path := strings.Trim(node.Path, "/"); path != "" && !visit(Change{Kind: ChangeAdded, Path: path}) {
			return nil
		}
	}
}

func encodeCursor(offset int) string { return strconv.Itoa(offset) }

func decodeCursor(cursor string) (int, error) {
	offset, err := strconv.Atoi(cursor)
	if err != nil || offset < 0 {
		return 0, errors.New("invalid changes cursor")
	}
	return offset, nil
}
