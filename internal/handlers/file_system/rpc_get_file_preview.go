package file_system

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/filepreview"
)

// maxFilePreviewBytes caps a binary preview (see wholeFileRead.maxBytes).
// 25 MB covers screenshots, diagrams, PDFs, recordings and short clips; a
// larger file is refused up front with a message that says why.
const maxFilePreviewBytes int64 = 25 << 20

// GetFilePreview returns the raw bytes of a previewable file (image, PDF,
// audio, video), read on the machine that holds it (readWholeFile). Only the
// four previewable kinds are served; DownloadFile serves any file.
func (s *Service) GetFilePreview(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFilePreviewRequest],
) (*connect.Response[reliantv1.GetFilePreviewResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	info, content, err := s.readWholeFile(ctx, userID, wholeFileRead{
		projectID:  req.Msg.ProjectId,
		path:       req.Msg.Path,
		worktreeID: req.Msg.WorktreeId,
		chatID:     req.Msg.ChatId,
		maxBytes:   maxFilePreviewBytes,
		tooLarge:   filePreviewTooLarge,
		accept: func(info *fsProxyPreviewInfo) error {
			switch filepreview.ViewerKind(info.ViewerKind) {
			case filepreview.ViewerKindImage, filepreview.ViewerKindPDF, filepreview.ViewerKindAudio, filepreview.ViewerKindVideo:
				return nil
			default:
				return svcerr.FailedPrecondition("file type is not previewable")
			}
		},
		action: viewFilesAction,
	})
	if err != nil {
		return nil, err
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
