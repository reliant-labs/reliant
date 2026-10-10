// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/base64"
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
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/filepreview"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// FileSystemProxyService implements FileSystemServiceHandler by forwarding
// requests to a daemon via DaemonCommand (request/response): the machine that
// holds the workspace a request names (see workspaceDaemonID).
type FileSystemProxyService struct {
	reliantv1connect.UnimplementedFileSystemServiceHandler
	router   toolexec.DaemonRouter
	database db.Repository
	wake     machineWake
}

// NewFileSystemProxyService creates a new FileSystemProxyService.
func NewFileSystemProxyService(router toolexec.DaemonRouter, database db.Repository) *FileSystemProxyService {
	return &FileSystemProxyService{
		router:   router,
		database: database,
		wake:     machineWake{router: router, owners: database},
	}
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

// workspaceScope is where a workspace-scoped request acts: the directory it
// is confined to, and the machine that directory is on.
type workspaceScope struct {
	basePath string
	// target is the machine holding basePath (daemonID "" when default
	// resolution picks it), and whether the request was made for a chat with
	// no machine. See workspaceDaemonID.
	target wakeTarget
}

// resolveScope resolves the workspace root a request is scoped to and the
// machine it must run on. Every request that names a project goes through
// here, so a file tree, a preview and a save of the same file cannot land on
// different machines.
func (s *FileSystemProxyService) resolveScope(ctx context.Context, userID, projectID string, worktreeID, chatID *string) (workspaceScope, error) {
	basePath, err := s.requireProjectBase(ctx, projectID, worktreeID, chatID)
	if err != nil {
		return workspaceScope{}, err
	}
	target, err := s.workspaceDaemonID(ctx, userID, projectID, worktreeID, chatID)
	if err != nil {
		return workspaceScope{}, err
	}
	return workspaceScope{basePath: basePath, target: target}, nil
}

// workspaceDaemonID returns the machine a workspace-scoped request must run
// on (daemonID "" leaves it to default resolution), marked noMachine when the
// request was made for a chat that has no machine: such a request must never
// wake one (see machineWake).
//
// The files exist on one machine. For a request made for a chat (chat_id) it
// is the chat's machine: its pinned daemon, else its worktree's owner
// (chatDaemonID, the precedence ExecuteTools routes the chat's tools by). The
// Files tab and the preview must show the files the chat's tools read and
// write. Default resolution picks the user's default machine instead, which is
// the wrong disk for any chat on another machine, main checkout included.
//
// A request that names a worktree some machine owns, other than the chat's
// own, is about that worktree, and goes to its owner: no other machine has the
// checkout. Without a chat, an owned worktree goes to its owner too. Anything
// else (the main checkout, legacy rows that record no owner) keeps default
// resolution.
func (s *FileSystemProxyService) workspaceDaemonID(ctx context.Context, userID, projectID string, worktreeID, chatID *string) (wakeTarget, error) {
	var worktree *db.Worktree
	if worktreeID != nil && *worktreeID != "" {
		wt, err := s.database.GetWorktree(ctx, *worktreeID)
		switch {
		case errors.Is(err, core.ErrWorktreeNotFound):
			// A missing row means "no owner", as it does for tool routing.
		case err != nil:
			logging.Error("[FSProxy] Failed to load worktree to route request", "error", err, "worktreeID", *worktreeID)
			return wakeTarget{}, connect.NewError(connect.CodeInternal, errors.New("failed to resolve the workspace's machine"))
		default:
			worktree = wt
		}
	}
	owner := worktreeOwner(worktree)

	if chatID == nil || *chatID == "" {
		return wakeTarget{daemonID: owner}, nil
	}
	chat, err := s.database.GetChat(ctx, *chatID)
	switch {
	case errors.Is(err, core.ErrChatNotFound):
		// The UI can still name a chat it has just deleted. Degrade to the
		// routing a request without a chat gets rather than fail the tree.
		return wakeTarget{daemonID: owner}, nil
	case err != nil:
		logging.Error("[FSProxy] Failed to load chat to route request", "error", err, "chatID", *chatID)
		return wakeTarget{}, connect.NewError(connect.CodeInternal, errors.New("failed to resolve the chat's machine"))
	case chat.UserID != userID:
		return wakeTarget{}, connect.NewError(connect.CodeNotFound, errors.New("chat not found"))
	case chat.ProjectID != projectID:
		// A chat from another project says nothing about this project's
		// files. It is stale UI context (a project switch racing the tree),
		// and ResolveBasePath ignores it the same way.
		return wakeTarget{daemonID: owner}, nil
	}
	if owner != "" && (chat.WorktreeID == nil || *chat.WorktreeID != worktree.ID) {
		return wakeTarget{daemonID: owner, noMachine: chat.NoMachine}, nil
	}
	daemonID, err := chatDaemonID(ctx, s.database, chat)
	if err != nil {
		logging.Error("[FSProxy] Failed to resolve the chat's machine", "error", err, "chatID", chat.ID)
		return wakeTarget{}, connect.NewError(connect.CodeInternal, errors.New("failed to resolve the chat's machine"))
	}
	if daemonID != "" {
		return wakeTarget{daemonID: daemonID, noMachine: chat.NoMachine}, nil
	}
	return wakeTarget{daemonID: owner, noMachine: chat.NoMachine}, nil
}

// resolve turns a client path into the absolute, confined path the daemon
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
// InvalidArgument for a malformed request — so callers return them unchanged.
// Re-wrapping them as NotFound (which every call site used to do) reported a
// refused traversal as a missing file.
func (w workspaceScope) resolve(requestedPath string) (string, error) {
	return validateWorkspacePath(w.basePath, requestedPath, filepreview.ScopeBaseOnly)
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

// sendCommand is a helper that marshals the request, sends the daemon command
// to the target machine (daemonID "" = default resolution), and unmarshals the
// response. A machine found asleep is woken (machineWake) and the request fails
// as waking, for the client to retry.
func (s *FileSystemProxyService) sendCommand(ctx context.Context, userID string, target wakeTarget, commandType string, req any, resp any, timeoutMs int32) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))
	}

	var respBytes []byte
	if target.daemonID == "" {
		respBytes, err = s.router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	} else {
		respBytes, err = s.router.SendDaemonCommandToDaemon(ctx, userID, target.daemonID, commandType, payload, timeoutMs)
	}
	if err != nil {
		// An asleep machine is woken here. The error then carries the wake,
		// which MachineWakingInterceptor turns into Unavailable + DaemonWaking
		// whatever code is chosen below.
		err = s.wake.afterFailure(ctx, userID, target, err)
		return fsProxyDaemonError(err)
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))
	}
	return nil
}

