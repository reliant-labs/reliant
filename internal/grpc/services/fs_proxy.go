// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/filepreview"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// FileSystemProxyService implements FileSystemServiceHandler by forwarding
// requests to the user's daemon via DaemonCommand (request/response).
type FileSystemProxyService struct {
	reliantv1connect.UnimplementedFileSystemServiceHandler
	router   toolexec.DaemonRouter
	database db.Repository
}

// NewFileSystemProxyService creates a new FileSystemProxyService.
func NewFileSystemProxyService(router toolexec.DaemonRouter, database db.Repository) *FileSystemProxyService {
	return &FileSystemProxyService{router: router, database: database}
}

func (s *FileSystemProxyService) getUserID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("user ID not found in context")
	}
	return userID, nil
}

// requireProjectBase resolves the workspace root a request is scoped to, and
// refuses the request when there isn't one.
//
// Failing closed here is the point. This service is mounted only in hosted,
// multi-tenant deployments (see internal/grpc/server.go), where the daemon on
// the far end runs as the user and every path this service emits is executed
// there with no further scoping. Without a base there is nothing to confine
// against, so the previous behaviour — forward the caller's raw path — handed
// the daemon whatever the client asked for, including "/" and "..". A request
// that names no workspace is refused instead of being run against the
// daemon's filesystem root or its working directory.
//
// Every client reaches these RPCs with a project id (web/src/api/fileSystem.ts
// returns early when there is no current project), so nothing legitimate
// depended on the passthrough.
func (s *FileSystemProxyService) requireProjectBase(ctx context.Context, projectID string, worktreeID *string, chatID *string) (string, error) {
	if strings.TrimSpace(projectID) == "" {
		return "", connect.NewError(connect.CodeInvalidArgument,
			errors.New("project_id is required to scope a filesystem request"))
	}
	basePath, err := filepreview.ResolveBasePath(ctx, s.database, projectID, worktreeID, chatID)
	if err != nil {
		return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))
	}
	if strings.TrimSpace(basePath) == "" {
		return "", connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("project %q has no workspace path to scope this request to", projectID))
	}
	return basePath, nil
}

// resolvePath turns a client path into the absolute, confined path the daemon
// will act on.
//
// The "" | "/" == workspace root rule and the confinement check are NOT
// reimplemented here: both come from filepreview.ValidatePathScoped via
// validateWorkspacePath, the same function the direct FileSystemService uses.
// ScopeBaseOnly is deliberate — unlike the desktop path, this service serves a
// user whose files live on a remote daemon, so an absolute path outside the
// workspace is refused rather than honoured.
//
// The returned path is always absolute, so "" and "/" never cross the wire to
// the daemon.
//
// Errors are already typed connect errors — PermissionDenied for an escape,
// InvalidArgument for a malformed request, NotFound for an unknown project —
// so callers return them unchanged. Re-wrapping them as NotFound (which every
// call site used to do) reported a refused traversal as a missing file.
func (s *FileSystemProxyService) resolvePath(ctx context.Context, projectID string, worktreeID *string, chatID *string, requestedPath string) (string, error) {
	basePath, err := s.requireProjectBase(ctx, projectID, worktreeID, chatID)
	if err != nil {
		return "", err
	}
	return validateWorkspacePath(basePath, requestedPath, filepreview.ScopeBaseOnly)
}

// projectRelativePrefix reports where resolvedPath sits under basePath, in the
// project-relative form the response contract uses ("" for the root itself).
//
// The request path cannot be used directly for this: it may be absolute (an
// absolute path inside the workspace is a legal request), and rebasing daemon
// node paths onto an absolute prefix would break the UI's lazy expansion,
// which builds a child path as parent + "/" + name.
func projectRelativePrefix(basePath, resolvedPath string) string {
	absBase, err := filepath.Abs(basePath)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(absBase, resolvedPath)
	if err != nil || rel == "." {
		return ""
	}
	return strings.Trim(filepath.ToSlash(rel), "/")
}

