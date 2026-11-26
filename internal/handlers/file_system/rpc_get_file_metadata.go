package file_system

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetFileMetadata returns metadata for a file or directory.
func (s *Service) GetFileMetadata(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFileMetadataRequest],
) (*connect.Response[reliantv1.GetFileMetadataResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := s.resolveForView(ctx, userID, scope, req.Msg.Path)
	if err != nil {
		return nil, err
	}

	cmdReq := map[string]any{
		"path":        resolvedPath,
		"viewer_root": scope.basePath,
	}

	var cmdResp struct {
		Exists  bool      `json:"exists"`
		Size    int64     `json:"size"`
		ModTime time.Time `json:"mod_time"`
		IsDir   bool      `json:"is_dir"`
		Mode    string    `json:"mode"`

		ViewerGuarded bool `json:"viewer_guarded"`
	}
	if err := s.sendCommand(ctx, userID, scope.target, "fs.stat", cmdReq, &cmdResp, fsCommandTimeoutMs); err != nil {
		return nil, err
	}
	if err := requireViewerGuard(cmdResp.ViewerGuarded); err != nil {
		return nil, err
	}

	if !cmdResp.Exists {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("path not found: %s", req.Msg.Path))
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

// baseName extracts the base name from a path string.
func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
