package file_system

import (
	"context"
	"encoding/base64"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
)

// UploadFileChunk writes one piece of an upload at offset. Re-sending a piece
// is harmless, and the reply's bytes_received is where the next one starts.
// For a legacy upload the one chunk is the whole file and places it
// (committed_path); see upload.go.
func (s *Service) UploadFileChunk(
	ctx context.Context,
	req *connect.Request[pb.UploadFileChunkRequest],
) (*connect.Response[pb.UploadFileChunkResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	legacy, isLegacy, err := decodeLegacyUploadID(req.Msg.UploadId)
	if err != nil {
		return nil, err
	}
	if isLegacy {
		resp, err := s.uploadLegacyChunk(ctx, userID, scope, legacy, req.Msg.Offset, req.Msg.Data)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(resp), nil
	}
	if err := validateUploadID(req.Msg.UploadId); err != nil {
		return nil, err
	}
	if req.Msg.Offset < 0 {
		return nil, svcerr.InvalidArgument("offset must not be negative")
	}
	if len(req.Msg.Data) > uploadMaxChunkBytes {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("a chunk carries at most %s", formatUploadBytes(uploadMaxChunkBytes)))
	}
	root, err := scope.uploadRoot()
	if err != nil {
		return nil, err
	}

	cmdReq := daemon.UploadChunkRequest{
		UploadID: req.Msg.UploadId,
		Root:     root,
		Offset:   req.Msg.Offset,
		Data:     base64.StdEncoding.EncodeToString(req.Msg.Data),
	}
	var progress daemon.UploadProgress
	if err := s.sendUploadCommand(ctx, userID, scope.target, daemon.UploadChunkCommand, cmdReq, &progress, uploadChunkTimeoutMs); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.UploadFileChunkResponse{BytesReceived: progress.BytesReceived}), nil
}