// sendCommand is a helper that marshals the request, sends the daemon command,
// and unmarshals the response.
func (s *FileSystemProxyService) sendCommand(ctx context.Context, userID, commandType string, req any, resp any, timeoutMs int32) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))
	}

	respBytes, err := s.router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	if err != nil {
		// A daemon that exists but hasn't connected yet (still
		// provisioning) is retryable, unlike every other daemon-command
		// failure this helper maps to CodeInternal — CodeUnavailable is
		// what the frontend's daemon-wait machinery expects, and the
		// message still carries the "no daemon connected" marker
		// isDaemonConnectingError keys on either way.
		if toolexec.IsDaemonPending(err) {
			return connect.NewError(connect.CodeUnavailable, err)
		}
		return connect.NewError(connect.CodeInternal, err)
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))
	}
	return nil
}

// GetFileTree returns the file tree structure for a project.
func (s *FileSystemProxyService) GetFileTree(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFileTreeRequest],
) (*connect.Response[reliantv1.GetFileTreeResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	basePath, err := s.requireProjectBase(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := validateWorkspacePath(basePath, req.Msg.Path, filepreview.ScopeBaseOnly)
	if err != nil {
		return nil, err
	}

	cmdReq := map[string]any{
		"path":        resolvedPath,
		"show_hidden": req.Msg.ShowHidden,
		"depth":       req.Msg.Depth,
	}

	var cmdResp struct {
		Nodes     []fsProxyFileNode `json:"nodes"`
		Truncated bool              `json:"truncated"`
		NodeCount int               `json:"node_count"`
	}
	if err := s.sendCommand(ctx, userID, "fs.get_tree", cmdReq, &cmdResp, 30000); err != nil {
		return nil, err
	}

	files := make([]*reliantv1.FileNode, len(cmdResp.Nodes))
	for i, n := range cmdResp.Nodes {
		files[i] = convertFsProxyNode(&n)
	}

	// The daemon walks the resolved absolute directory and returns node paths
	// relative to THAT directory (e.g. "inner.txt" for a request scoped to
	// "pkg"). Rebase them onto the project-relative request path so every node
	// carries a full project-relative path — matching the local
	// FileSystemService's contract and what the UI needs to lazily expand a
	// subdirectory (child path = parent path + "/" + name). Root requests
	// ("" / "/") need no prefix.
	if prefix := projectRelativePrefix(basePath, resolvedPath); prefix != "" {
		for _, f := range files {
			prefixFileNodePaths(f, prefix)
		}
	}

	return connect.NewResponse(&reliantv1.GetFileTreeResponse{
		Files:     files,
		Truncated: cmdResp.Truncated,
		NodeCount: int32(cmdResp.NodeCount),
	}), nil
}

// prefixFileNodePaths rebases a node subtree's paths onto prefix, so daemon
// results scoped to a subdirectory carry full project-relative paths.
func prefixFileNodePaths(node *reliantv1.FileNode, prefix string) {
	node.Path = prefix + "/" + node.Path
	for _, c := range node.Children {
		prefixFileNodePaths(c, prefix)
	}
}

// fsProxyFileNode mirrors the daemon's fsFileNode JSON shape.
type fsProxyFileNode struct {
	Name        string            `json:"name"`
	Path        string            `json:"path"`
	Type        string            `json:"type"`
	Children    []fsProxyFileNode `json:"children,omitempty"`
	Size        int64             `json:"size"`
	Modified    string            `json:"modified,omitempty"`
	HasChildren bool              `json:"has_children"`
}

func convertFsProxyNode(n *fsProxyFileNode) *reliantv1.FileNode {
	node := &reliantv1.FileNode{
		Name: n.Name,
		Path: n.Path,
	}
	if n.Modified != "" {
		node.Modified = proto.String(n.Modified)
	}

	if n.Type == "directory" {
		node.Type = reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY
		// has_children lets the UI render an expand chevron for a lazily-loaded
		// directory whose children were not eagerly included by a depth-limited
		// walk. When children are present, derive it too so the hint is never
		// stale relative to the payload.
		node.HasChildren = n.HasChildren || len(n.Children) > 0
		if len(n.Children) > 0 {
			node.Children = make([]*reliantv1.FileNode, len(n.Children))
			for i := range n.Children {
				node.Children[i] = convertFsProxyNode(&n.Children[i])
			}
		}
	} else {
		node.Type = reliantv1.FileNodeType_FILE_NODE_TYPE_FILE
		node.Size = proto.Int64(n.Size)
	}
	return node
}

// GetFileContent returns the content of a specific file.
func (s *FileSystemProxyService) GetFileContent(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFileContentRequest],
) (*connect.Response[reliantv1.GetFileContentResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	resolvedPath, err := s.resolvePath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId, req.Msg.Path)
	if err != nil {
		return nil, err
	}

	cmdReq := map[string]any{
		"path": resolvedPath,
	}

	var cmdResp struct {
		Content    string `json:"content"`
		TotalLines int    `json:"total_lines"`
		Truncated  bool   `json:"truncated"`
		Size       int64  `json:"size"`
	}
	if err := s.sendCommand(ctx, userID, "fs.read_file", cmdReq, &cmdResp, 30000); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetFileContentResponse{
		Content: cmdResp.Content,
	}), nil
}

