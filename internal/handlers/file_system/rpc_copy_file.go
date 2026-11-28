package file_system

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// CopyFile copies a file to a new location.
func (s *Service) CopyFile(
	ctx context.Context,
	req *connect.Request[reliantv1.CopyFileRequest],
) (*connect.Response[reliantv1.CopyFileResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
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
	if err := s.sendCommand(ctx, userID, scope.target, "fs.copy", cmdReq, &cmdResp, fsCommandTimeoutMs); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.CopyFileResponse{
		Message:     cmdResp.Message,
		Destination: cmdResp.Destination,
	}), nil
}
