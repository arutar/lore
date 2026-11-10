package loreclient

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	modelv1 "github.com/arutar/lore/platform/control-plane/gen/lore/model/v1"
	repositoryv1 "github.com/arutar/lore/platform/control-plane/gen/lore/repository/v1"
	revisionv1 "github.com/arutar/lore/platform/control-plane/gen/lore/revision/v1"
	storagev1 "github.com/arutar/lore/platform/control-plane/gen/lore/storage/v1"
	thinclientv1 "github.com/arutar/lore/platform/control-plane/gen/lore/thin_client/v1"
	lockv1 "github.com/arutar/lore/platform/control-plane/gen/urc/lock"
)

const maxLeafFragmentSize = 256 * 1024

type TreeNode struct {
	Path       string           `json:"path"`
	Type       string           `json:"type"`
	Size       uint64           `json:"size"`
	Mode       uint64           `json:"mode"`
	Executable bool             `json:"executable"`
	Hash       string           `json:"hash,omitempty"`
	Context    string           `json:"context,omitempty"`
	Address    *modelv1.Address `json:"-"`
}

type RepositoryTree struct {
	Revision string     `json:"revision"`
	BranchID string     `json:"branchId"`
	Items    []TreeNode `json:"items"`
}

type Revision struct {
	Number      uint64    `json:"number"`
	Signature   string    `json:"signature"`
	Message     string    `json:"message"`
	Author      string    `json:"author"`
	Committer   string    `json:"committer"`
	CommittedAt time.Time `json:"committedAt"`
}

