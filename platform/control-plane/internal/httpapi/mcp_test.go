package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"

	"github.com/arutar/lore/platform/control-plane/internal/loreclient"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

// mcpContent is repository content made of files in memory, the same in every repository.
type mcpContent struct {
	fakeRepositoryContent
	files map[string][]byte
}

func (c *mcpContent) Tree(_ context.Context, _ string, revision, prefix string) (loreclient.RepositoryTree, error) {
	if revision == "" {
		revision = "head"
	}
	tree := loreclient.RepositoryTree{Revision: revision, Items: []loreclient.TreeNode{}}
	folders := map[string]bool{}
	for name, data := range c.files {
		parent := path.Dir(name)
		if parent == "." {
			parent = ""
		}
		if parent == prefix {
			tree.Items = append(tree.Items, loreclient.TreeNode{Path: name, Type: "file", Size: uint64(len(data))})
		} else if prefix == "" && !folders[strings.Split(name, "/")[0]] {
			folders[strings.Split(name, "/")[0]] = true
			tree.Items = append(tree.Items, loreclient.TreeNode{Path: strings.Split(name, "/")[0], Type: "directory"})
		}
	}
	sort.Slice(tree.Items, func(i, j int) bool { return tree.Items[i].Path < tree.Items[j].Path })
	return tree, nil
}

func (c *mcpContent) Download(_ context.Context, _, _, filePath string) (*loreclient.Download, error) {
	data, ok := c.files[filePath]
	if !ok {
		return nil, errFileNotFound
	}
	return &loreclient.Download{Name: path.Base(filePath), Size: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data))}, nil
}

type mcpFixture struct {
	t        *testing.T
	handler  http.Handler
	storage  *store.Memory
	content  *mcpContent
	public   model.Repository
	private  model.Repository
	readable model.Repository
}

func encode(t *testing.T, text string, encoder interface{ String(string) (string, error) }) []byte {
	t.Helper()
	encoded, err := encoder.String(text)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(encoded)
}