// SaveFileContent saves content to a specific file.
func (s *FileSystemProxyService) SaveFileContent(
	ctx context.Context,
	req *connect.Request[reliantv1.SaveFileContentRequest],
) (*connect.Response[reliantv1.SaveFileContentResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	resolvedPath, err := s.resolvePath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId, req.Msg.Path)
	if err != nil {
		return nil, err
	}

	cmdReq := map[string]any{
		"path":    resolvedPath,
		"content": req.Msg.Content,
	}

	var cmdResp struct {
		Created      bool   `json:"created"`
		BytesWritten int    `json:"bytes_written"`
		ModTime      string `json:"mod_time"`
	}
	if err := s.sendCommand(ctx, userID, "fs.write_file", cmdReq, &cmdResp, 30000); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.SaveFileContentResponse{
		Message: "File saved successfully",
	}), nil
}

// GetFileMetadata returns metadata for a file or directory.
func (s *FileSystemProxyService) GetFileMetadata(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFileMetadataRequest],
) (*connect.Response[reliantv1.GetFileMetadataResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	resolvedPath, err := s.resolvePath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId, req.Msg.Path)
	if err != nil {
		return nil, err
	}

	cmdReq := map[string]any{
		"path": resolvedPath,
	}

	var cmdResp struct {
		Exists  bool      `json:"exists"`
		Size    int64     `json:"size"`
		ModTime time.Time `json:"mod_time"`
		IsDir   bool      `json:"is_dir"`
		Mode    string    `json:"mode"`
	}
	if err := s.sendCommand(ctx, userID, "fs.stat", cmdReq, &cmdResp, 30000); err != nil {
		return nil, err
	}

	if !cmdResp.Exists {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("path not found: %s", req.Msg.Path))
	}

	nodeType := reliantv1.FileNodeType_FILE_NODE_TYPE_FILE
	if cmdResp.IsDir {
		nodeType = reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY
	}

	return connect.NewResponse(&reliantv1.GetFileMetadataResponse{
		Metadata: &reliantv1.FileMetadata{
			Name:        baseName(req.Msg.Path),
			Path:        req.Msg.Path,
			Size:        cmdResp.Size,
			Modified:    cmdResp.ModTime.Format(time.RFC3339),
			Type:        nodeType,
			Permissions: cmdResp.Mode,
		},
	}), nil
}

