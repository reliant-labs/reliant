package file_system

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/filepreview"
)

// maxFilePreviewBytes caps a binary preview. The whole file crosses the
// daemon's NATS reply as base64 (a third larger, so well under the 64 MB
// chunked-reply cap) and is held in memory here while the response is
// written, so the cap is what keeps one preview from being a memory problem.
// 25 MB covers screenshots, diagrams, PDFs, recordings and short clips; a
// larger file is refused up front with a message that says why.
const maxFilePreviewBytes int64 = 25 << 20

// filePreviewTimeoutMs bounds the binary read: a full-size preview is a few
// chunked NATS round trips, well inside this.
const filePreviewTimeoutMs int32 = 60_000

// GetFilePreview returns the raw bytes of a previewable file (image, PDF,
// audio, video), read on the machine that holds it.
//
// It routes exactly as GetFilePreviewInfo does (resolveScope), so the preview
// comes from the same disk the tree listed. The classification is the
// daemon's fs.preview_info — internal/filepreview applied to the real file —
// and only the four previewable kinds are served. The bytes come from
// fs.read_binary_file with the size cap
// passed down, so the daemon refuses an oversized file before reading it.
func (s *Service) GetFilePreview(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFilePreviewRequest],
) (*connect.Response[reliantv1.GetFilePreviewResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Msg.Path) == "" {
		return nil, svcerr.InvalidArgument("path is required")
	}

	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := s.resolveForView(ctx, userID, scope, req.Msg.Path)
	if err != nil {
		return nil, err
	}

	info, err := s.previewInfo(ctx, userID, scope, resolvedPath, req.Msg.Path)
	if err != nil {
		if strings.Contains(err.Error(), "file not found") {
			return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("file not found: %s", req.Msg.Path))
		}
		return nil, err
	}
	switch filepreview.ViewerKind(info.ViewerKind) {
	case filepreview.ViewerKindImage, filepreview.ViewerKindPDF, filepreview.ViewerKindAudio, filepreview.ViewerKindVideo:
	default:
		return nil, svcerr.FailedPrecondition("file type is not previewable")
	}
	if info.Size > maxFilePreviewBytes {
		return nil, filePreviewTooLarge(info.Size)
	}

	var bin struct {
		Data          string `json:"data"`
		ViewerGuarded bool   `json:"viewer_guarded"`
	}
	cmdReq := map[string]any{"path": resolvedPath, "max_bytes": maxFilePreviewBytes, "viewer_root": scope.basePath}
	if err := s.sendCommand(ctx, userID, scope.target, "fs.read_binary_file", cmdReq, &bin, filePreviewTimeoutMs); err != nil {
		// The file grew past the cap between the stat and the read.
		if strings.Contains(err.Error(), "exceeds maximum of") {
			return nil, filePreviewTooLarge(info.Size)
		}
		return nil, err
	}
	if err := scope.requireViewerGuard(ctx, resolvedPath, bin.ViewerGuarded); err != nil {
		return nil, err
	}
	content, err := base64.StdEncoding.DecodeString(bin.Data)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("decode preview bytes"))
	}

	return connect.NewResponse(&reliantv1.GetFilePreviewResponse{
		Content:     content,
		ContentType: info.MIMEType,
		Filename:    info.Name,
		Size:        int64(len(content)),
	}), nil
}

// filePreviewTooLarge is the refusal for a file over maxFilePreviewBytes.
func filePreviewTooLarge(size int64) error {
	return svcerr.FailedPrecondition(fmt.Sprintf(
		"file is too large to preview (%d MB; the limit is %d MB)", (size+(1<<20)-1)>>20, maxFilePreviewBytes>>20))
}