func newMCPFixture(t *testing.T, config MCPConfig) *mcpFixture {
	t.Helper()
	storage := store.NewMemory()
	ctx := t.Context()
	for _, user := range []model.User{
		{ID: "owner", Username: "owner", Role: model.RoleUser},
		{ID: "reader", Username: "reader", Role: model.RoleUser},
		{ID: "admin", Username: "admin", Role: model.RoleAdmin},
	} {
		if _, err := storage.UpsertUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := storage.UpdateUser(ctx, "", "admin", model.RoleAdmin, false); err != nil {
		t.Fatal(err)
	}
	create := func(id, name string, public bool) model.Repository {
		repository, err := storage.CreateRepository(ctx, "owner", model.Repository{ID: id, Name: name, OwnerID: "owner", Public: public})
		if err != nil {
			t.Fatal(err)
		}
		return repository
	}
	fixture := &mcpFixture{t: t, storage: storage,
		public:   create("11111111-1111-1111-1111-111111111111", "team/public", true),
		private:  create("22222222-2222-2222-2222-222222222222", "team/private", false),
		readable: create("33333333-3333-3333-3333-333333333333", "team/shared", false),
	}
	if err := storage.SetGrant(ctx, "owner", model.Grant{RepositoryID: fixture.readable.ID, SubjectType: model.SubjectUser, Subject: "reader", Permission: model.PermissionRead}); err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	for number := 1; number <= 30; number++ {
		lines.WriteString("line ")
		lines.WriteString(strings.Repeat("x", number%3))
		if number%10 == 0 {
			lines.WriteString(" ERROR here")
		}
		lines.WriteString("\n")
	}
	fixture.content = &mcpContent{files: map[string][]byte{
		"README.md":       []byte("# Hello\nsecond line\n"),
		"logs/build.log":  []byte(lines.String()),
		"art/hero.fbx":    append([]byte("FBX\x00binary"), make([]byte, 64)...),
		"docs/legacy.txt": encode(t, "Привет, мир\nвторая строка\n", charmap.Windows1251.NewEncoder()),
		"docs/wide.txt":   encode(t, "Wide text\nсторока два\n", unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder()),
	}}
	auth, err := NewAuthenticator(context.Background(), storage, AuthConfig{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode == "" {
		config.Mode = MCPOn
	}
	fixture.handler = NewServer(storage, auth, Config{Content: fixture.content, ExternalURL: "https://lore.example", MCP: config,
		DevAuth: true, DevUser: model.User{ID: "admin", Username: "admin", Role: model.RoleAdmin}})
	return fixture
}

// token issues a token for the account straight in the store and returns its secret.
func (f *mcpFixture) token(userID string, repositories ...string) (string, model.MCPToken) {
	f.t.Helper()
	secret, err := newMCPSecret()
	if err != nil {
		f.t.Fatal(err)
	}
	token := model.MCPToken{ID: "token-" + secret[len(secret)-8:], UserID: userID, Name: "test", Hint: secret[:13], Repositories: repositories, CreatedAt: time.Now().UTC()}
	if err := f.storage.CreateMCPToken(f.t.Context(), token, secretHash(secret)); err != nil {
		f.t.Fatal(err)
	}
	return secret, token
}

func (f *mcpFixture) post(secret string, message map[string]any, configure ...func(*http.Request)) (int, map[string]any) {
	f.t.Helper()
	body, _ := json.Marshal(message)
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	for _, change := range configure {
		change(request)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	var decoded map[string]any
	if response.Body.Len() > 0 {
		_ = json.Unmarshal(response.Body.Bytes(), &decoded)
	}
	return response.Code, decoded
}

// call runs a tool and returns its text and whether it's an error.
func (f *mcpFixture) call(secret, tool string, arguments map[string]any) (string, bool) {
	f.t.Helper()
	status, body := f.post(secret, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": arguments}})
	if status != http.StatusOK {
		f.t.Fatalf("%s status = %d, body = %v", tool, status, body)
	}
	result, ok := body["result"].(map[string]any)
	if !ok {
		f.t.Fatalf("%s answered %v", tool, body)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	return text, result["isError"] == true
}

func TestMCPIsOffUnlessConfigured(t *testing.T) {
	fixture := newMCPFixture(t, MCPConfig{Mode: MCPOff})
	secret, _ := fixture.token("reader")
	status, _ := fixture.post(secret, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if status != http.StatusNotFound {
		t.Fatalf("MCP off: status = %d, want 404", status)
	}
}

func TestMCPNeedsAUsableToken(t *testing.T) {
	fixture := newMCPFixture(t, MCPConfig{})
	ping := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}
	for name, secret := range map[string]string{"none": "", "not an MCP token": "eyJhbGciOi", "unknown": mcpTokenPrefix + "unknown"} {
		if status, _ := fixture.post(secret, ping); status != http.StatusUnauthorized {
			t.Fatalf("%s token: status = %d, want 401", name, status)
		}
	}
	// A signed-in browser session doesn't stand in for a token.
	client := &testClient{t: t, handler: fixture.handler}
	client.login()
	if status, _ := fixture.post("", ping, func(r *http.Request) { r.AddCookie(client.cookie) }); status != http.StatusUnauthorized {
		t.Fatalf("session cookie without a token: status = %d, want 401", status)
	}

	secret, token := fixture.token("reader")
	status, body := fixture.post(secret, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}})
	if status != http.StatusOK || body["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: status = %d, body = %v", status, body)
	}
	if status, _ := fixture.post(secret, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); status != http.StatusAccepted {
		t.Fatalf("notification: status = %d, want 202", status)
	}
	if status, _ := fixture.post(secret, ping, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); status != http.StatusForbidden {
		t.Fatalf("request from another site: status = %d, want 403", status)
	}
	stored, _ := fixture.storage.GetMCPToken(t.Context(), token.ID)
	if stored.LastUsedAt == nil {
		t.Fatal("the token's last use wasn't recorded")
	}

	if err := fixture.storage.RevokeMCPToken(t.Context(), "reader", token.ID); err != nil {
		t.Fatal(err)
	}
	if status, _ := fixture.post(secret, ping); status != http.StatusUnauthorized {
		t.Fatalf("revoked token: status = %d, want 401", status)
	}

	past := time.Now().Add(-time.Hour)
	if err := fixture.storage.CreateMCPToken(t.Context(), model.MCPToken{ID: "expired", UserID: "reader", Name: "old", ExpiresAt: &past}, secretHash(mcpTokenPrefix+"expired")); err != nil {
		t.Fatal(err)
	}
	if status, _ := fixture.post(mcpTokenPrefix+"expired", ping); status != http.StatusUnauthorized {
		t.Fatalf("expired token: status = %d, want 401", status)
	}

	disabledSecret, _ := fixture.token("owner")
	if _, err := fixture.storage.UpdateUser(t.Context(), "admin", "owner", model.RoleUser, true); err != nil {
		t.Fatal(err)
	}
	if status, _ := fixture.post(disabledSecret, ping); status != http.StatusUnauthorized {
		t.Fatalf("disabled account: status = %d, want 401", status)
	}
}

func TestMCPSwitchStopsTokensAtOnce(t *testing.T) {
	fixture := newMCPFixture(t, MCPConfig{})
	secret, _ := fixture.token("reader")
	ping := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}
	client := &testClient{t: t, handler: fixture.handler}
	client.login()
	if status, body := client.request(http.MethodPut, "/web/v1/admin/users/reader/mcp", map[string]any{"enabled": false}, true); status != http.StatusOK {
		t.Fatalf("switch off: status = %d, body = %v", status, body)
	}
	if status, _ := fixture.post(secret, ping); status != http.StatusForbidden {
		t.Fatalf("MCP switched off: status = %d, want 403", status)
	}
	if status, _ := client.request(http.MethodPut, "/web/v1/admin/users/reader/mcp", map[string]any{"enabled": true}, true); status != http.StatusOK {
		t.Fatalf("switch on: status = %d", status)
	}
	if status, _ := fixture.post(secret, ping); status != http.StatusOK {
		t.Fatalf("MCP switched on again: status = %d, want 200", status)
	}
}

func TestMCPAllowedPersonalSwitchOverridesGroups(t *testing.T) {
	user := model.User{ID: "u", DirectoryID: "person", Groups: []string{"artists", "coders"}}
	group := func(id string, mcpOff, disabled bool) model.AccessEntry {
		return model.AccessEntry{SubjectType: model.SubjectGroup, Subject: id, MCPDisabled: mcpOff, Disabled: disabled}
	}
	personal := func(mcpOff bool) model.AccessEntry {
		return model.AccessEntry{SubjectType: model.SubjectUser, Subject: "PERSON", MCPDisabled: mcpOff}
	}
	cases := []struct {
		name    string
		entries []model.AccessEntry
		want    bool
	}{
		{"personal off beats an allowing group", []model.AccessEntry{group("artists", false, false), personal(true)}, false},
		{"personal on beats a denying group", []model.AccessEntry{group("artists", true, false), personal(false)}, true},
		{"one allowing group is enough", []model.AccessEntry{group("artists", true, false), group("coders", false, false)}, true},
		{"every group off", []model.AccessEntry{group("artists", true, false), group("coders", true, false)}, false},
		{"a switched-off group entry doesn't count", []model.AccessEntry{group("artists", false, true)}, false},
		{"other groups don't count", []model.AccessEntry{group("others", false, false)}, false},
	}
	for _, test := range cases {
		if got := model.MCPAllowed(user, test.entries); got != test.want {
			t.Errorf("%s: MCPAllowed = %v, want %v", test.name, got, test.want)
		}
	}
	if model.MCPAllowed(model.User{ID: "local", MCPDisabled: true}, nil) || !model.MCPAllowed(model.User{ID: "local"}, nil) {
		t.Error("a local account doesn't follow its own switch")
	}
}

func TestMCPReadsOnlyWhatTheOwnerMayRead(t *testing.T) {
	fixture := newMCPFixture(t, MCPConfig{})
	secret, _ := fixture.token("reader")
	text, failed := fixture.call(secret, "list_repositories", nil)
	if failed || !strings.Contains(text, "team/public") || !strings.Contains(text, "team/shared") || strings.Contains(text, "team/private") {
		t.Fatalf("list_repositories = %s", text)
	}
	for _, reference := range []string{"team/private", fixture.private.ID, "team/missing"} {
		text, failed := fixture.call(secret, "get_repository", map[string]any{"repository": reference})
		if !failed || !strings.Contains(text, "Repository not found") {
			t.Fatalf("get_repository %s = %s, want the same not-found answer", reference, text)
		}
	}
	if text, failed := fixture.call(secret, "list_files", map[string]any{"repository": "team/shared"}); failed || !strings.Contains(text, "README.md") {
		t.Fatalf("list_files = %s", text)
	}

	limited, _ := fixture.token("reader", fixture.public.ID)
	if text, failed := fixture.call(limited, "get_repository", map[string]any{"repository": "team/shared"}); !failed {
		t.Fatalf("a token limited to one repository read another: %s", text)
	}
	if text, _ := fixture.call(limited, "list_repositories", nil); strings.Contains(text, "team/shared") {
		t.Fatalf("a limited token lists other repositories: %s", text)
	}
	if status, body := fixture.post(secret, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "delete_repository"}}); status != http.StatusOK || body["error"] == nil {
		t.Fatalf("an unknown tool answered %v", body)
	}
	status, body := fixture.post(secret, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if status != http.StatusOK {
		t.Fatalf("tools/list status = %d", status)
	}
	for _, tool := range body["result"].(map[string]any)["tools"].([]any) {
		annotations := tool.(map[string]any)["annotations"].(map[string]any)
		if annotations["readOnlyHint"] != true || annotations["destructiveHint"] != false {
			t.Fatalf("tool %v isn't marked read-only", tool.(map[string]any)["name"])
		}
	}
}

func TestMCPFileInfoAndReadText(t *testing.T) {
	fixture := newMCPFixture(t, MCPConfig{MaxRead: 4 << 10})
	secret, _ := fixture.token("reader")
	info := func(path string) map[string]any {
		text, failed := fixture.call(secret, "file_info", map[string]any{"repository": "team/shared", "path": path})
		if failed {
			t.Fatalf("file_info %s = %s", path, text)
		}
		var decoded map[string]any
		_ = json.Unmarshal([]byte(text), &decoded)
		return decoded
	}
	for path, want := range map[string]string{"README.md": contentText, "art/hero.fbx": contentBinary, "docs/legacy.txt": contentOtherText, "docs/wide.txt": contentText} {
		if got := info(path)["content"]; got != want {
			t.Fatalf("file_info %s content = %v, want %s", path, got, want)
		}
	}

	read := func(arguments map[string]any) (string, bool) {
		arguments["repository"] = "team/shared"
		return fixture.call(secret, "read_text", arguments)
	}
	text, failed := read(map[string]any{"path": "logs/build.log", "offset": 5, "limit": 3})
	if failed || !strings.Contains(text, "     5│ line xx") || !strings.Contains(text, "     7│") || strings.Contains(text, "     8│") || !strings.Contains(text, "offset=8") {
		t.Fatalf("read_text part = %s", text)
	}
	text, _ = read(map[string]any{"path": "logs/build.log"})
	if !strings.Contains(text, "End of file: 30 lines") {
		t.Fatalf("read_text whole = %s", text)
	}
	text, _ = read(map[string]any{"path": "logs/build.log", "pattern": "error"})
	if !strings.Contains(text, "    10│") || !strings.Contains(text, "    30│") || strings.Contains(text, "    11│") || !strings.Contains(text, "3 matching lines") {
		t.Fatalf("read_text pattern = %s", text)
	}
	text, _ = read(map[string]any{"path": "logs/build.log", "pattern": `^line x{2}$`, "regex": true, "limit": 2})
	if !strings.Contains(text, "     2│") || !strings.Contains(text, "     5│") || strings.Contains(text, "     8│") || !strings.Contains(text, "offset=6") {
		t.Fatalf("read_text regex = %s", text)
	}
	if text, failed := read(map[string]any{"path": "logs/build.log", "pattern": "(", "regex": true}); !failed {
		t.Fatalf("an invalid regular expression was accepted: %s", text)
	}
	if text, failed := read(map[string]any{"path": "art/hero.fbx"}); !failed || !strings.Contains(text, "download_link") {
		t.Fatalf("read_text binary = %s", text)
	}
	if text, failed := read(map[string]any{"path": "docs/legacy.txt"}); !failed || !strings.Contains(text, "windows-1251") {
		t.Fatalf("read_text without encoding = %s", text)
	}
	if text, failed := read(map[string]any{"path": "docs/legacy.txt", "encoding": "cp1251"}); failed || !strings.Contains(text, "Привет, мир") {
		t.Fatalf("read_text windows-1251 = %s", text)
	}
	if text, failed := read(map[string]any{"path": "docs/wide.txt"}); failed || !strings.Contains(text, "     2│ сторока два") {
		t.Fatalf("read_text UTF-16 = %s", text)
	}
	if text, failed := read(map[string]any{"path": "README.md", "encoding": "klingon"}); !failed {
		t.Fatalf("an unknown encoding was accepted: %s", text)
	}

	fixture.content.files["big.txt"] = []byte(strings.Repeat(strings.Repeat("y", 99)+"\n", 200))
	text, _ = read(map[string]any{"path": "big.txt"})
	if len(text) > 4<<10+300 || !strings.Contains(text, "More lines follow") {
		t.Fatalf("read_text over the limit returned %d bytes: %s", len(text), text[len(text)-200:])
	}
}

func TestClassifyContent(t *testing.T) {
	cases := []struct {
		data     []byte
		complete bool
		kind     string
	}{
		{[]byte("plain ASCII\n"), true, contentText},
		{[]byte("Привет"), true, contentText},
		{[]byte("Привет")[:3], false, contentText}, // cut inside a character at the end of the sample
		{[]byte("Привет")[:3], true, contentOtherText},
		{[]byte{0xCF, 0xF0, 0xE8}, true, contentOtherText}, // windows-1251
		{[]byte("PK\x03\x04\x00\x00"), true, contentBinary},
		{[]byte{0xFF, 0xFE, 'a', 0}, true, contentText},
		{[]byte{}, true, contentText},
	}
	for _, test := range cases {
		if kind, _ := classifyContent(test.data, test.complete); kind != test.kind {
			t.Errorf("classifyContent(%q, %v) = %s, want %s", test.data, test.complete, kind, test.kind)
		}
	}
}

func TestMCPDownloadLink(t *testing.T) {
	fixture := newMCPFixture(t, MCPConfig{LinkTTL: time.Hour})
	secret, token := fixture.token("reader")
	text, failed := fixture.call(secret, "download_link", map[string]any{"repository": "team/shared", "path": "art/hero.fbx"})
	if failed {
		t.Fatalf("download_link = %s", text)
	}
	var link map[string]any
	_ = json.Unmarshal([]byte(text), &link)
	address, _ := link["url"].(string)
	if !strings.HasPrefix(address, "https://lore.example/mcp/download/") {
		t.Fatalf("link = %v", link)
	}
	get := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, strings.TrimPrefix(address, "https://lore.example"), nil))
		return response
	}
	if response := get(); response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), fixture.content.files["art/hero.fbx"]) {
		t.Fatalf("download: status = %d", response.Code)
	}
	audit, _ := fixture.storage.ListAudit(t.Context(), 0, 10)
	if audit.Items[0].Action != "mcp.download" || audit.Items[0].After != "art/hero.fbx" || audit.Items[0].ActorID != "reader" {
		t.Fatalf("audit = %+v", audit.Items[0])
	}
	// Losing access to the repository stops the link.
	if err := fixture.storage.RemoveGrant(t.Context(), "owner", model.Grant{RepositoryID: fixture.readable.ID, SubjectType: model.SubjectUser, Subject: "reader", Permission: model.PermissionRead}); err != nil {
		t.Fatal(err)
	}
	if response := get(); response.Code != http.StatusNotFound {
		t.Fatalf("download without access: status = %d, want 404", response.Code)
	}
	// So does revoking the token.
	text, _ = fixture.call(secret, "download_link", map[string]any{"repository": "team/public", "path": "README.md"})
	_ = json.Unmarshal([]byte(text), &link)
	address = link["url"].(string)
	if response := get(); response.Code != http.StatusOK {
		t.Fatalf("public download: status = %d", response.Code)
	}
	if err := fixture.storage.RevokeMCPToken(t.Context(), "reader", token.ID); err != nil {
		t.Fatal(err)
	}
	if response := get(); response.Code != http.StatusNotFound {
		t.Fatalf("download after the token was revoked: status = %d, want 404", response.Code)
	}

	expiring := newMCPFixture(t, MCPConfig{LinkTTL: time.Millisecond})
	secret, _ = expiring.token("reader")
	text, _ = expiring.call(secret, "download_link", map[string]any{"repository": "team/public", "path": "README.md"})
	_ = json.Unmarshal([]byte(text), &link)
	time.Sleep(5 * time.Millisecond)
	response := httptest.NewRecorder()
	expiring.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, strings.TrimPrefix(link["url"].(string), "https://lore.example"), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("expired link: status = %d, want 404", response.Code)
	}
}

