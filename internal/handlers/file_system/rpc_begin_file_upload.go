package file_system

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/errreason"
)

// BeginFileUpload starts an upload of one file into a workspace folder: the
// machine that holds the workspace opens a staging session, and the reply
// names its id, the piece size to send, and where the file will land. A
// destination that exists is refused with ALREADY_EXISTS under the fail
// policy, before any bytes are sent. upload.go has the shared rules.
func (s *Service) BeginFileUpload(
	ctx context.Context,
	req *connect.Request[pb.BeginFileUploadRequest],
) (*connect.Response[pb.BeginFileUploadResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	policy, err := uploadConflictWire(req.Msg.OnConflict)
	if err != nil {
		return nil, err
	}
	switch req.Msg.Destination {
	case pb.UploadDestination_UPLOAD_DESTINATION_UNSPECIFIED, pb.UploadDestination_UPLOAD_DESTINATION_WORKSPACE:
	case pb.UploadDestination_UPLOAD_DESTINATION_USER_UPLOADS:
		return s.beginUserUpload(ctx, userID, scope, req.Msg)
	default:
		return nil, svcerr.InvalidArgument(fmt.Sprintf("unknown upload destination %d", req.Msg.Destination))
	}
	dest, err := scope.resolveUploadDest(req.Msg.Directory, req.Msg.Filename)
	if err != nil {
		return nil, err
	}
	maxSize := s.uploadMaxSize()
	size := req.Msg.Size
	if err := checkUploadRequestSize(req.Msg.Filename, size, maxSize); err != nil {
		return nil, err
	}
	root, err := scope.uploadRoot()
	if err != nil {
		return nil, err
	}

	cmdReq := daemon.UploadBeginRequest{Root: root, Path: dest.abs, Size: size, MaxSize: maxSize, OnConflict: policy}
	var cmdResp daemon.UploadBeginResponse
	if err := s.sendUploadCommand(ctx, userID, scope.target, daemon.UploadBeginCommand, cmdReq, &cmdResp, uploadBeginTimeoutMs); err != nil {
		if daemonerr.IsOutdated(err) {
			resp, err := s.beginLegacyUpload(ctx, userID, scope, req.Msg.Directory, dest, req.Msg.Filename, size, policy, maxSize)
			if err != nil {
				return nil, err
			}
			return connect.NewResponse(resp), nil
		}
		return nil, err
	}
	if validateUploadID(cmdResp.UploadID) != nil {
		return nil, svcerr.Internal("the machine answered with a malformed upload id")
	}
	return connect.NewResponse(&pb.BeginFileUploadResponse{
		UploadId:    cmdResp.UploadID,
		ChunkSize:   uploadChunkSize,
		Path:        dest.rel,
		MaxSize:     maxSize,
		Destination: pb.UploadDestination_UPLOAD_DESTINATION_WORKSPACE,
	}), nil
}

// checkUploadRequestSize refuses a negative size, or one over maxSize.
func checkUploadRequestSize(filename string, size, maxSize int64) error {
	if size < 0 {
		return svcerr.InvalidArgument("size must not be negative")
	}
	if size > maxSize {
		return svcerr.InvalidArgument(fmt.Sprintf("%s is %s; files up to %s can be uploaded",
			filename, formatUploadBytes(size), formatUploadBytes(maxSize)))
	}
	return nil
}

// beginUserUpload is BeginFileUpload into the machine's uploads folder
// (~/.reliant/uploads/<uploads_folder>/<filename>, internal/daemon/
// user_uploads.go). The daemon resolves that folder from its own home
// directory; this server sends only the two names, each checked to be one
// plain path element, and the workspace root the session is bound to (so its
// chunks, commit, abort and status scope exactly like a workspace upload's).
// A daemon without fs.user_upload_begin is refused with the reason
// machine_needs_update_for_uploads: no older command may write outside the
// workspace, so there is no whole-file fallback.
func (s *Service) beginUserUpload(ctx context.Context, userID string, scope workspaceScope, msg *pb.BeginFileUploadRequest) (*connect.Response[pb.BeginFileUploadResponse], error) {
	if !daemon.ValidUserUploadsFolder(msg.UploadsFolder) {
		return nil, svcerr.InvalidArgument("uploads_folder must be 1 to 128 letters, digits, '-' or '_', starting with a letter or digit")
	}
	if err := daemon.ValidateUploadFilename(msg.Filename); err != nil {
		return nil, svcerr.InvalidArgument(err.Error())
	}
	maxSize := s.uploadMaxSize()
	if err := checkUploadRequestSize(msg.Filename, msg.Size, maxSize); err != nil {
		return nil, err
	}
	root, err := scope.uploadRoot()
	if err != nil {
		return nil, err
	}

	cmdReq := daemon.UserUploadBeginRequest{Root: root, Folder: msg.UploadsFolder, Filename: msg.Filename, Size: msg.Size, MaxSize: maxSize}
	var cmdResp daemon.UploadBeginResponse
	if err := s.sendUploadCommand(ctx, userID, scope.target, daemon.UserUploadBeginCommand, cmdReq, &cmdResp, uploadBeginTimeoutMs); err != nil {
		if daemonerr.IsOutdated(err) {
			return nil, svcerr.WithReason(svcerr.FailedPrecondition(fmt.Sprintf(
				"Update Reliant on %s to send it files chat can't attach", s.machineName(ctx, scope.target))),
				errreason.MachineNeedsUpdateForUploads)
		}
		return nil, err
	}
	if validateUploadID(cmdResp.UploadID) != nil {
		return nil, svcerr.Internal("the machine answered with a malformed upload id")
	}
	return connect.NewResponse(&pb.BeginFileUploadResponse{
		UploadId:    cmdResp.UploadID,
		ChunkSize:   uploadChunkSize,
		Path:        cmdResp.Path,
		MaxSize:     maxSize,
		Destination: pb.UploadDestination_UPLOAD_DESTINATION_USER_UPLOADS,
	}), nil
}