// GetFilePreviewInfo returns preview metadata for a file.
func (s *FileSystemProxyService) GetFilePreviewInfo(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFilePreviewInfoRequest],
) (*connect.Response[reliantv1.GetFilePreviewInfoResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	resolvedPath, err := s.resolvePath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId, req.Msg.Path)
	if err != nil {
		return nil, err
	}

	cmdReq := map[string]any{
		"path": resolvedPath,
	}

	var cmdResp struct {
		Name       string `json:"name"`
		Path       string `json:"path"`
		Size       int64  `json:"size"`
		Modified   string `json:"modified"`
		ViewerKind string `json:"viewer_kind"`
		MIMEType   string `json:"mime_type"`
		IsBinary   bool   `json:"is_binary"`
		IsEditable bool   `json:"is_editable"`
	}
	if err := s.sendCommand(ctx, userID, "fs.preview_info", cmdReq, &cmdResp, 30000); err != nil {
		// Requesting preview info for a directory is a client-input condition
		// (e.g. the UI asking for a tree folder), not a server failure. Return
		// the same typed error as the local FileSystemService so it stays out
		// of ERROR logs / Sentry. The daemon-side error text is the contract
		// here (cmd_fs.go returns "path is a directory: <path>").
		if strings.Contains(err.Error(), "path is a directory") {
			logging.Debug("[FSProxy] GetFilePreviewInfo requested for a directory",
				"path", req.Msg.Path)
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("path is a directory"))
		}
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetFilePreviewInfoResponse{
		Info: &reliantv1.FilePreviewInfo{
			Name:       cmdResp.Name,
			Path:       cmdResp.Path,
			Size:       cmdResp.Size,
			Modified:   cmdResp.Modified,
			ViewerKind: viewerKindFromString(cmdResp.ViewerKind),
			MimeType:   cmdResp.MIMEType,
			IsBinary:   cmdResp.IsBinary,
			IsEditable: cmdResp.IsEditable,
		},
	}), nil
}

// CreateFileOrFolder creates a new file or folder.
func (s *FileSystemProxyService) CreateFileOrFolder(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateFileOrFolderRequest],
) (*connect.Response[reliantv1.CreateFileOrFolderResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	resolvedPath, err := s.resolvePath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId, req.Msg.Path)
	if err != nil {
		return nil, err
	}

	isFolder := req.Msg.Type == reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY

	if isFolder {
		cmdReq := map[string]any{
			"path": resolvedPath,
		}
		var cmdResp struct{}
		if err := s.sendCommand(ctx, userID, "fs.mkdir", cmdReq, &cmdResp, 30000); err != nil {
			return nil, err
		}
	} else {
		cmdReq := map[string]any{
			"path":    resolvedPath,
			"content": req.Msg.Content,
		}
		var cmdResp struct {
			Created      bool   `json:"created"`
			BytesWritten int    `json:"bytes_written"`
			ModTime      string `json:"mod_time"`
		}
		if err := s.sendCommand(ctx, userID, "fs.write_file", cmdReq, &cmdResp, 30000); err != nil {
			return nil, err
		}
	}

	action := "File"
	if isFolder {
		action = "Folder"
	}
	return connect.NewResponse(&reliantv1.CreateFileOrFolderResponse{
		Message: action + " created successfully",
		Path:    req.Msg.Path,
	}), nil
}

// DeleteFileOrFolder deletes a file or folder.
func (s *FileSystemProxyService) DeleteFileOrFolder(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteFileOrFolderRequest],
) (*connect.Response[reliantv1.DeleteFileOrFolderResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	resolvedPath, err := s.resolvePath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId, req.Msg.Path)
	if err != nil {
		return nil, err
	}

	cmdReq := map[string]any{
		"path": resolvedPath,
	}

	var cmdResp struct{}
	if err := s.sendCommand(ctx, userID, "fs.delete", cmdReq, &cmdResp, 30000); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.DeleteFileOrFolderResponse{
		Message: "Deleted successfully",
	}), nil
}

// CopyFile copies a file to a new location.
func (s *FileSystemProxyService) CopyFile(
	ctx context.Context,
	req *connect.Request[reliantv1.CopyFileRequest],
) (*connect.Response[reliantv1.CopyFileResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	// Both endpoints are confined, not merely joined: filepath.Join(base,
	// "../../etc/passwd") cleans into an escape, so a copy could previously
	// read from or write to any path on the daemon.
	sourcePath, err := s.resolvePath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId, req.Msg.SourcePath)
	if err != nil {
		return nil, err
	}
	destPath, err := s.resolvePath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId, req.Msg.DestinationPath)
	if err != nil {
		return nil, err
	}

	cmdReq := map[string]any{
		"source":      sourcePath,
		"destination": destPath,
	}

	var cmdResp struct {
		Message     string `json:"message"`
		Destination string `json:"destination"`
	}
	if err := s.sendCommand(ctx, userID, "fs.copy", cmdReq, &cmdResp, 30000); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.CopyFileResponse{
		Message:     cmdResp.Message,
		Destination: cmdResp.Destination,
	}), nil
}

