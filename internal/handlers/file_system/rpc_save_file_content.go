package file_system

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// SaveFileContent saves content to a specific file.
func (s *Service) SaveFileContent(
	ctx context.Context,
	req *connect.Request[reliantv1.SaveFileContentRequest],
) (*connect.Response[reliantv1.SaveFileContentResponse], error) {
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

	return connect.NewResponse(&reliantv1.SaveFileContentResponse{
		Message: "File saved successfully",
	}), nil
}
