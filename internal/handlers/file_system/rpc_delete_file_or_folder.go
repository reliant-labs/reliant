package file_system

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// DeleteFileOrFolder deletes a file or folder.
func (s *Service) DeleteFileOrFolder(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteFileOrFolderRequest],
) (*connect.Response[reliantv1.DeleteFileOrFolderResponse], error) {
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
		"path": resolvedPath,
	}

	var cmdResp struct{}
	if err := s.sendCommand(ctx, userID, scope.target, "fs.delete", cmdReq, &cmdResp, fsCommandTimeoutMs); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.DeleteFileOrFolderResponse{
		Message: "Deleted successfully",
	}), nil
}
