package file_system

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetFileContent returns the content of a specific file.
func (s *Service) GetFileContent(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFileContentRequest],
) (*connect.Response[reliantv1.GetFileContentResponse], error) {
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
		Content       string `json:"content"`
		TotalLines    int    `json:"total_lines"`
		Truncated     bool   `json:"truncated"`
		Size          int64  `json:"size"`
		ViewerGuarded bool   `json:"viewer_guarded"`
	}
	if err := s.sendCommand(ctx, userID, scope.target, "fs.read_file", cmdReq, &cmdResp, fsCommandTimeoutMs); err != nil {
		return nil, err
	}
	if err := requireViewerGuard(cmdResp.ViewerGuarded); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetFileContentResponse{
		Content: cmdResp.Content,
	}), nil
}