// SearchFiles searches for text within files in the workspace.
func (s *FileSystemProxyService) SearchFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.SearchFilesRequest],
) (*connect.Response[reliantv1.SearchFilesResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	searchPath := ""
	if req.Msg.Path != nil {
		searchPath = *req.Msg.Path
	}
	resolvedPath, err := s.resolvePath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId, searchPath)
	if err != nil {
		return nil, err
	}

	opts := map[string]any{
		"output_mode": "content",
		// Always set: resolvedPath is absolute and confined, and leaving
		// base_dir unset would let the daemon search from its own working
		// directory instead of the workspace.
		"base_dir": resolvedPath,
	}
	if req.Msg.FilePattern != nil {
		opts["file_glob"] = *req.Msg.FilePattern
	}
	if req.Msg.CaseSensitive != nil && !*req.Msg.CaseSensitive {
		opts["case_insensitive"] = true
	}
	if req.Msg.MaxResults != nil {
		opts["max_results"] = *req.Msg.MaxResults
	}
	if req.Msg.ContextLines != nil {
		opts["context_before"] = *req.Msg.ContextLines
		opts["context_after"] = *req.Msg.ContextLines
	}

	cmdReq := map[string]any{
		"pattern": req.Msg.Query,
		"opts":    opts,
	}

	var cmdResp struct {
		Matches   []daemonSearchMatch `json:"matches"`
		Truncated bool                `json:"truncated"`
	}
	if err := s.sendCommand(ctx, userID, "fs.search", cmdReq, &cmdResp, 60000); err != nil {
		return nil, err
	}

	// Group matches by file path for the proto response format
	fileResults := make(map[string]*reliantv1.SearchResult)
	var fileOrder []string
	for _, m := range cmdResp.Matches {
		sr, exists := fileResults[m.File]
		if !exists {
			sr = &reliantv1.SearchResult{Path: m.File}
			fileResults[m.File] = sr
			fileOrder = append(fileOrder, m.File)
		}
		sr.Matches = append(sr.Matches, &reliantv1.SearchMatch{
			LineNumber:  int32(m.Line),
			LineContent: m.Content,
		})
	}

	results := make([]*reliantv1.SearchResult, 0, len(fileOrder))
	totalMatches := int32(0)
	for _, path := range fileOrder {
		sr := fileResults[path]
		totalMatches += int32(len(sr.Matches))
		results = append(results, sr)
	}

	return connect.NewResponse(&reliantv1.SearchFilesResponse{
		Results:      results,
		TotalMatches: totalMatches,
		Truncated:    cmdResp.Truncated,
	}), nil
}

// daemonSearchMatch mirrors daemon.SearchMatch for JSON deserialization.
type daemonSearchMatch struct {
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Content    string `json:"content,omitempty"`
	MatchCount int    `json:"match_count,omitempty"`
}

