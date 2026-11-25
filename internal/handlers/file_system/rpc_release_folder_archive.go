package file_system

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonerr"
)

// ReleaseFolderArchive stops a folder archive being zipped and deletes it
// (transfer.go). An unknown archive, and a machine too old to have made one,
// release successfully: the outcome the caller wanted already holds.
func (s *Service) ReleaseFolderArchive(
	ctx context.Context,
	req *connect.Request[pb.ReleaseFolderArchiveRequest],
) (*connect.Response[pb.ReleaseFolderArchiveResponse], error) {
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
	var released daemon.ArchiveReleaseResponse
	if err := s.sendTransferCommand(ctx, userID, scope.target, daemon.ArchiveReleaseCommand, cmdReq, &released, archiveReleaseTimeoutMs); err != nil {
		if daemonerr.IsOutdated(err) {
			return connect.NewResponse(&pb.ReleaseFolderArchiveResponse{}), nil
		}
		return nil, err
	}
	return connect.NewResponse(&pb.ReleaseFolderArchiveResponse{}), nil
}
