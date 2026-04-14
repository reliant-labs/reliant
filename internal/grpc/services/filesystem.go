// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/filepreview"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/localfs"
	"github.com/reliant-labs/reliant/internal/logging"
)

// FileSystemService implements the FileSystemService RPC handlers
type FileSystemService struct {
	reliantv1connect.UnimplementedFileSystemServiceHandler
	database db.Repository
	fs       localfs.FS
	basePath string // If set, used as the base path instead of DB lookups
}

// NewFileSystemService creates a new FileSystemService
func NewFileSystemService(database db.Repository) *FileSystemService {
	return &FileSystemService{
		database: database,
		fs:       localfs.New(),
	}
}

// NewFileSystemServiceWithBasePath creates a FileSystemService that uses a fixed
// base path for all file operations instead of resolving paths via the database.
// This is used by the daemon runtime where project/worktree records are not
// available in the local DB.
func NewFileSystemServiceWithBasePath(database db.Repository, basePath string) *FileSystemService {
	return &FileSystemService{
		database: database,
		fs:       localfs.New(),
		basePath: basePath,
	}
}

// ============================================================================
// Helper Methods
// ============================================================================

// resolveBasePath resolves the correct base path for file operations.
// If a fixed basePath was configured (e.g. daemon runtime), it is returned
// directly without any DB lookups. Otherwise it falls back to resolving via
// worktree/chat/project records in the database.
func (s *FileSystemService) resolveBasePath(ctx context.Context, projectID string, worktreeID *string, chatID *string) (string, error) {
	if s.basePath != "" {
		return s.basePath, nil
	}
	basePath, err := filepreview.ResolveBasePath(ctx, s.database, projectID, worktreeID, chatID)
	if err != nil {
		return "", err
	}
	return basePath, nil
}

