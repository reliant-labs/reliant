package file_system

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonerr"
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
	dest, err := scope.resolveUploadDest(req.Msg.Directory, req.Msg.Filename)
	if err != nil {
		return nil, err
	}
	maxSize := s.uploadMaxSize()
	size := req.Msg.Size
	if size < 0 {
		return nil, svcerr.InvalidArgument("size must not be negative")
	}
	if size > maxSize {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("%s is %s; files up to %s can be uploaded",
			req.Msg.Filename, formatUploadBytes(size), formatUploadBytes(maxSize)))
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
		UploadId:  cmdResp.UploadID,
		ChunkSize: uploadChunkSize,
		Path:      dest.rel,
		MaxSize:   maxSize,
	}), nil
}
