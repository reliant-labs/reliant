package file_system

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
)

// CommitFileUpload moves a fully received upload into place. A destination
// that appeared since BeginFileUpload is refused with ALREADY_EXISTS and the
// session kept, so the client commits again with on_conflict set (OVERWRITE
// or RENAME) without re-sending the bytes.
func (s *Service) CommitFileUpload(
	ctx context.Context,
	req *connect.Request[pb.CommitFileUploadRequest],
) (*connect.Response[pb.CommitFileUploadResponse], error) {
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
		return nil, legacyUploadCommitted()
	}
	if err := validateUploadID(req.Msg.UploadId); err != nil {
		return nil, err
	}
	var override string
	if req.Msg.OnConflict != nil {
		if override, err = uploadConflictWire(*req.Msg.OnConflict); err != nil {
			return nil, err
		}
	}
	root, err := scope.uploadRoot()
	if err != nil {
		return nil, err
	}

	cmdReq := daemon.UploadSessionRequest{UploadID: req.Msg.UploadId, Root: root, OnConflict: override}
	var committed daemon.UploadCommitResponse
	if err := s.sendUploadCommand(ctx, userID, scope.target, daemon.UploadCommitCommand, cmdReq, &committed, uploadCommitTimeoutMs); err != nil {
		return nil, err
	}
	// A user upload is outside every workspace (it went to the machine's
	// uploads folder): its absolute path is the only path it has.
	path := committed.Path
	if committed.Destination != daemon.UploadDestinationUserUploads {
		path = uploadRelativePath(root, committed.Path)
	}
	return connect.NewResponse(&pb.CommitFileUploadResponse{
		Path:         path,
		Size:         committed.Size,
		Created:      committed.Created,
		AbsolutePath: committed.Path,
	}), nil
}
