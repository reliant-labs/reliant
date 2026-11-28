// Copyright (c) 2025 Reliant Labs
package file_system

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/errreason"
)

// maxDownloadFileBytes caps DownloadFile: one response carries the whole
// file, held in memory at every hop (see wholeFileRead.maxBytes), the same
// bound as a preview. A larger file needs a ranged read, which is a separate
// rpc rather than a change to this one.
const maxDownloadFileBytes int64 = 25 << 20

// octetStream is the content type of a file the machine could not classify.
const octetStream = "application/octet-stream"

// DownloadFile returns the whole content of a file of any kind, read on the
// machine that holds it (readWholeFile), for the browser to save.
func (s *Service) DownloadFile(
	ctx context.Context,
	req *connect.Request[reliantv1.DownloadFileRequest],
) (*connect.Response[reliantv1.DownloadFileResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	info, content, err := s.readWholeFile(ctx, userID, wholeFileRead{
		projectID:  req.Msg.ProjectId,
		path:       req.Msg.Path,
		worktreeID: req.Msg.WorktreeId,
		chatID:     req.Msg.ChatId,
		maxBytes:   maxDownloadFileBytes,
		tooLarge:   downloadTooLarge,
		action:     "download files",
	})
	if err != nil {
		return nil, err
	}

	contentType := info.MIMEType
	if contentType == "" {
		contentType = octetStream
	}
	return connect.NewResponse(&reliantv1.DownloadFileResponse{
		Content:     content,
		ContentType: contentType,
		Filename:    info.Name,
		Size:        int64(len(content)),
	}), nil
}

// downloadTooLarge is the refusal for a file over maxDownloadFileBytes.
func downloadTooLarge(size int64) error {
	return svcerr.WithReason(svcerr.FailedPrecondition(fmt.Sprintf(
		"file is larger than %d MB (it is %d MB); large downloads are coming",
		maxDownloadFileBytes>>20, (size+(1<<20)-1)>>20)), errreason.FileTooLargeToDownload)
}
