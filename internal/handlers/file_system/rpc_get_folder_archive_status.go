package file_system

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonerr"
)

// GetFolderArchiveStatus reports how far a folder archive has got
// (transfer.go).
func (s *Service) GetFolderArchiveStatus(
	ctx context.Context,
	req *connect.Request[pb.GetFolderArchiveStatusRequest],
) (*connect.Response[pb.GetFolderArchiveStatusResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateArchiveID(req.Msg.ArchiveId); err != nil {
		return nil, err
	}
	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	root, err := scope.uploadRoot()
	if err != nil {
		return nil, err
	}

	cmdReq := daemon.ArchiveRequest{ArchiveID: req.Msg.ArchiveId, Root: root}
	var status daemon.ArchiveStatusResponse
	if err := s.sendTransferCommand(ctx, userID, scope.target, daemon.ArchiveStatusCommand, cmdReq, &status, archiveStatusTimeoutMs); err != nil {
		if daemonerr.IsOutdated(err) {
			return nil, s.rangedDownloadUnsupported(ctx, scope.target, "download folders", err)
		}
		return nil, err
	}
	state, err := folderArchiveState(status.State)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.GetFolderArchiveStatusResponse{
		State:       state,
		FilesDone:   status.FilesDone,
		FilesTotal:  status.FilesTotal,
		BytesDone:   status.BytesDone,
		BytesTotal:  status.BytesTotal,
		ArchiveSize: status.ArchiveSize,
		Error:       status.Error,
		Skipped:     status.Skipped,
	}), nil
}

// folderArchiveState is a daemon's archive state on the wire.
func folderArchiveState(state string) (pb.FolderArchiveState, error) {
	switch state {
	case daemon.ArchiveStateRunning:
		return pb.FolderArchiveState_FOLDER_ARCHIVE_STATE_RUNNING, nil
	case daemon.ArchiveStateReady:
		return pb.FolderArchiveState_FOLDER_ARCHIVE_STATE_READY, nil
	case daemon.ArchiveStateFailed:
		return pb.FolderArchiveState_FOLDER_ARCHIVE_STATE_FAILED, nil
	}
	return pb.FolderArchiveState_FOLDER_ARCHIVE_STATE_UNSPECIFIED, svcerr.Internal("the machine reported an unknown archive state " + state)
}
