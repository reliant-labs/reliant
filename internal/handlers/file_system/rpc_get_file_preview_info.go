package file_system

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetFilePreviewInfo returns preview metadata for a file.
func (s *Service) GetFilePreviewInfo(
	ctx context.Context,
	req *connect.Request[reliantv1.GetFilePreviewInfoRequest],
) (*connect.Response[reliantv1.GetFilePreviewInfoResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
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
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetFilePreviewInfoResponse{
		Info: &reliantv1.FilePreviewInfo{
			Name:       info.Name,
			Path:       info.Path,
			Size:       info.Size,
			Modified:   info.Modified,
			ViewerKind: viewerKindFromString(info.ViewerKind),
			MimeType:   info.MIMEType,
			IsBinary:   info.IsBinary,
			IsEditable: info.IsEditable,
		},
	}), nil
}

// viewerKindFromString maps the daemon's viewer_kind string to the proto enum.
func viewerKindFromString(kind string) reliantv1.FileViewerKind {
	switch kind {
	case "text":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_TEXT
	case "image":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_IMAGE
	case "pdf":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_PDF
	case "audio":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_AUDIO
	case "video":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_VIDEO
	case "binary":
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_BINARY
	default:
		return reliantv1.FileViewerKind_FILE_VIEWER_KIND_UNSPECIFIED
	}
}