// validatePath performs security checks to ensure path is within base directory
func (s *FileSystemService) validatePath(basePath, requestedPath string) (string, error) {
	absFullPath, err := filepreview.ValidatePath(basePath, requestedPath)
	if err != nil {
		if err == filepreview.ErrPathOutsideBase {
			return "", connect.NewError(connect.CodePermissionDenied, err)
		}
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	return absFullPath, nil
}

// buildFileTree builds a file tree from a directory
func (s *FileSystemService) buildFileTree(fsPath string, relativePath string, showHidden bool) ([]*reliantv1.FileNode, error) {
	entries, err := s.fs.ReadDir(fsPath)
	if err != nil {
		return nil, err
	}

	var nodes []*reliantv1.FileNode
	for _, entry := range entries {
		// Skip common ignore patterns (always skip these)
		if entry.Name() == "node_modules" ||
			entry.Name() == "dist" ||
			entry.Name() == "__pycache__" {
			continue
		}

		// Skip hidden files unless showHidden is true
		if !showHidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		entryPath := filepath.Join(fsPath, entry.Name())
		relPath := filepath.Join(relativePath, entry.Name())

		info, err := entry.Info()
		if err != nil {
			continue
		}

		node := &reliantv1.FileNode{
			Name: entry.Name(),
			Path: relPath,
		}

		if entry.IsDir() {
			node.Type = reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY
			// Recursively build children
			children, err := s.buildFileTree(entryPath, relPath, showHidden)
			if err == nil {
				node.Children = children
			}
		} else {
			node.Type = reliantv1.FileNodeType_FILE_NODE_TYPE_FILE
			size := info.Size()
			node.Size = proto.Int64(size)
			modTime := info.ModTime().Format(time.RFC3339)
			node.Modified = proto.String(modTime)
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

// isBinaryFile checks if a file is binary using the canonical preview classifier.
func isBinaryFile(filePath string, content []byte) bool {
	classification := filepreview.Classify(filePath, content)
	if classification.IsBinary {
		return true
	}

	// Check for executable files without extensions (common on Unix systems)
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == "" && len(content) > 4 {
		// ELF (Linux): \x7FELF
		if content[0] == 0x7F && content[1] == 'E' && content[2] == 'L' && content[3] == 'F' {
			return true
		}
		// Mach-O (macOS): \xFE\xED\xFA or \xCF\xFA\xED\xFE (32/64-bit)
		if (content[0] == 0xFE && content[1] == 0xED && content[2] == 0xFA) ||
			(content[0] == 0xCF && content[1] == 0xFA && content[2] == 0xED && content[3] == 0xFE) ||
			(content[0] == 0xCE && content[1] == 0xFA && content[2] == 0xED && content[3] == 0xFE) {
			return true
		}
		// PE (Windows): MZ
		if content[0] == 'M' && content[1] == 'Z' {
			return true
		}
	}

	return filepreview.HasBinaryContent(content)
}

// ============================================================================
// RPC Handlers
// ============================================================================

// GetFileTree returns the file tree structure for a project
func (s *FileSystemService) GetFileTree(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFileTreeRequest],
) (*connect.Response[reliantv1.GetFileTreeResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Resolve base path (worktree or project)
	basePath, err := s.resolveBasePath(ctx, projectID, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Get requested path (default to root)
	requestedPath := req.Msg.Path
	if requestedPath == "" {
		requestedPath = "/"
	}

	// Validate path
	absFullPath, err := s.validatePath(basePath, requestedPath)
	if err != nil {
		return nil, err
	}

	// Build file tree
	files, err := s.buildFileTree(absFullPath, requestedPath, req.Msg.ShowHidden)
	if err != nil {
		logging.Error("Failed to read directory", "error", err, "path", absFullPath)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&reliantv1.GetFileTreeResponse{
		Files: files,
	}), nil
}

// GetFileContent returns the content of a specific file
func (s *FileSystemService) GetFileContent(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFileContentRequest],
) (*connect.Response[reliantv1.GetFileContentResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	requestedPath := req.Msg.Path
	if requestedPath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Resolve base path (worktree or project)
	basePath, err := s.resolveBasePath(ctx, projectID, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Validate path
	absFullPath, err := s.validatePath(basePath, requestedPath)
	if err != nil {
		return nil, err
	}

	// Read file
	content, err := s.fs.ReadFile(absFullPath)
	if err != nil {
		if s.fs.IsNotExist(err) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	classification := filepreview.Classify(absFullPath, content)

	// Most binary files remain blocked from the raw content API, but PDFs are
	// intentionally allowed so they can open in the editor as raw source/text.
	if isBinaryFile(absFullPath, content) && classification.ViewerKind != filepreview.ViewerKindPDF {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			connect.NewError(connect.CodeFailedPrecondition, nil))
	}

	textContent := string(content)
	if classification.ViewerKind == filepreview.ViewerKindPDF {
		textContent = strings.ToValidUTF8(textContent, "�")
	}

	return connect.NewResponse(&reliantv1.GetFileContentResponse{
		Content: textContent,
	}), nil
}

// SaveFileContent saves content to a specific file
func (s *FileSystemService) SaveFileContent(
	ctx context.Context,
	req *connect.Request[reliantv1.SaveFileContentRequest],
) (*connect.Response[reliantv1.SaveFileContentResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	requestedPath := req.Msg.Path
	if requestedPath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Resolve base path (worktree or project)
	basePath, err := s.resolveBasePath(ctx, projectID, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Validate path
	absFullPath, err := s.validatePath(basePath, requestedPath)
	if err != nil {
		return nil, err
	}

	// Ensure parent directory exists
	parentDir := filepath.Dir(absFullPath)
	if err := s.fs.MkdirAll(parentDir, 0755); err != nil {
		logging.Error("Failed to create parent directory", "error", err, "path", parentDir)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Write file
	if err := s.fs.WriteFile(absFullPath, []byte(req.Msg.Content), 0644); err != nil {
		logging.Error("Failed to write file", "error", err, "path", absFullPath)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	logging.Info("File saved successfully", "path", requestedPath, "project", projectID)

	return connect.NewResponse(&reliantv1.SaveFileContentResponse{
		Message: "File saved successfully",
	}), nil
}

// GetFileMetadata returns metadata for a file or directory
func (s *FileSystemService) GetFileMetadata(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFileMetadataRequest],
) (*connect.Response[reliantv1.GetFileMetadataResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	requestedPath := req.Msg.Path
	if requestedPath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Resolve base path (worktree or project)
	basePath, err := s.resolveBasePath(ctx, projectID, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Validate path
	absFullPath, err := s.validatePath(basePath, requestedPath)
	if err != nil {
		return nil, err
	}

	// Get file info
	info, err := s.fs.Stat(absFullPath)
	if err != nil {
		if s.fs.IsNotExist(err) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	fileType := "file"
	if info.IsDir() {
		fileType = "directory"
	}

	return connect.NewResponse(&reliantv1.GetFileMetadataResponse{
		Metadata: &reliantv1.FileMetadata{
			Name:        info.Name(),
			Path:        requestedPath,
			Size:        info.Size(),
			Modified:    info.ModTime().Format(time.RFC3339),
			Type:        fileNodeTypeFromString(fileType),
			Permissions: info.Mode().String(),
		},
	}), nil
}

// GetFilePreviewInfo returns preview metadata for a file.
func (s *FileSystemService) GetFilePreviewInfo(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFilePreviewInfoRequest],
) (*connect.Response[reliantv1.GetFilePreviewInfoResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	requestedPath := req.Msg.Path
	if requestedPath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	basePath, err := s.resolveBasePath(ctx, projectID, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	absFullPath, err := s.validatePath(basePath, requestedPath)
	if err != nil {
		return nil, err
	}

	info, err := s.fs.Stat(absFullPath)
	if err != nil {
		if s.fs.IsNotExist(err) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if info.IsDir() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("path is a directory"))
	}

	sample, err := s.readPreviewSample(absFullPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	classification := filepreview.Classify(absFullPath, sample)

	return connect.NewResponse(&reliantv1.GetFilePreviewInfoResponse{
		Info: &reliantv1.FilePreviewInfo{
			Name:       info.Name(),
			Path:       requestedPath,
			Size:       info.Size(),
			Modified:   info.ModTime().Format(time.RFC3339),
			ViewerKind: viewerKindToProto(classification.ViewerKind),
			MimeType:   classification.MIMEType,
			IsBinary:   classification.IsBinary,
			IsEditable: classification.IsEditable,
		},
	}), nil
}

// GetFilePreview returns raw binary file content for previewable files (images, PDFs, audio, video).
func (s *FileSystemService) GetFilePreview(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFilePreviewRequest],
) (*connect.Response[reliantv1.GetFilePreviewResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	requestedPath := req.Msg.Path
	if requestedPath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	basePath, err := s.resolveBasePath(ctx, projectID, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	absFullPath, err := s.validatePath(basePath, requestedPath)
	if err != nil {
		return nil, err
	}

	info, err := s.fs.Stat(absFullPath)
	if err != nil {
		if s.fs.IsNotExist(err) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if info.IsDir() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("path is a directory"))
	}

	sample, err := s.readPreviewSample(absFullPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	classification := filepreview.Classify(absFullPath, sample)

	switch classification.ViewerKind {
	case filepreview.ViewerKindImage, filepreview.ViewerKindPDF, filepreview.ViewerKindAudio, filepreview.ViewerKindVideo:
		// previewable
	default:
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("file type is not previewable"))
	}

	content, err := s.fs.ReadFile(absFullPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&reliantv1.GetFilePreviewResponse{
		Content:     content,
		ContentType: classification.MIMEType,
		Filename:    filepath.Base(absFullPath),
		Size:        info.Size(),
	}), nil
}

// CreateFileOrFolder creates a new file or folder
func (s *FileSystemService) CreateFileOrFolder(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateFileOrFolderRequest],
) (*connect.Response[reliantv1.CreateFileOrFolderResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	requestedPath := req.Msg.Path
	if requestedPath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	fileType := req.Msg.Type
	if fileType != reliantv1.FileNodeType_FILE_NODE_TYPE_FILE && fileType != reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Resolve base path (worktree or project)
	basePath, err := s.resolveBasePath(ctx, projectID, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Validate path
	absFullPath, err := s.validatePath(basePath, requestedPath)
	if err != nil {
		return nil, err
	}

	// Check if file/folder already exists
	if _, err := s.fs.Stat(absFullPath); err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, nil)
	}

	// Create file or directory
	if fileType == reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY {
		if err := s.fs.MkdirAll(absFullPath, 0755); err != nil {
			logging.Error("Failed to create directory", "error", err, "path", absFullPath)
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		logging.Info("Directory created successfully", "path", requestedPath, "project", projectID)
	} else {
		// Ensure parent directory exists
		parentDir := filepath.Dir(absFullPath)
		if err := s.fs.MkdirAll(parentDir, 0755); err != nil {
			logging.Error("Failed to create parent directory", "error", err, "path", parentDir)
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		// Create file with content (or empty)
		if err := s.fs.WriteFile(absFullPath, []byte(req.Msg.Content), 0644); err != nil {
			logging.Error("Failed to create file", "error", err, "path", absFullPath)
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		logging.Info("File created successfully", "path", requestedPath, "project", projectID)
	}

	return connect.NewResponse(&reliantv1.CreateFileOrFolderResponse{
		Message: "Created successfully",
		Path:    requestedPath,
	}), nil
}

// DeleteFileOrFolder deletes a file or folder
func (s *FileSystemService) DeleteFileOrFolder(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteFileOrFolderRequest],
) (*connect.Response[reliantv1.DeleteFileOrFolderResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	requestedPath := req.Msg.Path
	if requestedPath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Resolve base path (worktree or project)
	basePath, err := s.resolveBasePath(ctx, projectID, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Validate path
	absFullPath, err := s.validatePath(basePath, requestedPath)
	if err != nil {
		return nil, err
	}

	// Check if file/folder exists
	info, err := s.fs.Stat(absFullPath)
	if err != nil {
		if s.fs.IsNotExist(err) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Delete file or directory
	if info.IsDir() {
		if err := s.fs.RemoveAll(absFullPath); err != nil {
			logging.Error("Failed to delete directory", "error", err, "path", absFullPath)
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		logging.Info("Directory deleted successfully", "path", requestedPath, "project", projectID)
	} else {
		if err := s.fs.Remove(absFullPath); err != nil {
			logging.Error("Failed to delete file", "error", err, "path", absFullPath)
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		logging.Info("File deleted successfully", "path", requestedPath, "project", projectID)
	}

	return connect.NewResponse(&reliantv1.DeleteFileOrFolderResponse{
		Message: "Deleted successfully",
	}), nil
}

// CopyFile copies a file to a new location
func (s *FileSystemService) CopyFile(
	ctx context.Context,
	req *connect.Request[reliantv1.CopyFileRequest],
) (*connect.Response[reliantv1.CopyFileResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	sourcePath := req.Msg.SourcePath
	destPath := req.Msg.DestinationPath
	if sourcePath == "" || destPath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Resolve base path (worktree or project)
	basePath, err := s.resolveBasePath(ctx, projectID, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Validate source path
	absSourcePath, err := s.validatePath(basePath, sourcePath)
	if err != nil {
		return nil, err
	}

	// Validate destination path
	absDestPath, err := s.validatePath(basePath, destPath)
	if err != nil {
		return nil, err
	}

	// Check if source exists and is a file (not directory)
	info, err := s.fs.Stat(absSourcePath)
	if err != nil {
		if s.fs.IsNotExist(err) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if info.IsDir() {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Check if destination already exists
	if _, err := s.fs.Stat(absDestPath); err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, nil)
	}

	// Read source file
	content, err := s.fs.ReadFile(absSourcePath)
	if err != nil {
		logging.Error("Failed to read source file", "error", err, "path", absSourcePath)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Ensure destination parent directory exists
	parentDir := filepath.Dir(absDestPath)
	if err := s.fs.MkdirAll(parentDir, 0755); err != nil {
		logging.Error("Failed to create destination parent directory", "error", err, "path", parentDir)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Write to destination
	if err := s.fs.WriteFile(absDestPath, content, 0644); err != nil {
		logging.Error("Failed to write destination file", "error", err, "path", absDestPath)
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	logging.Info("File copied successfully", "source", sourcePath, "destination", destPath, "project", projectID)

	return connect.NewResponse(&reliantv1.CopyFileResponse{
		Message:     "File copied successfully",
		Destination: destPath,
	}), nil
}

// SearchFiles searches for text within files in the workspace
func (s *FileSystemService) SearchFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.SearchFilesRequest],
) (*connect.Response[reliantv1.SearchFilesResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	query := req.Msg.Query
	if query == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Resolve base path (worktree or project)
	basePath, err := s.resolveBasePath(ctx, projectID, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Get search path
	searchPath := "/"
	if req.Msg.Path != nil {
		searchPath = *req.Msg.Path
	}

	// Validate search path
	absSearchPath, err := s.validatePath(basePath, searchPath)
	if err != nil {
		return nil, err
	}

	// Set defaults
	maxResults := int32(100)
	if req.Msg.MaxResults != nil && *req.Msg.MaxResults > 0 {
		maxResults = *req.Msg.MaxResults
	}
	// Cap at 500 to prevent performance issues
	if maxResults > 500 {
		maxResults = 500
	}

	contextLines := int32(2)
	if req.Msg.ContextLines != nil {
		contextLines = *req.Msg.ContextLines
	}
	if contextLines > 10 {
		contextLines = 10
	}

	caseSensitive := false
	if req.Msg.CaseSensitive != nil {
		caseSensitive = *req.Msg.CaseSensitive
	}

	filePattern := ""
	if req.Msg.FilePattern != nil {
		filePattern = *req.Msg.FilePattern
	}

	// Perform search
	results, totalMatches, truncated := s.searchInDirectory(absSearchPath, basePath, query, caseSensitive, filePattern, int(maxResults), int(contextLines))

	return connect.NewResponse(&reliantv1.SearchFilesResponse{
		Results:      results,
		TotalMatches: totalMatches,
		Truncated:    truncated,
	}), nil
}

// searchInDirectory recursively searches files in a directory
func (s *FileSystemService) searchInDirectory(
	dirPath string,
	basePath string,
	query string,
	caseSensitive bool,
	filePattern string,
	maxResults int,
	contextLines int,
) ([]*reliantv1.SearchResult, int32, bool) {
	var results []*reliantv1.SearchResult
	var totalMatches int32
	truncated := false

	// Compile the search query
	var searchQuery string
	if caseSensitive {
		searchQuery = query
	} else {
		searchQuery = "(?i)" + query
	}

	re, err := regexp.Compile(searchQuery)
	if err != nil {
		// If regex is invalid, treat as literal string
		escaped := regexp.QuoteMeta(query)
		if !caseSensitive {
			escaped = "(?i)" + escaped
		}
		re, _ = regexp.Compile(escaped)
	}

	// Walk the directory tree
	err = s.fs.Walk(dirPath, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		// Skip directories we always want to ignore
		if info.IsDir() {
			name := info.Name()
			if name == "node_modules" || name == ".git" || name == "dist" ||
				name == "__pycache__" || name == ".next" || name == "vendor" ||
				name == "build" || name == "target" {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if we've hit the max results limit
		if int(totalMatches) >= maxResults {
			truncated = true
			return filepath.SkipAll
		}

		// Skip binary files
		ext := strings.ToLower(filepath.Ext(path))
		if isBinaryExtension(ext) {
			return nil
		}

		// Check file pattern if specified
		if filePattern != "" {
			matched, err := filepath.Match(filePattern, info.Name())
			if err != nil {
				return fmt.Errorf("invalid file pattern: %w", err)
			}
			if !matched {
				return nil
			}
		}

		// Skip large files (>1MB)
		if info.Size() > 1024*1024 {
			return nil
		}

		// Read file content
		content, err := s.fs.ReadFile(path)
		if err != nil {
			return nil
		}

		// Skip binary content
		if isBinaryContent(content) {
			return nil
		}

		// Search for matches in the file
		matches := s.searchInFile(string(content), re, contextLines)
		if len(matches) > 0 {
			// Get relative path from base
			relPath, err := filepath.Rel(basePath, path)
			if err != nil {
				return fmt.Errorf("failed to get relative path: %w", err)
			}
			results = append(results, &reliantv1.SearchResult{
				Path:    relPath,
				Matches: matches,
			})
			totalMatches += int32(len(matches))
		}

		return nil
	})

	if err != nil {
		logging.Error("Error walking directory", "error", err, "path", dirPath)
	}

	return results, totalMatches, truncated
}

// searchInFile searches for matches within a file's content
func (s *FileSystemService) searchInFile(content string, re *regexp.Regexp, contextLines int) []*reliantv1.SearchMatch {
	var matches []*reliantv1.SearchMatch
	lines := strings.Split(content, "\n")

	for lineNum, line := range lines {
		locs := re.FindAllStringIndex(line, -1)
		for _, loc := range locs {
			// Get context before
			var contextBefore []string
			for i := max(0, lineNum-contextLines); i < lineNum; i++ {
				contextBefore = append(contextBefore, lines[i])
			}

			// Get context after
			var contextAfter []string
			for i := lineNum + 1; i <= min(len(lines)-1, lineNum+contextLines); i++ {
				contextAfter = append(contextAfter, lines[i])
			}

			matches = append(matches, &reliantv1.SearchMatch{
				LineNumber:    int32(lineNum + 1), // 1-indexed
				LineContent:   line,
				MatchStart:    int32(loc[0]),
				MatchEnd:      int32(loc[1]),
				ContextBefore: contextBefore,
				ContextAfter:  contextAfter,
			})
		}
	}

	return matches
}

// isBinaryExtension checks if file extension is binary
func isBinaryExtension(ext string) bool {
	return filepreview.IsBinaryExtension(ext)
}

// isBinaryContent checks if content contains binary data
func isBinaryContent(content []byte) bool {
	return filepreview.HasBinaryContent(content)
}

// ReplaceInFiles replaces text in files across the workspace
func viewerKindToProto(kind filepreview.ViewerKind) reliantv1.FileViewerKind {
	switch kind {
	case filepreview.ViewerKindText:
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_TEXT
	case filepreview.ViewerKindImage:
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_IMAGE
	case filepreview.ViewerKindPDF:
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_PDF
	case filepreview.ViewerKindAudio:
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_AUDIO
	case filepreview.ViewerKindVideo:
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_VIDEO
	case filepreview.ViewerKindBinary:
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_BINARY
	default:
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_UNSPECIFIED
	}
}

func (s *FileSystemService) readPreviewSample(path string) ([]byte, error) {
	file, err := s.fs.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	buf := make([]byte, 8192)
	n, err := file.Read(buf)
	if err != nil && err.Error() != "EOF" {
		return nil, err
	}
	return buf[:n], nil
}

func (s *FileSystemService) ReplaceInFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.ReplaceInFilesRequest],
) (*connect.Response[reliantv1.ReplaceInFilesResponse], error) {
	projectID := req.Msg.ProjectId
	searchText := req.Msg.SearchText
	replaceText := req.Msg.ReplaceText
	filePattern := req.Msg.FilePattern
	caseSensitive := req.Msg.CaseSensitive != nil && *req.Msg.CaseSensitive
	filePaths := req.Msg.FilePaths

	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	if searchText == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Resolve base path (worktree or project)
	basePath, err := s.resolveBasePath(ctx, projectID, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Get search path
	searchPath := "/"
	if req.Msg.Path != nil {
		searchPath = *req.Msg.Path
	}

	// Validate search path
	absSearchPath, err := s.validatePath(basePath, searchPath)
	if err != nil {
		return nil, err
	}

	// Compile regex
	var re *regexp.Regexp
	pattern := regexp.QuoteMeta(searchText)
	if caseSensitive {
		re, err = regexp.Compile(pattern)
	} else {
		re, err = regexp.Compile("(?i)" + pattern)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid search pattern: %w", err))
	}

	// Compile file pattern if provided
	var filePatternValue string
	if filePattern != nil {
		filePatternValue = *filePattern
	}

	// Build set of specific file paths if provided
	specificFiles := make(map[string]bool)
	for _, fp := range filePaths {
		specificFiles[fp] = true
	}

	// Walk and replace
	results := []*reliantv1.ReplaceResult{}
	var totalReplacements int32
	var filesModified int32

	err = s.fs.Walk(absSearchPath, func(path string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // Skip errors, continue walking
		}

		// Skip directories
		if info.IsDir() {
			// Skip node_modules, .git, etc.
			base := filepath.Base(path)
			if base == "node_modules" || base == ".git" || base == "vendor" || base == ".next" || base == "dist" || base == "build" {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip large files (> 10MB)
		if info.Size() > 10*1024*1024 {
			return nil
		}

		// Skip binary extensions
		ext := strings.ToLower(filepath.Ext(path))
		if isBinaryExtension(ext) {
			return nil
		}

		// Get relative path (relative to base path)
		relPath, err := filepath.Rel(basePath, path)
		if err != nil {
			return nil
		}

		// Check file pattern
		if filePatternValue != "" {
			matched, err := filepath.Match(filePatternValue, filepath.Base(path))
			if err != nil || !matched {
				return nil
			}
		}

		// Check specific files filter
		if len(specificFiles) > 0 && !specificFiles[relPath] {
			return nil
		}

		// Read file
		content, err := s.fs.ReadFile(path)
		if err != nil {
			results = append(results, &reliantv1.ReplaceResult{
				Path:    relPath,
				Success: false,
				Error:   fmt.Sprintf("failed to read: %v", err),
			})
			return nil
		}

		// Skip binary content
		if isBinaryContent(content) {
			return nil
		}

		// Check for matches
		originalContent := string(content)
		if !re.MatchString(originalContent) {
			return nil
		}

		// Perform replacement
		newContent := re.ReplaceAllString(originalContent, replaceText)
		replacementCount := int32(len(re.FindAllStringIndex(originalContent, -1)))

		// Write file
		if err := s.fs.WriteFile(path, []byte(newContent), info.Mode()); err != nil {
			results = append(results, &reliantv1.ReplaceResult{
				Path:    relPath,
				Success: false,
				Error:   fmt.Sprintf("failed to write: %v", err),
			})
			return nil
		}

		results = append(results, &reliantv1.ReplaceResult{
			Path:         relPath,
			Replacements: replacementCount,
			Success:      true,
		})
		totalReplacements += replacementCount
		filesModified++

		return nil
	})

	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to walk directory: %w", err))
	}

	return connect.NewResponse(&reliantv1.ReplaceInFilesResponse{
		Results:           results,
		TotalReplacements: totalReplacements,
		FilesModified:     filesModified,
	}), nil
}

// ListDirectory lists entries in an arbitrary filesystem directory.
func (s *FileSystemService) ListDirectory(
	ctx context.Context,
	req *connect.Request[reliantv1.ListDirectoryRequest],
) (*connect.Response[reliantv1.ListDirectoryResponse], error) {
	path := req.Msg.Path
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get home directory: %w", err))
		}
		path = home
	}

	if !filepath.IsAbs(path) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("path must be absolute"))
	}

	dirEntries, err := s.fs.ReadDir(path)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("cannot read directory: %w", err))
	}

	entries := make([]*reliantv1.DirectoryEntry, 0, len(dirEntries))
	for _, entry := range dirEntries {
		fullPath := filepath.Join(path, entry.Name())
		info, _ := entry.Info()
		de := &reliantv1.DirectoryEntry{
			Name:        entry.Name(),
			Path:        fullPath,
			IsDirectory: entry.IsDir(),
			IsHidden:    strings.HasPrefix(entry.Name(), "."),
		}
		if info != nil {
			de.IsSymlink = info.Mode()&os.ModeSymlink != 0
		}
		entries = append(entries, de)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDirectory != entries[j].IsDirectory {
			return entries[i].IsDirectory
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	return connect.NewResponse(&reliantv1.ListDirectoryResponse{
		Path:    path,
		Entries: entries,
	}), nil
}
