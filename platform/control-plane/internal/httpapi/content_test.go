package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/loreclient"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

type fakeRepositoryContent struct {
	calls int
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
	return []loreclient.RepositoryLock{{Path: "README.md", Owner: "user"}}, nil
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
	}
	calls := content.calls
	status, _ = anonymous.request(http.MethodGet, "/web/v1/repositories/"+private["id"].(string)+"/tree", nil, false)
	if status != http.StatusNotFound || content.calls != calls {
		t.Fatalf("private tree status = %d, content calls changed = %v", status, content.calls != calls)
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
