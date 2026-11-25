package file_system

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
)

// AbortFileUpload discards an upload's staged bytes. An unknown session, and
// a legacy upload (which stages nothing), abort successfully: the outcome the
// caller wanted already holds.
func (s *Service) AbortFileUpload(
	ctx context.Context,
	req *connect.Request[pb.AbortFileUploadRequest],
) (*connect.Response[pb.AbortFileUploadResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	if _, isLegacy, err := decodeLegacyUploadID(req.Msg.UploadId); err != nil {
		return nil, err
	} else if isLegacy {
		return connect.NewResponse(&pb.AbortFileUploadResponse{}), nil
	}
	if err := validateUploadID(req.Msg.UploadId); err != nil {
		return nil, err
	}
	root, err := scope.uploadRoot()
	if err != nil {
		return nil, err
	}

	cmdReq := daemon.UploadSessionRequest{UploadID: req.Msg.UploadId, Root: root}
	var aborted daemon.UploadAbortResponse
	if err := s.sendUploadCommand(ctx, userID, scope.target, daemon.UploadAbortCommand, cmdReq, &aborted, uploadAbortTimeoutMs); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.AbortFileUploadResponse{}), nil
}
