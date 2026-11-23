package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/policy"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

// The MCP endpoint lets AI agents read repositories: a stateless Streamable HTTP server
// (JSON-RPC over POST, answered with plain JSON) whose tools only read. Every request carries a
// personal token, also for public repositories, and reads only what the token's owner may read
// at that moment. See platform/docs/decisions/0002-mcp.md.

type MCPMode string

const (
	MCPOff      MCPMode = "off"
	MCPInternal MCPMode = "internal"
	MCPOn       MCPMode = "on"
)

type MCPConfig struct {
	Mode MCPMode
	// Networks are the client networks MCPInternal answers.
	Networks []netip.Prefix
	// Concurrency is how many tool calls one token may run at once; 0 means no limit.
	Concurrency int
	// MaxRead is the most bytes of text read_text returns in one call.
	MaxRead int
	// LinkTTL is how long a download link works.
	LinkTTL time.Duration
}

// mcpProtocolVersions are the MCP revisions this server speaks, newest first.
var mcpProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

const (
	mcpCallTimeout  = 25 * time.Second
	mcpTouchEvery   = time.Minute
	maxMCPBody      = 1 << 20
	mcpServerName   = "lore"
	mcpInstructions = "Lore is a version control system. These tools read the repositories your token may read: " +
		"list_repositories, then list_files and file_info. file_info returns metadata only; read_text returns a text file " +
		"with line numbers, in parts (offset, limit) and can search one file (pattern); download_link gives a short-lived link " +
		"to download any file, binary ones included. Nothing can be changed through these tools."
)

type mcpCaller struct {
	user  model.User
	token model.MCPToken
}

// mcpLimiter counts the tool calls each token is running.
type mcpLimiter struct {
	mu      sync.Mutex
	limit   int
	running map[string]int
}

func (l *mcpLimiter) acquire(id string) bool {
	if l.limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running[id] >= l.limit {
		return false
	}
	l.running[id]++
	return true
}

func (l *mcpLimiter) release(id string) {
	if l.limit <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running[id]--; l.running[id] <= 0 {
		delete(l.running, id)
	}
}

func (l *mcpLimiter) forget(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.running, id)
}

func (s *Server) mcpLimiter() *mcpLimiter { return s.limiter }

// mcpNetworkAllowed reports whether the client may reach MCP from where it is. The address is the
// one the proxy reports (Caddy sets X-Real-IP to the connection's address).
func (s *Server) mcpNetworkAllowed(r *http.Request) bool {
	if s.config.MCP.Mode != MCPInternal {
		return true
	}
	address, err := netip.ParseAddr(r.RemoteAddr)
	if err != nil {
		addressPort, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			return false
		}
		address = addressPort.Addr()
	}
	address = address.Unmap()
	return slices.ContainsFunc(s.config.MCP.Networks, func(network netip.Prefix) bool { return network.Contains(address) })
}

// mcpOriginAllowed refuses requests a web page in a browser sends: MCP clients send no Origin,
// and the WebUI's own origin is the only one accepted.
func (s *Server) mcpOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	external, err := url.Parse(s.config.ExternalURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(origin, external.Scheme+"://"+external.Host)
}

// mcpAccount checks that a token's owner may use MCP now: the account exists and is active (a
// directory account's groups and state are refreshed first) and MCP isn't switched off for it.
func (s *Server) mcpAccount(r *http.Request, token model.MCPToken) (model.User, int, string) {
	user, err := s.store.GetUser(r.Context(), token.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return model.User{}, http.StatusUnauthorized, "The token is invalid"
	}
	if err != nil {
		return model.User{}, http.StatusInternalServerError, "Internal service error"
	}
	if s.auth != nil && user.Active() {
		if user, err = s.auth.refresh(r.Context(), user); err != nil {
			return model.User{}, http.StatusServiceUnavailable, "Active Directory can't be reached to check the account; try again later"
		}
	}
	if !user.Active() {
		return model.User{}, http.StatusUnauthorized, "The token's account is disabled or has no access to Lore"
	}
	allowed, err := s.mcpAllowed(r, user)
	if err != nil {
		return model.User{}, http.StatusInternalServerError, "Internal service error"
	}
	if !allowed {
		return model.User{}, http.StatusForbidden, "An administrator has turned MCP off for this account"
	}
	return user, 0, ""
}

