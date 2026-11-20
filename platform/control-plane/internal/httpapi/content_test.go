package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/arutar/lore/platform/control-plane/internal/loreclient"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

type fakeRepositoryContent struct {
	calls    int
	released []string
	filter   loreclient.ChangeFilter
	cursor   string
	limit    int
}

func (f *fakeRepositoryContent) Changes(_ context.Context, _ string, revision string, filter loreclient.ChangeFilter, cursor string, limit int) (loreclient.ChangePage, error) {
	f.calls++
	f.filter, f.cursor, f.limit = filter, cursor, limit
	if revision != "abc123" {
		return loreclient.ChangePage{}, status.Error(codes.NotFound, "no such revision")
	}
	return loreclient.ChangePage{Items: []loreclient.Change{{Kind: loreclient.ChangeDeleted, Path: "Art/old.png"}}, NextCursor: "500"}, nil
}

func (f *fakeRepositoryContent) ChangeSummary(context.Context, string, string) (loreclient.ChangeSummary, error) {
	f.calls++
	return loreclient.ChangeSummary{Parent: "parent", Deleted: 1, Total: 1}, nil
}

func (f *fakeRepositoryContent) LastChanges(_ context.Context, _ string, head string, paths []string) (map[string]loreclient.LastChange, error) {
	f.calls++
	result := map[string]loreclient.LastChange{}
	for _, path := range paths {
		if path == "README.md" && head == "revision" {
			result[path] = loreclient.LastChange{Revision: "revision", Number: 3, Author: "author-id", Message: "Edit readme", CommittedAt: time.Unix(1700000000, 0).UTC()}
		}
	}
	return result, nil
}

func (f *fakeRepositoryContent) ReleaseLock(_ context.Context, _ string, branchID, hash string) (loreclient.RepositoryLock, error) {
	f.calls++
	if hash != "art-hash" {
		return loreclient.RepositoryLock{}, loreclient.ErrLockNotFound
	}
	f.released = append(f.released, branchID+"/"+hash)
	return loreclient.RepositoryLock{BranchID: branchID, Path: "art.psd", Hash: hash, Owner: "unknown-owner"}, nil
}

func (f *fakeRepositoryContent) Tree(context.Context, string, string, string) (loreclient.RepositoryTree, error) {
	f.calls++
	return loreclient.RepositoryTree{Revision: "revision", Items: []loreclient.TreeNode{{Path: "README.md", Type: "file", Size: 5}}}, nil
}

func (f *fakeRepositoryContent) History(context.Context, string, string) (loreclient.HistoryPage, error) {
	f.calls++
	return loreclient.HistoryPage{Items: []loreclient.Revision{{Number: 1, Message: "Initial commit", Author: "author-id", Committer: "author-id"}}}, nil
}

func (f *fakeRepositoryContent) Locks(context.Context, string) ([]loreclient.RepositoryLock, error) {
	f.calls++
	return []loreclient.RepositoryLock{{Path: "README.md", Owner: "author-id"}, {Path: "art.psd", Owner: "unknown-owner"}}, nil
}

func (f *fakeRepositoryContent) Download(context.Context, string, string, string) (*loreclient.Download, error) {
	f.calls++
	return &loreclient.Download{Name: "README.md", Size: 5, Body: io.NopCloser(strings.NewReader("hello"))}, nil
}

