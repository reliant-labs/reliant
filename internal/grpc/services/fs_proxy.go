// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/filepreview"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
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

// resolveProjectPath resolves the absolute filesystem path for a project,
// optionally scoped to a worktree or chat.
func (s *FileSystemProxyService) resolveProjectPath(ctx context.Context, projectID string, worktreeID *string, chatID *string) (string, error) {
	if projectID == "" {
		return "", nil
	}
	return filepreview.ResolveBasePath(ctx, s.database, projectID, worktreeID, chatID)
}

// resolvePath resolves a project-scoped path to an absolute path for the daemon.
func (s *FileSystemProxyService) resolvePath(ctx context.Context, projectID string, worktreeID *string, chatID *string, requestedPath string) (string, error) {
	basePath, err := s.resolveProjectPath(ctx, projectID, worktreeID, chatID)
	if err != nil {
		return "", err
	}
	if basePath == "" {
		return requestedPath, nil
	}
	if requestedPath == "" || requestedPath == "/" {
		return basePath, nil
	}
	return filepath.Join(basePath, requestedPath), nil
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

	resolvedPath, err := s.resolvePath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId, req.Msg.Path)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))
	}

	cmdReq := map[string]any{
		"path":        resolvedPath,
		"show_hidden": req.Msg.ShowHidden,
	}

	var cmdResp struct {
		Nodes []fsProxyFileNode `json:"nodes"`
	}
	if err := s.sendCommand(ctx, userID, "fs.get_tree", cmdReq, &cmdResp, 30000); err != nil {
		return nil, err
	}

	files := make([]*reliantv1.FileNode, len(cmdResp.Nodes))
	for i, n := range cmdResp.Nodes {
		files[i] = convertFsProxyNode(&n)
	}

	return connect.NewResponse(&reliantv1.GetFileTreeResponse{
		Files: files,
	}), nil
}

// fsProxyFileNode mirrors the daemon's fsFileNode JSON shape.
type fsProxyFileNode struct {
	Name     string            `json:"name"`
	Path     string            `json:"path"`
	Type     string            `json:"type"`
	Children []fsProxyFileNode `json:"children,omitempty"`
	Size     int64             `json:"size"`
	Modified string            `json:"modified,omitempty"`
}

func convertFsProxyNode(n *fsProxyFileNode) *reliantv1.FileNode {
	node := &reliantv1.FileNode{
		Name: n.Name,
		Path: n.Path,
	}

	if n.Type == "directory" {
		node.Type = reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY
		if len(n.Children) > 0 {
			node.Children = make([]*reliantv1.FileNode, len(n.Children))
			for i := range n.Children {
				node.Children[i] = convertFsProxyNode(&n.Children[i])
			}
		}
	} else {
		node.Type = reliantv1.FileNodeType_FILE_NODE_TYPE_FILE
		node.Size = proto.Int64(n.Size)
		if n.Modified != "" {
			node.Modified = proto.String(n.Modified)
		}
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
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))
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
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))
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
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))
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
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))
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
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))
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
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))
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

	basePath, err := s.resolveProjectPath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))
	}

	sourcePath := req.Msg.SourcePath
	destPath := req.Msg.DestinationPath
	if basePath != "" {
		sourcePath = filepath.Join(basePath, sourcePath)
		destPath = filepath.Join(basePath, destPath)
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
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))
	}

	opts := map[string]any{
		"output_mode": "content",
	}
	if resolvedPath != "" {
		opts["base_dir"] = resolvedPath
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

	basePath, err := s.resolveProjectPath(ctx, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("resolve project path: %w", err))
	}

	opts := map[string]any{}
	if basePath != "" {
		opts["base_dir"] = basePath
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