func (s *Server) mcpAuthenticate(r *http.Request) (mcpCaller, int, string) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	secret = strings.TrimSpace(secret)
	if !ok || !strings.HasPrefix(secret, mcpTokenPrefix) {
		return mcpCaller{}, http.StatusUnauthorized, "An MCP token is required: Authorization: Bearer lore_mcp_…"
	}
	token, err := s.store.GetMCPTokenByHash(r.Context(), secretHash(secret))
	if errors.Is(err, store.ErrNotFound) {
		return mcpCaller{}, http.StatusUnauthorized, "The token is invalid"
	}
	if err != nil {
		return mcpCaller{}, http.StatusInternalServerError, "Internal service error"
	}
	now := time.Now()
	if !token.Usable(now) {
		return mcpCaller{}, http.StatusUnauthorized, "The token was revoked or has expired"
	}
	user, status, message := s.mcpAccount(r, token)
	if status != 0 {
		return mcpCaller{}, status, message
	}
	if token.LastUsedAt == nil || now.Sub(*token.LastUsedAt) >= mcpTouchEvery {
		if err := s.store.TouchMCPToken(r.Context(), token.ID, now); err != nil {
			log.Printf("MCP token %s: last use wasn't recorded: %v", token.ID, err)
		}
	}
	return mcpCaller{user: user, token: token}, 0, ""
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
)

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, problem *rpcError) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	response := map[string]any{"jsonrpc": "2.0", "id": id}
	if problem != nil {
		response["error"] = problem
	} else {
		response["result"] = result
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	if !s.mcpNetworkAllowed(r) {
		// The address as the proxy reported it, so an administrator can see why a client is
		// refused, for example when Docker Desktop replaces client addresses with its own.
		log.Printf("MCP request from %s refused: not in LORE_MCP_NETWORKS", r.RemoteAddr)
		writeError(w, http.StatusNotFound, "not_found", "Resource not found")
		return
	}
	if !s.mcpOriginAllowed(r) {
		writeError(w, http.StatusForbidden, "origin_not_allowed", "Requests from web pages aren't accepted")
		return
	}
	caller, status, message := s.mcpAuthenticate(r)
	if status != 0 {
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="Lore MCP"`)
		}
		writeError(w, status, "mcp_unauthorized", message)
		return
	}
	if version := r.Header.Get("MCP-Protocol-Version"); version != "" && !slices.Contains(mcpProtocolVersions, version) {
		writeError(w, http.StatusBadRequest, "unsupported_protocol_version", "Supported MCP protocol versions: "+strings.Join(mcpProtocolVersions, ", "))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPBody))
	if err != nil {
		writeRPC(w, nil, nil, &rpcError{Code: rpcParseError, Message: "The request body is too large or unreadable"})
		return
	}
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '[' {
		writeRPC(w, nil, nil, &rpcError{Code: rpcInvalidRequest, Message: "Batches aren't supported; send one message per request"})
		return
	}
	var request rpcRequest
	if err := json.Unmarshal(body, &request); err != nil || request.JSONRPC != "2.0" || request.Method == "" {
		writeRPC(w, nil, nil, &rpcError{Code: rpcInvalidRequest, Message: "Expected one JSON-RPC 2.0 message"})
		return
	}
	if len(request.ID) == 0 {
		// A notification, such as notifications/initialized: nothing to answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(request.Params, &params)
		version := mcpProtocolVersions[0]
		if slices.Contains(mcpProtocolVersions, params.ProtocolVersion) {
			version = params.ProtocolVersion
		}
		writeRPC(w, request.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": mcpServerName, "title": "Lore", "version": "1"},
			"instructions":    mcpInstructions,
		}, nil)
	case "ping":
		writeRPC(w, request.ID, map[string]any{}, nil)
	case "tools/list":
		writeRPC(w, request.ID, map[string]any{"tools": mcpToolList()}, nil)
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.Name == "" {
			writeRPC(w, request.ID, nil, &rpcError{Code: rpcInvalidParams, Message: "tools/call needs a tool name"})
			return
		}
		tool, ok := mcpTools[params.Name]
		if !ok {
			writeRPC(w, request.ID, nil, &rpcError{Code: rpcInvalidParams, Message: "Unknown tool: " + params.Name})
			return
		}
		if !s.limiter.acquire(caller.token.ID) {
			writeRPC(w, request.ID, toolError("Busy: this token already runs "+strconv.Itoa(s.config.MCP.Concurrency)+
				" calls at once. Wait for one to finish and try again."), nil)
			return
		}
		defer s.limiter.release(caller.token.ID)
		ctx, cancel := context.WithTimeout(r.Context(), mcpCallTimeout)
		defer cancel()
		arguments := params.Arguments
		if len(arguments) == 0 || string(arguments) == "null" {
			arguments = json.RawMessage("{}")
		}
		writeRPC(w, request.ID, tool.run(ctx, s, r, caller, arguments), nil)
	default:
		writeRPC(w, request.ID, nil, &rpcError{Code: rpcMethodNotFound, Message: "Method not found: " + request.Method})
	}
}

// mcpNoStream answers GET and DELETE on /mcp: this server sends no server-initiated messages and
// keeps no sessions.
func (s *Server) mcpNoStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", http.MethodPost)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST")
}

func newLinkKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(key), nil
}

// mcpDownload serves a download link issued by the download_link tool. It needs no token, so a
// person can open it in a browser, but it works only while the token, the account, its MCP switch
// and its access to the repository still allow the download, and only from where MCP is reachable.
func (s *Server) mcpDownload(w http.ResponseWriter, r *http.Request) {
	if !s.mcpNetworkAllowed(r) {
		writeError(w, http.StatusNotFound, "not_found", "Resource not found")
		return
	}
	gone := func() {
		writeError(w, http.StatusNotFound, "link_expired", "The download link has expired or isn't valid; ask for a new one")
	}
	link, err := s.store.GetMCPLink(r.Context(), secretHash(chi.URLParam(r, "key")), time.Now())
	if err != nil {
		gone()
		return
	}
	token, err := s.store.GetMCPToken(r.Context(), link.TokenID)
	if err != nil || !token.Usable(time.Now()) || token.UserID != link.UserID || !token.Covers(link.RepositoryID) {
		gone()
		return
	}
	user, status, _ := s.mcpAccount(r, token)
	if status != 0 {
		gone()
		return
	}
	grants, err := s.store.ListGrants(r.Context(), link.RepositoryID)
	if err != nil || !policy.CanRead(&user, grants) {
		gone()
		return
	}
	content := s.configuredContent(w)
	if content == nil {
		return
	}
	result, err := content.Download(r.Context(), link.RepositoryID, link.Revision, link.Path)
	if err != nil {
		writeRepositoryContentError(w, err)
		return
	}
	defer result.Body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": result.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if result.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(result.Size, 10))
	}
	if _, err := io.Copy(w, result.Body); err != nil {
		log.Printf("MCP download interrupted: %v", err)
	}
}
