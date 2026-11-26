package file_system

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// CreateFileOrFolder creates a new file or folder.
func (s *Service) CreateFileOrFolder(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateFileOrFolderRequest],
) (*connect.Response[reliantv1.CreateFileOrFolderResponse], error) {
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

	isFolder := req.Msg.Type == reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY

	if isFolder {
		cmdReq := map[string]any{
			"path": resolvedPath,
		}
		var cmdResp struct{}
		if err := s.sendCommand(ctx, userID, scope.target, "fs.mkdir", cmdReq, &cmdResp, fsCommandTimeoutMs); err != nil {
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
		if err := s.sendCommand(ctx, userID, scope.target, "fs.write_file", cmdReq, &cmdResp, fsCommandTimeoutMs); err != nil {
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