func TestRepositoryContentUsesWebPermissions(t *testing.T) {
	storage := store.NewMemory()
	storage.UpsertUser(t.Context(), model.User{ID: "author-id", Username: "alice", Role: model.RoleUser})
	auth, err := NewAuthenticator(context.Background(), storage, AuthConfig{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	content := &fakeRepositoryContent{}
	handler := NewServer(storage, auth, Config{Content: content, DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}})
	client := &testClient{t: t, handler: handler}
	client.login()
	status, public := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "public", "public": true}, true)
	if status != http.StatusCreated {
		t.Fatalf("public create status = %d", status)
	}
	status, private := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "private"}, true)
	if status != http.StatusCreated {
		t.Fatalf("private create status = %d", status)
	}

	anonymous := &testClient{t: t, handler: handler}
	for _, endpoint := range []string{"tree", "history", "locks"} {
		status, body := anonymous.request(http.MethodGet, "/web/v1/repositories/"+public["id"].(string)+"/"+endpoint, nil, false)
		if status != http.StatusOK || body["items"] == nil {
			t.Fatalf("anonymous %s status = %d, body = %v", endpoint, status, body)
		}
		if endpoint == "history" {
			revision := body["items"].([]any)[0].(map[string]any)
			if revision["author"] != "alice" || revision["committer"] != "alice" {
				t.Fatalf("history identity = %v", revision)
			}
		}
		if endpoint == "locks" {
			locks := body["items"].([]any)
			if owner := locks[0].(map[string]any)["owner"]; owner != "alice" {
				t.Fatalf("lock owner = %v, want the account name", owner)
			}
			if owner := locks[1].(map[string]any)["owner"]; owner != "unknown-owner" {
				t.Fatalf("unknown lock owner = %v, want it unchanged", owner)
			}
			if locks[0].(map[string]any)["ownerDisabled"] != nil || locks[1].(map[string]any)["ownerDisabled"] != true {
				t.Fatalf("owner states = %v, want only the unknown owner marked as gone", locks)
			}
			if body["canRelease"] != false {
				t.Fatalf("an anonymous visitor may release locks: %v", body["canRelease"])
			}
		}
		if endpoint == "tree" {
			change, _ := body["items"].([]any)[0].(map[string]any)["lastChange"].(map[string]any)
			if change == nil || change["author"] != "alice" || change["message"] != "Edit readme" || change["number"] != float64(3) {
				t.Fatalf("tree last change = %v, want alice's revision 3 by name", change)
			}
		}
	}
	calls := content.calls
	status, _ = anonymous.request(http.MethodGet, "/web/v1/repositories/"+private["id"].(string)+"/tree", nil, false)
	if status != http.StatusNotFound || content.calls != calls {
		t.Fatalf("private tree status = %d, content calls changed = %v", status, content.calls != calls)
	}

	// Releasing someone else's lock: owners and administrators only, recorded in the audit log.
	release := "/web/v1/repositories/" + public["id"].(string) + "/locks/release"
	if status, _ := anonymous.request(http.MethodPost, release, map[string]any{"branchId": "branch", "hash": "art-hash"}, true); status == http.StatusNoContent || len(content.released) != 0 {
		t.Fatalf("anonymous release status = %d, released = %v", status, content.released)
	}
	if status, _ := client.request(http.MethodPost, release, map[string]any{"branchId": "branch", "hash": "gone-hash"}, true); status != http.StatusNotFound {
		t.Fatalf("release of a lock that isn't held: status = %d", status)
	}
	if status, _ := client.request(http.MethodPost, release, map[string]any{"branchId": "branch"}, true); status != http.StatusBadRequest {
		t.Fatalf("release without a hash: status = %d", status)
	}
	if status, _ := client.request(http.MethodPost, release, map[string]any{"branchId": "branch", "hash": "art-hash"}, true); status != http.StatusNoContent || len(content.released) != 1 {
		t.Fatalf("admin release status = %d, released = %v", status, content.released)
	}
	status, audit := client.request(http.MethodGet, "/web/v1/admin/audit", nil, false)
	if status != http.StatusOK || !strings.Contains(fmtJSON(audit), `"lock.release"`) || !strings.Contains(fmtJSON(audit), "art.psd") {
		t.Fatalf("the release isn't in the audit log: %v", audit)
	}

	request := httptest.NewRequest(http.MethodGet, "/web/v1/repositories/"+public["id"].(string)+"/download?path=README.md", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "hello" || response.Header().Get("Content-Disposition") != `attachment; filename=README.md` {
		t.Fatalf("download status = %d, headers = %v, body = %q", response.Code, response.Header(), response.Body.String())
	}
}

func TestAccountNameUsesHumanIdentity(t *testing.T) {
	user := model.User{ID: "238b27e5-84bb-41b5-b1bb-b48597388a37", Username: "238b27e5-84bb-41b5-b1bb-b48597388a37", DisplayName: "Lore Administrator"}
	if name := accountName(user); name != "Lore Administrator" {
		t.Fatalf("account name = %q", name)
	}
	user.DisplayName = ""
	user.Email = "admin@example.com"
	if name := accountName(user); name != "admin@example.com" {
		t.Fatalf("account email = %q", name)
	}
}

func fmtJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestRevisionChangesFilterTheWholeRevision(t *testing.T) {
	storage := store.NewMemory()
	auth, err := NewAuthenticator(context.Background(), storage, AuthConfig{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	content := &fakeRepositoryContent{}
	handler := NewServer(storage, auth, Config{Content: content, DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}})
	client := &testClient{t: t, handler: handler}
	client.login()
	_, public := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "public", "public": true}, true)
	_, private := client.request(http.MethodPost, "/web/v1/admin/repositories", map[string]any{"name": "private"}, true)
	anonymous := &testClient{t: t, handler: handler}
	base := "/web/v1/repositories/" + public["id"].(string) + "/revisions/abc123"

	code, page := anonymous.request(http.MethodGet, base+"/changes?kind=deleted&q=%20old%20&cursor=500&limit=200", nil, false)
	if code != http.StatusOK || page["nextCursor"] != "500" {
		t.Fatalf("changes status = %d, body = %v", code, page)
	}
	if item := page["items"].([]any)[0].(map[string]any); item["kind"] != "deleted" || item["path"] != "Art/old.png" {
		t.Fatalf("change = %v", item)
	}
	if content.filter != (loreclient.ChangeFilter{Kind: "deleted", Query: "old"}) || content.cursor != "500" || content.limit != 200 {
		t.Fatalf("passed filter %+v, cursor %q, limit %d", content.filter, content.cursor, content.limit)
	}
	if code, summary := anonymous.request(http.MethodGet, base+"/summary", nil, false); code != http.StatusOK || summary["deleted"] != float64(1) || summary["parent"] != "parent" {
		t.Fatalf("summary status = %d, body = %v", code, summary)
	}
	if code, _ := anonymous.request(http.MethodGet, base+"/changes?kind=renamed", nil, false); code != http.StatusBadRequest {
		t.Fatalf("unknown kind status = %d", code)
	}
	if code, _ := anonymous.request(http.MethodGet, "/web/v1/repositories/"+public["id"].(string)+"/revisions/gone/changes", nil, false); code != http.StatusNotFound {
		t.Fatalf("unknown revision status = %d", code)
	}
	calls := content.calls
	if code, _ := anonymous.request(http.MethodGet, "/web/v1/repositories/"+private["id"].(string)+"/revisions/abc123/changes", nil, false); code != http.StatusNotFound || content.calls != calls {
		t.Fatalf("private changes status = %d, content read = %v", code, content.calls != calls)
	}
}
