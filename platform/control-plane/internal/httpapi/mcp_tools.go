package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/arutar/lore/platform/control-plane/internal/loreclient"
	"github.com/arutar/lore/platform/control-plane/internal/model"
	"github.com/arutar/lore/platform/control-plane/internal/policy"
	"github.com/arutar/lore/platform/control-plane/internal/store"
)

type mcpTool struct {
	title       string
	description string
	// properties describes the arguments as JSON Schema; required names the ones that must be given.
	properties map[string]any
	required   []string
	run        func(ctx context.Context, s *Server, r *http.Request, caller mcpCaller, arguments json.RawMessage) map[string]any
}

const mcpListPage = 200

var errMCPRepository = errors.New("repository not found, or this token can't read it")

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerProperty(description string, minimum, maximum int) map[string]any {
	return map[string]any{"type": "integer", "description": description, "minimum": minimum, "maximum": maximum}
}

var (
	repositoryProperty = stringProperty("Repository name, such as game/hollow-keep, or its ID")
	revisionProperty   = stringProperty("Revision ID (signature) to read; the latest revision when omitted")
	filePathProperty   = stringProperty("File path in the repository, such as source/main.cpp")
)

var mcpTools = map[string]mcpTool{
	"list_repositories": {
		title:       "List repositories",
		description: "Lists the repositories this token may read, a page at a time.",
		properties: map[string]any{
			"query":  stringProperty("Keep repositories whose name or description contains this text"),
			"cursor": stringProperty("nextCursor from the previous page"),
		},
		run: toolListRepositories,
	},
	"get_repository": {
		title:       "Repository details",
		description: "Returns a repository's name, description, owner, visibility and dates.",
		properties:  map[string]any{"repository": repositoryProperty},
		required:    []string{"repository"},
		run:         toolGetRepository,
	},
	"list_files": {
		title:       "List files",
		description: "Lists the files and folders in one folder of a repository, a page at a time.",
		properties: map[string]any{
			"repository": repositoryProperty,
			"path":       stringProperty("Folder path; the repository root when omitted"),
			"revision":   revisionProperty,
			"cursor":     stringProperty("nextCursor from the previous page"),
		},
		required: []string{"repository"},
		run:      toolListFiles,
	},
	"file_info": {
		title: "File details",
		description: "Returns a file's metadata only: size, whether it's UTF-8 text, other text or binary, the revision, " +
			"who changed it last and whether it's locked. Use read_text for a text file's content and download_link for the file itself.",
		properties: map[string]any{"repository": repositoryProperty, "path": filePathProperty, "revision": revisionProperty},
		required:   []string{"repository", "path"},
		run:        toolFileInfo,
	},
	"read_text": {
		title: "Read a text file",
		description: "Returns lines of a text file with their numbers. Long files come in parts: continue with offset. " +
			"pattern returns only the lines that contain it (case-insensitive), or match it as a regular expression with regex. " +
			"A file that isn't UTF-8 needs its encoding, such as windows-1251, ibm866 or koi8-r. Binary files can't be read: use download_link.",
		properties: map[string]any{
			"repository": repositoryProperty,
			"path":       filePathProperty,
			"revision":   revisionProperty,
			"offset":     integerProperty("First line to return, from 1", 1, 1<<31-1),
			"limit":      integerProperty("Most lines to return (default 2000)", 1, maxReadLines),
			"pattern":    stringProperty("Return only lines containing this text (case-insensitive)"),
			"regex":      map[string]any{"type": "boolean", "description": "Treat pattern as a regular expression (RE2 syntax)"},
			"encoding":   stringProperty("The file's encoding when it isn't UTF-8, such as windows-1251, ibm866, koi8-r, iso-8859-5"),
		},
		required: []string{"repository", "path"},
		run:      toolReadText,
	},
	"download_link": {
		title: "Download link",
		description: "Returns a short-lived link that downloads the file, of any size and type, with no token needed " +
			"(for example with curl -o, or opened in a browser). The link is for one file at one revision.",
		properties: map[string]any{"repository": repositoryProperty, "path": filePathProperty, "revision": revisionProperty},
		required:   []string{"repository", "path"},
		run:        toolDownloadLink,
	},
	"history": {
		title:       "Revision history",
		description: "Lists a repository's revisions, newest first, a page at a time.",
		properties:  map[string]any{"repository": repositoryProperty, "cursor": stringProperty("nextCursor from the previous page")},
		required:    []string{"repository"},
		run:         toolHistory,
	},
	"revision_changes": {
		title:       "Changes in a revision",
		description: "Lists the files a revision added, modified, deleted, moved or copied, with counts by kind on the first page.",
		properties: map[string]any{
			"repository": repositoryProperty,
			"revision":   stringProperty("Revision ID (signature), from history"),
			"kind":       map[string]any{"type": "string", "enum": []string{"added", "modified", "deleted", "moved", "copied"}, "description": "Keep one kind of change"},
			"query":      stringProperty("Keep paths containing this text"),
			"cursor":     stringProperty("nextCursor from the previous page"),
		},
		required: []string{"repository", "revision"},
		run:      toolRevisionChanges,
	},
	"list_locks": {
		title:       "File locks",
		description: "Lists the files locked in a repository, with who holds each lock and since when.",
		properties:  map[string]any{"repository": repositoryProperty},
		required:    []string{"repository"},
		run:         toolListLocks,
	},
}

