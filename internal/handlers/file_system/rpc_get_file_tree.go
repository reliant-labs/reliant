package file_system

import (
	"context"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

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

// GetFileTree returns the file tree structure for a project.
func (s *Service) GetFileTree(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFileTreeRequest],
) (*connect.Response[reliantv1.GetFileTreeResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
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
		// The tree expands symlinked directories, which can lead anywhere on
		// the machine: the daemon refuses a credential store as the viewer
		// does, judged against the workspace (an older daemon ignores it).
		"viewer_root": scope.basePath,
	}

	var cmdResp struct {
		Nodes     []fsProxyFileNode `json:"nodes"`
		Truncated bool              `json:"truncated"`
		NodeCount int               `json:"node_count"`
	}
	if err := s.sendCommand(ctx, userID, scope.target, "fs.get_tree", cmdReq, &cmdResp, fsCommandTimeoutMs); err != nil {
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
	// carries a full project-relative path — the response contract, and what
	// the UI needs to lazily expand a
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
	// SymlinkTarget and SymlinkStatus are set for a symbolic link; a daemon
	// that predates them sends neither.
	SymlinkTarget string `json:"symlink_target,omitempty"`
	SymlinkStatus string `json:"symlink_status,omitempty"`
}

// symlinkStatusFromWire maps the daemon's symlink status onto the proto enum.
func symlinkStatusFromWire(s string) reliantv1.FileNodeSymlinkStatus {
	switch s {
	case "resolved":
		return reliantv1.FileNodeSymlinkStatus_FILE_NODE_SYMLINK_STATUS_RESOLVED
	case "broken":
		return reliantv1.FileNodeSymlinkStatus_FILE_NODE_SYMLINK_STATUS_BROKEN
	case "loop":
		return reliantv1.FileNodeSymlinkStatus_FILE_NODE_SYMLINK_STATUS_LOOP
	case "denied":
		return reliantv1.FileNodeSymlinkStatus_FILE_NODE_SYMLINK_STATUS_DENIED
	default:
		return reliantv1.FileNodeSymlinkStatus_FILE_NODE_SYMLINK_STATUS_UNSPECIFIED
	}
}

func convertFsProxyNode(n *fsProxyFileNode) *reliantv1.FileNode {
	node := &reliantv1.FileNode{
		Name:          n.Name,
		Path:          n.Path,
		SymlinkStatus: symlinkStatusFromWire(n.SymlinkStatus),
	}
	if n.Modified != "" {
		node.Modified = proto.String(n.Modified)
	}
	if n.SymlinkTarget != "" {
		node.SymlinkTarget = proto.String(n.SymlinkTarget)
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
