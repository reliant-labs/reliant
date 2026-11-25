package file_system

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
)

// GetFileUploadStatus reports whether an upload's session still exists on
// its machine and how many bytes arrived, so a client resumes a failed upload
// where it stopped, or begins again when exists is false. A legacy upload has
// no session: exists is false.
func (s *Service) GetFileUploadStatus(
	ctx context.Context,
	req *connect.Request[pb.GetFileUploadStatusRequest],
) (*connect.Response[pb.GetFileUploadStatusResponse], error) {
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
		return connect.NewResponse(&pb.GetFileUploadStatusResponse{}), nil
	}
	if err := validateUploadID(req.Msg.UploadId); err != nil {
		return nil, err
	}
	root, err := scope.uploadRoot()
	if err != nil {
		return nil, err
	}

	cmdReq := daemon.UploadSessionRequest{UploadID: req.Msg.UploadId, Root: root}
	var status daemon.UploadStatusResponse
	if err := s.sendUploadCommand(ctx, userID, scope.target, daemon.UploadStatusCommand, cmdReq, &status, uploadStatusTimeoutMs); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.GetFileUploadStatusResponse{
		Exists:        status.Exists,
		BytesReceived: status.BytesReceived,
		Size:          status.Size,
	}), nil
}
