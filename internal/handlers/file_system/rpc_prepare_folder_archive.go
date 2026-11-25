package file_system

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// PrepareFolderArchive starts zipping a folder of the workspace on the
// machine that holds it (transfer.go). The folder is resolved like any
// workspace path (scope.resolve): it must be inside the workspace, and not in
// .git. The daemon checks it again on its own disk, symlinks resolved, walks
// it, and refuses one over the cap before writing anything.
func (s *Service) PrepareFolderArchive(
	ctx context.Context,
	req *connect.Request[pb.PrepareFolderArchiveRequest],
) (*connect.Response[pb.PrepareFolderArchiveResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	folder, err := scope.resolve(req.Msg.Path)
	if err != nil {
		return nil, err
	}
	root, err := scope.uploadRoot()
	if err != nil {
		return nil, err
	}
	rel, ok := workspaceRelative(root, folder)
	if !ok {
		return nil, svcerr.PermissionDenied(daemon.ArchiveRefusedPathMarker + ": the folder is outside the workspace")
	}
	for _, segment := range strings.Split(rel, "/") {
		if strings.EqualFold(segment, ".git") {
			return nil, svcerr.PermissionDenied(daemon.ArchiveRefusedPathMarker + ": a .git directory cannot be downloaded")
		}
	}

	maxSize := s.archiveMaxSize()
	cmdReq := daemon.ArchivePrepareRequest{Root: root, Path: folder, MaxBytes: maxSize}
	var prepared daemon.ArchivePrepareResponse
	if err := s.sendTransferCommand(ctx, userID, scope.target, daemon.ArchivePrepareCommand, cmdReq, &prepared, archivePrepareTimeoutMs); err != nil {
		if daemonerr.IsOutdated(err) {
			return nil, s.rangedDownloadUnsupported(ctx, scope.target, "download folders", err)
		}
		return nil, err
	}
	if validateArchiveID(prepared.ArchiveID) != nil {
		return nil, svcerr.Internal("the machine answered with a malformed archive id")
	}

	name := ospath.Base(folder)
	if name == "" || name == "." || name == "/" || name == `\` {
		name = "workspace"
	}
	return connect.NewResponse(&pb.PrepareFolderArchiveResponse{
		ArchiveId:  prepared.ArchiveID,
		Filename:   name + ".zip",
		TotalFiles: prepared.TotalFiles,
		TotalBytes: prepared.TotalBytes,
		MaxSize:    maxSize,
	}), nil
}