func TestMCPInternalAnswersOnlyItsNetworks(t *testing.T) {
	fixture := newMCPFixture(t, MCPConfig{Mode: MCPInternal, Networks: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16"), netip.MustParsePrefix("192.168.0.0/24")}})
	secret, _ := fixture.token("reader")
	ping := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}
	from := func(address string) func(*http.Request) {
		return func(r *http.Request) { r.RemoteAddr = address }
	}
	for address, want := range map[string]int{"10.0.3.4:5000": http.StatusOK, "192.168.0.9:5000": http.StatusOK, "192.168.1.9:5000": http.StatusNotFound, "203.0.113.7:5000": http.StatusNotFound} {
		if status, _ := fixture.post(secret, ping, from(address)); status != want {
			t.Errorf("from %s: status = %d, want %d", address, status, want)
		}
	}
	// The address comes from the proxy's X-Real-IP.
	if status, _ := fixture.post(secret, ping, from("172.18.0.5:5000"), func(r *http.Request) { r.Header.Set("X-Real-IP", "10.0.0.8") }); status != http.StatusOK {
		t.Errorf("through the proxy from an internal address: status = %d, want 200", status)
	}
}

func TestMCPLimiterCapsCallsPerToken(t *testing.T) {
	limiter := &mcpLimiter{limit: 2, running: map[string]int{}}
	if !limiter.acquire("a") || !limiter.acquire("a") || limiter.acquire("a") {
		t.Fatal("the third call of a token wasn't refused")
	}
	if !limiter.acquire("b") {
		t.Fatal("another token was refused")
	}
	limiter.release("a")
	if !limiter.acquire("a") {
		t.Fatal("a finished call didn't free its place")
	}
	unlimited := &mcpLimiter{running: map[string]int{}}
	for range 100 {
		if !unlimited.acquire("a") {
			t.Fatal("0 should mean no limit")
		}
	}
}

func TestMCPTokenAPI(t *testing.T) {
	fixture := newMCPFixture(t, MCPConfig{})
	client := &testClient{t: t, handler: fixture.handler}
	client.login()
	status, created := client.request(http.MethodPost, "/web/v1/mcp-tokens", map[string]any{"name": "laptop agent", "repositories": []string{fixture.private.ID}}, true)
	if status != http.StatusCreated {
		t.Fatalf("create: status = %d, body = %v", status, created)
	}
	secret, _ := created["secret"].(string)
	if !strings.HasPrefix(secret, mcpTokenPrefix) || len(secret) != len(mcpTokenPrefix)+43 || created["url"] != "https://lore.example/mcp" {
		t.Fatalf("created = %v", created)
	}
	status, listed := client.request(http.MethodGet, "/web/v1/mcp-tokens", nil, false)
	encoded, _ := json.Marshal(listed)
	if status != http.StatusOK || strings.Contains(string(encoded), secret) || len(listed["items"].([]any)) != 1 {
		t.Fatalf("list: status = %d, body = %s", status, encoded)
	}
	if text, failed := fixture.call(secret, "get_repository", map[string]any{"repository": "team/private"}); failed {
		t.Fatalf("an administrator's token limited to the repository: %s", text)
	}
	if status, _ := client.request(http.MethodPost, "/web/v1/mcp-tokens", map[string]any{"name": ""}, true); status != http.StatusBadRequest {
		t.Fatalf("token without a name: status = %d, want 400", status)
	}
	readerSecret, readerToken := fixture.token("reader")
	if status, _ := client.request(http.MethodDelete, "/web/v1/mcp-tokens/"+readerToken.ID, nil, true); status != http.StatusNotFound {
		t.Fatalf("revoking someone else's token from your own list: status = %d, want 404", status)
	}
	status, all := client.request(http.MethodGet, "/web/v1/admin/mcp-tokens", nil, false)
	if status != http.StatusOK || len(all["items"].([]any)) != 2 {
		t.Fatalf("administrator's list: status = %d, body = %v", status, all)
	}
	if status, _ := client.request(http.MethodDelete, "/web/v1/admin/mcp-tokens/"+readerToken.ID, nil, true); status != http.StatusNoContent {
		t.Fatalf("administrator revokes: status = %d", status)
	}
	if status, _ := fixture.post(readerSecret, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}); status != http.StatusUnauthorized {
		t.Fatalf("revoked token: status = %d, want 401", status)
	}
	created2 := created["token"].(map[string]any)
	if status, _ := client.request(http.MethodDelete, "/web/v1/mcp-tokens/"+created2["id"].(string), nil, true); status != http.StatusNoContent {
		t.Fatalf("revoke own: status = %d", status)
	}
}