var mcpToolOrder = []string{"list_repositories", "get_repository", "list_files", "file_info", "read_text", "download_link", "history", "revision_changes", "list_locks"}

func mcpToolList() []map[string]any {
	tools := make([]map[string]any, 0, len(mcpToolOrder))
	for _, name := range mcpToolOrder {
		tool := mcpTools[name]
		schema := map[string]any{"type": "object", "properties": tool.properties, "additionalProperties": false}
		if len(tool.required) > 0 {
			schema["required"] = tool.required
		}
		tools = append(tools, map[string]any{
			"name": name, "title": tool.title, "description": tool.description, "inputSchema": schema,
			"annotations": map[string]any{"title": tool.title, "readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		})
	}
	return tools
}

func toolText(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false}
}

func toolError(message string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": message}}, "isError": true}
}

func toolJSON(value any) map[string]any {
	text, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return toolError("Couldn't encode the result")
	}
	return toolText(string(text))
}

// toolArguments decodes a call's arguments, refusing unknown names so a typo isn't silently ignored.
func toolArguments(arguments json.RawMessage, destination any) map[string]any {
	decoder := json.NewDecoder(strings.NewReader(string(arguments)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return toolError("Invalid arguments: " + err.Error())
	}
	return nil
}

// toolContentError explains a failed read of repository content to the agent.
func toolContentError(ctx context.Context, err error) map[string]any {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return toolError("The call took longer than " + mcpCallTimeout.String() + " and was stopped. Ask for less, for example a smaller part of the file.")
	}
	log.Printf("MCP content request failed: %v", err)
	_, _, message := contentProblem(err)
	return toolError(message)
}

// mcpRepository finds a repository the caller may read by name or ID. A private repository, one
// the token doesn't cover and one that doesn't exist all give the same answer.
func (s *Server) mcpRepository(ctx context.Context, caller mcpCaller, reference string) (model.Repository, error) {
	reference = strings.Trim(strings.TrimSpace(reference), "/")
	if reference == "" {
		return model.Repository{}, errMCPRepository
	}
	var repository model.Repository
	var err error
	if repositoryID.MatchString(reference) {
		repository, err = s.store.GetRepository(ctx, reference)
	} else {
		repository, err = s.store.GetRepositoryByName(ctx, reference)
	}
	if errors.Is(err, store.ErrNotFound) {
		return model.Repository{}, errMCPRepository
	}
	if err != nil {
		return model.Repository{}, err
	}
	grants, err := s.store.ListGrants(ctx, repository.ID)
	if err != nil {
		return model.Repository{}, err
	}
	if !policy.CanRead(&caller.user, grants) || !caller.token.Covers(repository.ID) {
		return model.Repository{}, errMCPRepository
	}
	return repository, nil
}

func repositoryProblem(err error) map[string]any {
	if errors.Is(err, errMCPRepository) {
		return toolError("Repository not found, or this token can't read it. list_repositories shows the ones it can.")
	}
	log.Printf("MCP repository lookup failed: %v", err)
	return toolError("Internal service error")
}

func (s *Server) mcpContent() (RepositoryContent, map[string]any) {
	if s.config.Content == nil {
		return nil, toolError("Repository content isn't available on this server")
	}
	return s.config.Content, nil
}

func cleanPath(value string) string {
	return strings.Trim(strings.TrimSpace(value), "/")
}

func offsetCursor(cursor string) (int, bool) {
	if cursor == "" {
		return 0, true
	}
	offset, err := strconv.Atoi(cursor)
	return offset, err == nil && offset >= 0
}

type repositoryView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Public      bool      `json:"public"`
	Owner       string    `json:"owner,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func repositoryViewOf(repository model.Repository, names func(string) string) repositoryView {
	return repositoryView{ID: repository.ID, Name: repository.Name, Description: repository.Description, Public: repository.Public,
		Owner: names(repository.OwnerID), CreatedAt: repository.CreatedAt, UpdatedAt: repository.UpdatedAt}
}

func toolListRepositories(ctx context.Context, s *Server, _ *http.Request, caller mcpCaller, arguments json.RawMessage) map[string]any {
	var input struct{ Query, Cursor string }
	if problem := toolArguments(arguments, &input); problem != nil {
		return problem
	}
	query := strings.TrimSpace(input.Query)
	if runes := []rune(query); len(runes) > 128 {
		query = string(runes[:128])
	}
	page, err := s.store.Catalog(ctx, &caller.user, query, input.Cursor, 50)
	if err != nil {
		log.Printf("MCP catalog failed: %v", err)
		return toolError("Internal service error")
	}
	names := s.accountNames(ctx)
	items := []repositoryView{}
	for _, repository := range page.Items {
		if caller.token.Covers(repository.ID) {
			items = append(items, repositoryViewOf(repository, names))
		}
	}
	return toolJSON(map[string]any{"items": items, "nextCursor": page.NextCursor})
}

func toolGetRepository(ctx context.Context, s *Server, _ *http.Request, caller mcpCaller, arguments json.RawMessage) map[string]any {
	var input struct{ Repository string }
	if problem := toolArguments(arguments, &input); problem != nil {
		return problem
	}
	repository, err := s.mcpRepository(ctx, caller, input.Repository)
	if err != nil {
		return repositoryProblem(err)
	}
	return toolJSON(repositoryViewOf(repository, s.accountNames(ctx)))
}

func toolListFiles(ctx context.Context, s *Server, _ *http.Request, caller mcpCaller, arguments json.RawMessage) map[string]any {
	var input struct{ Repository, Path, Revision, Cursor string }
	if problem := toolArguments(arguments, &input); problem != nil {
		return problem
	}
	repository, err := s.mcpRepository(ctx, caller, input.Repository)
	if err != nil {
		return repositoryProblem(err)
	}
	content, problem := s.mcpContent()
	if problem != nil {
		return problem
	}
	offset, ok := offsetCursor(input.Cursor)
	if !ok {
		return toolError("Invalid cursor")
	}
	tree, err := content.Tree(ctx, repository.ID, strings.TrimSpace(input.Revision), cleanPath(input.Path))
	if err != nil {
		return toolContentError(ctx, err)
	}
	type entry struct {
		Path string `json:"path"`
		Type string `json:"type"`
		Size uint64 `json:"size,omitempty"`
	}
	items := []entry{}
	next := ""
	for index := offset; index < len(tree.Items); index++ {
		if len(items) == mcpListPage {
			next = strconv.Itoa(index)
			break
		}
		item := tree.Items[index]
		items = append(items, entry{Path: item.Path, Type: item.Type, Size: item.Size})
	}
	return toolJSON(map[string]any{"repository": repository.Name, "revision": tree.Revision, "path": cleanPath(input.Path), "items": items, "nextCursor": next})
}

// mcpFile finds a file at a revision (the latest when none is given) and the revision itself.
func mcpFile(ctx context.Context, content RepositoryContent, repositoryID, revision, filePath string) (loreclient.TreeNode, string, error) {
	parent := path.Dir(filePath)
	if parent == "." {
		parent = ""
	}
	tree, err := content.Tree(ctx, repositoryID, strings.TrimSpace(revision), parent)
	if err != nil {
		return loreclient.TreeNode{}, "", err
	}
	for _, item := range tree.Items {
		if item.Path == filePath {
			return item, tree.Revision, nil
		}
	}
	return loreclient.TreeNode{}, tree.Revision, errFileNotFound
}

var errFileNotFound = errors.New("the file wasn't found in revision")

func toolFileInfo(ctx context.Context, s *Server, _ *http.Request, caller mcpCaller, arguments json.RawMessage) map[string]any {
	var input struct{ Repository, Path, Revision string }
	if problem := toolArguments(arguments, &input); problem != nil {
		return problem
	}
	repository, err := s.mcpRepository(ctx, caller, input.Repository)
	if err != nil {
		return repositoryProblem(err)
	}
	content, problem := s.mcpContent()
	if problem != nil {
		return problem
	}
	filePath := cleanPath(input.Path)
	if filePath == "" {
		return toolError("path is required")
	}
	node, revision, err := mcpFile(ctx, content, repository.ID, input.Revision, filePath)
	if err != nil {
		return toolContentError(ctx, err)
	}
	info := map[string]any{"repository": repository.Name, "path": node.Path, "type": node.Type, "revision": revision}
	if node.Type == "file" {
		info["size"] = node.Size
		kind, encoding, err := sniffFile(ctx, content, repository.ID, revision, node)
		if err != nil {
			return toolContentError(ctx, err)
		}
		info["content"] = kind
		if encoding != "" {
			info["encoding"] = encoding
		}
		switch kind {
		case contentText:
			info["hint"] = "Read it with read_text."
		case contentOtherText:
			info["hint"] = "Text, but not UTF-8: read it with read_text and its encoding (such as windows-1251), or download it with download_link."
		default:
			info["hint"] = "Binary: download it with download_link."
		}
	}
	// Who changed it last reads history, so it gets a share of the time and is left out if slow.
	lastCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	changes, err := content.LastChanges(lastCtx, repository.ID, revision, []string{node.Path})
	cancel()
	if err == nil {
		if change, ok := changes[node.Path]; ok {
			change.Author = s.accountNames(ctx)(change.Author)
			info["lastChange"] = change
		}
	}
	if locks, err := content.Locks(ctx, repository.ID); err == nil {
		for _, lock := range locks {
			if lock.Path == node.Path {
				info["lock"] = map[string]any{"owner": s.accountNames(ctx)(lock.Owner), "lockedAt": lock.LockedAt}
			}
		}
	}
	return toolJSON(info)
}

func toolDownloadLink(ctx context.Context, s *Server, r *http.Request, caller mcpCaller, arguments json.RawMessage) map[string]any {
	var input struct{ Repository, Path, Revision string }
	if problem := toolArguments(arguments, &input); problem != nil {
		return problem
	}
	repository, err := s.mcpRepository(ctx, caller, input.Repository)
	if err != nil {
		return repositoryProblem(err)
	}
	content, problem := s.mcpContent()
	if problem != nil {
		return problem
	}
	filePath := cleanPath(input.Path)
	node, revision, err := mcpFile(ctx, content, repository.ID, input.Revision, filePath)
	if err != nil {
		return toolContentError(ctx, err)
	}
	if node.Type != "file" {
		return toolError(filePath + " is a " + node.Type + ", not a file")
	}
	key, err := newLinkKey()
	if err != nil {
		return toolError("Couldn't create a link")
	}
	expires := time.Now().Add(s.config.MCP.LinkTTL).UTC()
	link := model.MCPLink{TokenID: caller.token.ID, UserID: caller.user.ID, RepositoryID: repository.ID, Path: node.Path, Revision: revision, ExpiresAt: expires}
	if err := s.store.CreateMCPLink(ctx, secretHash(key), link); err != nil {
		log.Printf("MCP link wasn't stored: %v", err)
		return toolError("Internal service error")
	}
	s.recordAudit(r, model.AuditEvent{ActorID: caller.user.ID, Action: "mcp.download", TargetType: "repository", TargetID: repository.ID, After: node.Path})
	return toolJSON(map[string]any{
		"url": strings.TrimRight(s.config.ExternalURL, "/") + "/mcp/download/" + key, "expiresAt": expires,
		"name": path.Base(node.Path), "size": node.Size, "revision": revision,
		"hint": "Download it, for example: curl -fLo '" + path.Base(node.Path) + "' '<url>'. No token is needed; the link stops working at expiresAt.",
	})
}

func toolHistory(ctx context.Context, s *Server, _ *http.Request, caller mcpCaller, arguments json.RawMessage) map[string]any {
	var input struct{ Repository, Cursor string }
	if problem := toolArguments(arguments, &input); problem != nil {
		return problem
	}
	repository, err := s.mcpRepository(ctx, caller, input.Repository)
	if err != nil {
		return repositoryProblem(err)
	}
	content, problem := s.mcpContent()
	if problem != nil {
		return problem
	}
	page, err := content.History(ctx, repository.ID, strings.TrimSpace(input.Cursor))
	if err != nil {
		return toolContentError(ctx, err)
	}
	names := s.accountNames(ctx)
	for index := range page.Items {
		page.Items[index].Author = names(page.Items[index].Author)
		page.Items[index].Committer = names(page.Items[index].Committer)
	}
	return toolJSON(map[string]any{"repository": repository.Name, "items": page.Items, "nextCursor": page.NextCursor})
}

func toolRevisionChanges(ctx context.Context, s *Server, _ *http.Request, caller mcpCaller, arguments json.RawMessage) map[string]any {
	var input struct{ Repository, Revision, Kind, Query, Cursor string }
	if problem := toolArguments(arguments, &input); problem != nil {
		return problem
	}
	switch input.Kind {
	case "", loreclient.ChangeAdded, loreclient.ChangeModified, loreclient.ChangeDeleted, loreclient.ChangeMoved, loreclient.ChangeCopied:
	default:
		return toolError("kind must be added, modified, deleted, moved or copied")
	}
	repository, err := s.mcpRepository(ctx, caller, input.Repository)
	if err != nil {
		return repositoryProblem(err)
	}
	content, problem := s.mcpContent()
	if problem != nil {
		return problem
	}
	revision := strings.TrimSpace(input.Revision)
	page, err := content.Changes(ctx, repository.ID, revision, loreclient.ChangeFilter{Kind: input.Kind, Query: strings.TrimSpace(input.Query)}, input.Cursor, mcpListPage)
	if err != nil {
		return toolContentError(ctx, err)
	}
	result := map[string]any{"repository": repository.Name, "revision": revision, "items": page.Items, "nextCursor": page.NextCursor}
	if input.Cursor == "" {
		if summary, err := content.ChangeSummary(ctx, repository.ID, revision); err == nil {
			result["summary"] = summary
		}
	}
	return toolJSON(result)
}

func toolListLocks(ctx context.Context, s *Server, _ *http.Request, caller mcpCaller, arguments json.RawMessage) map[string]any {
	var input struct{ Repository string }
	if problem := toolArguments(arguments, &input); problem != nil {
		return problem
	}
	repository, err := s.mcpRepository(ctx, caller, input.Repository)
	if err != nil {
		return repositoryProblem(err)
	}
	content, problem := s.mcpContent()
	if problem != nil {
		return problem
	}
	locks, err := content.Locks(ctx, repository.ID)
	if err != nil {
		return toolContentError(ctx, err)
	}
	names := s.accountNames(ctx)
	type lockView struct {
		Path     string    `json:"path"`
		Owner    string    `json:"owner"`
		LockedAt time.Time `json:"lockedAt"`
	}
	items := []lockView{}
	for _, lock := range locks {
		items = append(items, lockView{Path: lock.Path, Owner: names(lock.Owner), LockedAt: lock.LockedAt})
	}
	return toolJSON(map[string]any{"repository": repository.Name, "items": items})
}