// ReplaceInFiles replaces text in files across the workspace.
func (s *FileSystemProxyService) ReplaceInFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.ReplaceInFilesRequest],
) (*connect.Response[reliantv1.ReplaceInFilesResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	// base_dir is the only thing bounding this walk. Omitting it (which an
	// empty base used to do) leaves the daemon to fall back to its own working
	// directory and rewrite files across the whole machine.
	basePath, err := s.requireProjectBase(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}

	opts := map[string]any{
		"base_dir": basePath,
	}
	if req.Msg.FilePattern != nil {
		opts["file_glob"] = *req.Msg.FilePattern
	}
	if req.Msg.CaseSensitive != nil && !*req.Msg.CaseSensitive {
		opts["ignore_case"] = true
	}

	cmdReq := map[string]any{
		"pattern":     req.Msg.SearchText,
		"replacement": req.Msg.ReplaceText,
		"opts":        opts,
	}

	var cmdResp struct {
		FilesChanged int `json:"files_changed"`
		Changes      []struct {
			File         string `json:"file"`
			Replacements int    `json:"replacements"`
		} `json:"changes"`
	}
	if err := s.sendCommand(ctx, userID, "fs.find_replace", cmdReq, &cmdResp, 60000); err != nil {
		return nil, err
	}

	results := make([]*reliantv1.ReplaceResult, len(cmdResp.Changes))
	totalReplacements := int32(0)
	for i, c := range cmdResp.Changes {
		results[i] = &reliantv1.ReplaceResult{
			Path:         c.File,
			Replacements: int32(c.Replacements),
			Success:      true,
		}
		totalReplacements += int32(c.Replacements)
	}

	return connect.NewResponse(&reliantv1.ReplaceInFilesResponse{
		Results:           results,
		TotalReplacements: totalReplacements,
		FilesModified:     int32(cmdResp.FilesChanged),
	}), nil
}

// viewerKindFromString maps the daemon's viewer_kind string to the proto enum.
func viewerKindFromString(kind string) reliantv1.FileViewerKind {
	switch kind {
	case "text":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_TEXT
	case "image":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_IMAGE
	case "pdf":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_PDF
	case "audio":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_AUDIO
	case "video":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_VIDEO
	case "binary":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_BINARY
	default:
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_UNSPECIFIED
	}
}

// ListDirectory lists entries in an arbitrary filesystem directory.
func (s *FileSystemProxyService) ListDirectory(
	ctx context.Context,
	req *connect.Request[reliantv1.ListDirectoryRequest],
) (*connect.Response[reliantv1.ListDirectoryResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	path := req.Msg.Path

	cmdReq := map[string]any{
		"path": path,
	}

	var cmdResp struct {
		// When path is empty the daemon resolves to home and returns it.
		Path    string            `json:"path"`
		Entries []fsProxyDirEntry `json:"entries"`
	}
	if err := s.sendCommand(ctx, userID, "fs.list_dir", cmdReq, &cmdResp, 5000); err != nil {
		return nil, err
	}

	resolvedPath := cmdResp.Path
	if resolvedPath == "" {
		resolvedPath = path
	}

	entries := make([]*reliantv1.DirectoryEntry, 0, len(cmdResp.Entries))
	for _, e := range cmdResp.Entries {
		fullPath := e.Name
		if resolvedPath != "" {
			fullPath = resolvedPath + "/" + e.Name
		}
		entries = append(entries, &reliantv1.DirectoryEntry{
			Name:        e.Name,
			Path:        fullPath,
			IsDirectory: e.IsDir,
			IsHidden:    len(e.Name) > 0 && e.Name[0] == '.',
			IsSymlink:   e.IsSymlink,
		})
	}

	return connect.NewResponse(&reliantv1.ListDirectoryResponse{
		Path:    resolvedPath,
		Entries: entries,
	}), nil
}

// CreateDirectory creates a new directory at an arbitrary absolute path by
// forwarding an fs.mkdir command to the user's daemon. Mirrors ListDirectory:
// the path is an absolute filesystem path on the daemon (not project-scoped).
func (s *FileSystemProxyService) CreateDirectory(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateDirectoryRequest],
) (*connect.Response[reliantv1.CreateDirectoryResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	path := req.Msg.Path
	if path == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("path is required"))
	}
	if !filepath.IsAbs(path) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("path must be absolute"))
	}

	cmdReq := map[string]any{
		"path": path,
	}
	var cmdResp struct{}
	if err := s.sendCommand(ctx, userID, "fs.mkdir", cmdReq, &cmdResp, 5000); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.CreateDirectoryResponse{
		Path: path,
	}), nil
}

// fsProxyDirEntry mirrors the daemon's DirEntry JSON shape.
type fsProxyDirEntry struct {
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	IsSymlink bool   `json:"is_symlink"`
}

// baseName extracts the base name from a path string.
func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