type HistoryPage struct {
	Items      []Revision `json:"items"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

type RepositoryLock struct {
	BranchID string    `json:"branchId"`
	Path     string    `json:"path"`
	Hash     string    `json:"hash"`
	Owner    string    `json:"owner"`
	LockedAt time.Time `json:"lockedAt"`
}

type Download struct {
	Name string
	Size int64
	Body io.ReadCloser
}

func (c *Client) Tree(ctx context.Context, repositoryID, revision, prefix string) (RepositoryTree, error) {
	if !validRepositoryPath(prefix) {
		return RepositoryTree{}, errors.New("invalid repository path")
	}
	id, err := decodeUUID(repositoryID)
	if err != nil {
		return RepositoryTree{}, err
	}
	ctx, err = c.repositoryContext(ctx, id)
	if err != nil {
		return RepositoryTree{}, err
	}
	request := &thinclientv1.RevisionTreeRequest{MaxDepth: uint32Ptr(1)}
	if prefix != "" {
		request.PathPrefix = &prefix
	}
	if revision != "" {
		signature, err := hex.DecodeString(revision)
		if err != nil || len(signature) == 0 {
			return RepositoryTree{}, errors.New("invalid revision signature")
		}
		request.Query = &thinclientv1.RevisionTreeRequest_Signature{Signature: signature}
	} else {
		branch, err := c.defaultBranch(ctx, id)
		if err != nil {
			return RepositoryTree{}, err
		}
		request.Query = &thinclientv1.RevisionTreeRequest_Identifier{Identifier: &modelv1.RevisionIdentifier{BranchId: branch}}
	}
	stream, err := thinclientv1.NewThinClientServiceClient(c.connection).RevisionTree(ctx, request)
	if err != nil {
		if revision == "" && status.Code(err) == codes.InvalidArgument {
			return RepositoryTree{Items: []TreeNode{}}, nil
		}
		return RepositoryTree{}, err
	}
	result := RepositoryTree{Items: []TreeNode{}}
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if revision == "" && prefix == "" && status.Code(err) == codes.InvalidArgument {
				return RepositoryTree{Items: []TreeNode{}}, nil
			}
			return RepositoryTree{}, err
		}
		if header := response.GetHeader(); header != nil {
			result.Revision = hex.EncodeToString(header.Signature)
			if header.Identifier != nil {
				result.BranchID = formatUUID(header.Identifier.BranchId)
			}
		}
		if node := response.GetNode(); node != nil {
			kind := "file"
			if node.NodeType == thinclientv1.NodeType_DIRECTORY {
				kind = "directory"
			} else if node.NodeType == thinclientv1.NodeType_LINK {
				kind = "link"
			}
			result.Items = append(result.Items, TreeNode{Path: node.Path, Type: kind, Size: node.Size, Mode: node.Mode, Executable: node.Mode == uint64(thinclientv1.FileMode_EXECUTABLE), Hash: hex.EncodeToString(node.GetAddress().GetHash()), Context: hex.EncodeToString(node.GetAddress().GetContext()), Address: node.Address})
		}
	}
	sort.Slice(result.Items, func(i, j int) bool {
		if result.Items[i].Type == "directory" && result.Items[j].Type != "directory" {
			return true
		}
		if result.Items[j].Type == "directory" && result.Items[i].Type != "directory" {
			return false
		}
		return result.Items[i].Path < result.Items[j].Path
	})
	return result, nil
}

func (c *Client) History(ctx context.Context, repositoryID, cursor string) (HistoryPage, error) {
	id, err := decodeUUID(repositoryID)
	if err != nil {
		return HistoryPage{}, err
	}
	ctx, err = c.repositoryContext(ctx, id)
	if err != nil {
		return HistoryPage{}, err
	}
	request := &revisionv1.RevisionListRequest{}
	if cursor != "" {
		signature, err := hex.DecodeString(cursor)
		if err != nil || len(signature) == 0 {
			return HistoryPage{}, errors.New("invalid history cursor")
		}
		request.Start = &revisionv1.RevisionListRequest_Signature{Signature: signature}
	} else {
		branch, err := c.defaultBranch(ctx, id)
		if err != nil {
			return HistoryPage{}, err
		}
		request.Start = &revisionv1.RevisionListRequest_Identifier{Identifier: &modelv1.RevisionIdentifier{BranchId: branch}}
	}
	response, err := revisionv1.NewRevisionServiceClient(c.connection).RevisionList(ctx, request)
	if err != nil {
		return HistoryPage{}, err
	}
	result := HistoryPage{Items: []Revision{}}
	thinClient := thinclientv1.NewThinClientServiceClient(c.connection)
	for _, item := range response.Items {
		info, err := thinClient.RevisionInfo(ctx, &thinclientv1.RevisionInfoRequest{Query: &thinclientv1.RevisionInfoRequest_Signature{Signature: item.Signature}})
		if err != nil {
			return HistoryPage{}, err
		}
		revision := info.Revision
		if revision == nil {
			return HistoryPage{}, errors.New("Lore returned an empty revision")
		}
		result.Items = append(result.Items, Revision{
			Number: revision.Number, Signature: hex.EncodeToString(revision.Signature), Message: revision.CommitMessage,
			Author: revision.CreatedBy, Committer: revision.CommittedBy, CommittedAt: time.UnixMilli(int64(revision.Timestamp)).UTC(),
		})
	}
	if response.SignatureBackward != nil {
		result.NextCursor = hex.EncodeToString(response.SignatureBackward)
	}
	return result, nil
}

func (c *Client) Locks(ctx context.Context, repositoryID string) ([]RepositoryLock, error) {
	id, err := decodeUUID(repositoryID)
	if err != nil {
		return nil, err
	}
	ctx, err = c.repositoryContext(ctx, id)
	if err != nil {
		return nil, err
	}
	response, err := lockv1.NewLockServiceClient(c.connection).Query(ctx, &lockv1.QueryRequest{})
	if err != nil {
		return nil, err
	}
	result := make([]RepositoryLock, 0, len(response.Result))
	for _, lock := range response.Result {
		item := RepositoryLock{Owner: lock.Owner}
		if lock.Resource != nil {
			item.BranchID = formatUUID(lock.Resource.Branch)
			item.Path = lock.Resource.Description
			item.Hash = hex.EncodeToString(lock.Resource.Hash)
		}
		if lock.LockedAt != nil {
			item.LockedAt = lock.LockedAt.AsTime().UTC()
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (c *Client) Download(ctx context.Context, repositoryID, revision, filePath string) (*Download, error) {
	if filePath == "" || !validRepositoryPath(filePath) {
		return nil, errors.New("invalid repository path")
	}
	parent := path.Dir(filePath)
	if parent == "." {
		parent = ""
	}
	tree, err := c.Tree(ctx, repositoryID, revision, parent)
	if err != nil {
		return nil, err
	}
	var node *TreeNode
	for i := range tree.Items {
		if tree.Items[i].Path == filePath && tree.Items[i].Type == "file" {
			node = &tree.Items[i]
			break
		}
	}
	if node == nil {
		return nil, fmt.Errorf("file %q wasn't found in revision", filePath)
	}
	if node.Size == 0 {
		return &Download{Name: path.Base(node.Path), Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	if node.Address == nil || len(node.Address.Hash) != 32 {
		return nil, errors.New("Lore returned an invalid content address")
	}
	id, err := decodeUUID(repositoryID)
	if err != nil {
		return nil, err
	}
	ctx, err = c.repositoryContext(ctx, id)
	if err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	go func() {
		var written uint64
		err := c.writeContent(ctx, node.Address, writer, &written, 0)
		if err == nil && written != node.Size {
			err = fmt.Errorf("content size mismatch: wrote %d bytes, expected %d", written, node.Size)
		}
		_ = writer.CloseWithError(err)
	}()
	return &Download{Name: path.Base(node.Path), Size: int64(node.Size), Body: reader}, nil
}

func (c *Client) writeContent(ctx context.Context, address *modelv1.Address, output io.Writer, written *uint64, depth int) error {
	if depth > 16 {
		return errors.New("content fragment tree is too deep")
	}
	fragment, payload, err := c.fetchFragment(ctx, address)
	if err != nil {
		return err
	}
	if fragment.Flags&1 != 0 {
		if fragment.Flags&0xfe != 0 || len(payload)%40 != 0 || len(payload) == 0 {
			return errors.New("invalid content fragment list")
		}
		for offset := 0; offset < len(payload); offset += 40 {
			if binary.LittleEndian.Uint64(payload[offset+32:offset+40]) != *written {
				return errors.New("invalid content fragment offset")
			}
			child := &modelv1.Address{Hash: append([]byte(nil), payload[offset:offset+32]...), Context: address.Context}
			if err := c.writeContent(ctx, child, output, written, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if fragment.SizeContent > maxLeafFragmentSize {
		return errors.New("content leaf exceeds the Lore fragment limit")
	}
	expanded, err := expandPayload(fragment, payload)
	if err != nil {
		return err
	}
	count, err := output.Write(expanded)
	*written += uint64(count)
	return err
}

func (c *Client) fetchFragment(ctx context.Context, address *modelv1.Address) (*modelv1.Fragment, []byte, error) {
	stream, err := storagev1.NewStorageServiceClient(c.connection).Get(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := stream.Send(address); err != nil {
		return nil, nil, err
	}
	if err := stream.CloseSend(); err != nil {
		return nil, nil, err
	}
	response, err := stream.Recv()
	if err != nil {
		return nil, nil, err
	}
	if response.Status != nil && response.Status.Code != 0 {
		return nil, nil, fmt.Errorf("content fragment read failed: %s", response.Status.Message)
	}
	fragment := response.Fragment
	if fragment == nil || fragment.SizePayload > maxLeafFragmentSize || len(response.Payload) != int(fragment.SizePayload) {
		return nil, nil, errors.New("invalid content fragment response")
	}
	return fragment, response.Payload, nil
}

func expandPayload(fragment *modelv1.Fragment, payload []byte) ([]byte, error) {
	switch fragment.Flags & 0xfe {
	case 0:
		if uint64(len(payload)) != fragment.SizeContent {
			return nil, errors.New("plain content fragment size mismatch")
		}
		return payload, nil
	case 2:
		output := make([]byte, int(fragment.SizeContent))
		count, err := lz4.UncompressBlock(payload, output)
		if err != nil || count != len(output) {
			return nil, errors.New("invalid LZ4 content fragment")
		}
		return output, nil
	case 8:
		decoder, err := zstd.NewReader(nil)
		if err != nil {
			return nil, err
		}
		defer decoder.Close()
		output, err := decoder.DecodeAll(payload, make([]byte, 0, int(fragment.SizeContent)))
		if err != nil || uint64(len(output)) != fragment.SizeContent {
			return nil, errors.New("invalid Zstandard content fragment")
		}
		return output, nil
	default:
		return nil, errors.New("unsupported content compression")
	}
}

func (c *Client) repositoryContext(ctx context.Context, id []byte) (context.Context, error) {
	token, err := c.tokens.Token()
	if err != nil {
		return nil, err
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token.AccessToken, "urc-repository-id-bin", string(id)), nil
}

func (c *Client) defaultBranch(ctx context.Context, id []byte) ([]byte, error) {
	response, err := repositoryv1.NewRepositoryServiceClient(c.connection).RepositoryGet(ctx, &repositoryv1.RepositoryGetRequest{Query: &repositoryv1.RepositoryGetRequest_Id{Id: id}})
	if err != nil {
		return nil, err
	}
	if response.Repository == nil || len(response.Repository.DefaultBranchId) != 16 {
		return nil, errors.New("Lore returned an invalid default branch")
	}
	return response.Repository.DefaultBranchId, nil
}

func validRepositoryPath(value string) bool {
	if value == "" {
		return true
	}
	return !strings.HasPrefix(value, "/") && !strings.Contains(value, "\\") && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}

func formatUUID(value []byte) string {
	if len(value) != 16 {
		return ""
	}
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func uint32Ptr(value uint32) *uint32 {
	return &value
}