// fsProxyDaemonError maps a failed daemon command to the code that says what
// happened. The machine's STATE is not a server failure:
//
//   - not reachable right now — starting, asleep (and possibly just woken), or
//     not connected (NATS had no responder) — is Unavailable: retryable, what
//     the web's daemon-wait machinery expects, and its message keeps the
//     "no daemon connected" marker isDaemonConnectingError keys on;
//   - no machine at all is FailedPrecondition: nothing to wait for, and the
//     file tree says "no machine" instead of an error.
//
// The path the request named not being on the machine — a folder the user
// deleted, a worktree that was removed, a checkout never cloned there — is the
// state of the user's disk too: NotFound. As Internal it reached Sentry from
// both the server and the browser (ELECTRON-AW, ELECTRON-9Y).
//
// Only what is left is Internal. Mapping the not-connected case there (it used
// to fall through) logged an ERROR "rpc failed" for every file-tree poll while
// a machine restarted — 117 in five hours for one user's crash-looping machine
// on 2026-10-09 — and handed the UI a 500-class error for a machine that was
// merely starting.
func fsProxyDaemonError(err error) error {
	switch {
	case machineUnreachable(err):
		return connect.NewError(connect.CodeUnavailable, err)
	case toolexec.IsNoDaemon(err):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case isMissingPathError(err):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
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

	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := scope.resolve(req.Msg.Path)
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
	if err := s.sendCommand(ctx, userID, scope.target, "fs.get_tree", cmdReq, &cmdResp, 30000); err != nil {
		if missing := s.checkoutMissingError(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, scope, resolvedPath, err); missing != nil {
			return nil, missing
		}
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
	if prefix := projectRelativePrefix(scope.basePath, resolvedPath); prefix != "" {
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

	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := scope.resolve(req.Msg.Path)
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
	if err := s.sendCommand(ctx, userID, scope.target, "fs.read_file", cmdReq, &cmdResp, 30000); err != nil {
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

	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := scope.resolve(req.Msg.Path)
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
	if err := s.sendCommand(ctx, userID, scope.target, "fs.write_file", cmdReq, &cmdResp, 30000); err != nil {
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

	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := scope.resolve(req.Msg.Path)
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
	if err := s.sendCommand(ctx, userID, scope.target, "fs.stat", cmdReq, &cmdResp, 30000); err != nil {
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

	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := scope.resolve(req.Msg.Path)
	if err != nil {
		return nil, err
	}

	info, err := s.previewInfo(ctx, userID, scope.target, resolvedPath, req.Msg.Path)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetFilePreviewInfoResponse{
		Info: &reliantv1.FilePreviewInfo{
			Name:       info.Name,
			Path:       info.Path,
			Size:       info.Size,
			Modified:   info.Modified,
			ViewerKind: viewerKindFromString(info.ViewerKind),
			MimeType:   info.MIMEType,
			IsBinary:   info.IsBinary,
			IsEditable: info.IsEditable,
		},
	}), nil
}

// fsProxyPreviewInfo mirrors the daemon's fs.preview_info response: the file's
// stat plus internal/filepreview's classification of it, made on the daemon
// from the file's name and first bytes.
type fsProxyPreviewInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Modified   string `json:"modified"`
	ViewerKind string `json:"viewer_kind"`
	MIMEType   string `json:"mime_type"`
	IsBinary   bool   `json:"is_binary"`
	IsEditable bool   `json:"is_editable"`
}

// previewInfo asks the machine holding resolvedPath to stat and classify it.
// requestedPath is the client's path, used only in the log line.
func (s *FileSystemProxyService) previewInfo(ctx context.Context, userID string, target wakeTarget, resolvedPath, requestedPath string) (*fsProxyPreviewInfo, error) {
	var info fsProxyPreviewInfo
	if err := s.sendCommand(ctx, userID, target, "fs.preview_info", map[string]any{"path": resolvedPath}, &info, 30000); err != nil {
		// Requesting preview info for a directory is a client-input condition
		// (e.g. the UI asking for a tree folder), not a server failure. Return
		// the same typed error as the local FileSystemService so it stays out
		// of ERROR logs / Sentry. The daemon-side error text is the contract
		// here (cmd_fs.go returns "path is a directory: <path>").
		if strings.Contains(err.Error(), "path is a directory") {
			logging.Debug("[FSProxy] preview requested for a directory", "path", requestedPath)
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("path is a directory"))
		}
		return nil, err
	}
	return &info, nil
}

// maxFilePreviewBytes caps a binary preview. The whole file crosses the
// daemon's NATS reply as base64 (a third larger, so well under the 64 MB
// chunked-reply cap) and is held in memory here while the response is
// written, so the cap is what keeps one preview from being a memory problem.
// 25 MB covers screenshots, diagrams, PDFs, recordings and short clips; a
// larger file is refused up front with a message that says why.
const maxFilePreviewBytes int64 = 25 << 20

// filePreviewTimeoutMs bounds the binary read: a full-size preview is a few
// chunked NATS round trips, well inside this.
const filePreviewTimeoutMs int32 = 60_000

// GetFilePreview returns the raw bytes of a previewable file (image, PDF,
// audio, video), read on the machine that holds it.
//
// It routes exactly as GetFilePreviewInfo does (resolveScope), so the preview
// comes from the same disk the tree listed. The classification is the
// daemon's fs.preview_info — internal/filepreview applied to the real file —
// and only the four previewable kinds are served, as in the local
// FileSystemService. The bytes come from fs.read_binary_file with the size cap
// passed down, so the daemon refuses an oversized file before reading it.
func (s *FileSystemProxyService) GetFilePreview(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFilePreviewRequest],
) (*connect.Response[reliantv1.GetFilePreviewResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if strings.TrimSpace(req.Msg.Path) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("path is required"))
	}

	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := scope.resolve(req.Msg.Path)
	if err != nil {
		return nil, err
	}

	info, err := s.previewInfo(ctx, userID, scope.target, resolvedPath, req.Msg.Path)
	if err != nil {
		if strings.Contains(err.Error(), "file not found") {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("file not found: %s", req.Msg.Path))
		}
		return nil, err
	}
	switch filepreview.ViewerKind(info.ViewerKind) {
	case filepreview.ViewerKindImage, filepreview.ViewerKindPDF, filepreview.ViewerKindAudio, filepreview.ViewerKindVideo:
	default:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("file type is not previewable"))
	}
	if info.Size > maxFilePreviewBytes {
		return nil, filePreviewTooLarge(info.Size)
	}

	var bin struct {
		Data string `json:"data"`
	}
	cmdReq := map[string]any{"path": resolvedPath, "max_bytes": maxFilePreviewBytes}
	if err := s.sendCommand(ctx, userID, scope.target, "fs.read_binary_file", cmdReq, &bin, filePreviewTimeoutMs); err != nil {
		// The file grew past the cap between the stat and the read.
		if strings.Contains(err.Error(), "exceeds maximum of") {
			return nil, filePreviewTooLarge(info.Size)
		}
		return nil, err
	}
	content, err := base64.StdEncoding.DecodeString(bin.Data)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode preview bytes: %w", err))
	}

	return connect.NewResponse(&reliantv1.GetFilePreviewResponse{
		Content:     content,
		ContentType: info.MIMEType,
		Filename:    info.Name,
		Size:        int64(len(content)),
	}), nil
}

// filePreviewTooLarge is the refusal for a file over maxFilePreviewBytes.
func filePreviewTooLarge(size int64) error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"file is too large to preview (%d MB; the limit is %d MB)", (size+(1<<20)-1)>>20, maxFilePreviewBytes>>20))
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

	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := scope.resolve(req.Msg.Path)
	if err != nil {
		return nil, err
	}

	isFolder := req.Msg.Type == reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY

	if isFolder {
		cmdReq := map[string]any{
			"path": resolvedPath,
		}
		var cmdResp struct{}
		if err := s.sendCommand(ctx, userID, scope.target, "fs.mkdir", cmdReq, &cmdResp, 30000); err != nil {
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
		if err := s.sendCommand(ctx, userID, scope.target, "fs.write_file", cmdReq, &cmdResp, 30000); err != nil {
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

	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := scope.resolve(req.Msg.Path)
	if err != nil {
		return nil, err
	}

	cmdReq := map[string]any{
		"path": resolvedPath,
	}

	var cmdResp struct{}
	if err := s.sendCommand(ctx, userID, scope.target, "fs.delete", cmdReq, &cmdResp, 30000); err != nil {
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
	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	sourcePath, err := scope.resolve(req.Msg.SourcePath)
	if err != nil {
		return nil, err
	}
	destPath, err := scope.resolve(req.Msg.DestinationPath)
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
	if err := s.sendCommand(ctx, userID, scope.target, "fs.copy", cmdReq, &cmdResp, 30000); err != nil {
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
	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := scope.resolve(searchPath)
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
	if err := s.sendCommand(ctx, userID, scope.target, "fs.search", cmdReq, &cmdResp, 60000); err != nil {
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
	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}

	opts := map[string]any{
		"base_dir": scope.basePath,
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
	if err := s.sendCommand(ctx, userID, scope.target, "fs.find_replace", cmdReq, &cmdResp, 60000); err != nil {
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
	// No workspace to follow: the project picker browses the default machine.
	if err := s.sendCommand(ctx, userID, wakeTarget{}, "fs.list_dir", cmdReq, &cmdResp, 5000); err != nil {
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
	// The path is executed on the daemon's filesystem, which may be Windows
	// even when this server is Linux, so it is judged OS-agnostically and
	// forwarded verbatim rather than being cleaned into the host's convention.
	if !ospath.IsAbs(path) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("path must be absolute"))
	}

	cmdReq := map[string]any{
		"path": path,
	}
	var cmdResp struct{}
	if err := s.sendCommand(ctx, userID, wakeTarget{}, "fs.mkdir", cmdReq, &cmdResp, 5000); err != nil {
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
