// Copyright (c) 2025 Reliant Labs
package file_system

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// wholeFileRead is one file read whole from the machine that holds it: the
// bytes behind GetFilePreview and DownloadFile.
type wholeFileRead struct {
	projectID  string
	path       string
	worktreeID *string
	chatID     *string
	// maxBytes is the cap. The whole file crosses the daemon's NATS reply as
	// base64 (a third larger, so a 25 MB file stays well under the 64 MB
	// chunked-reply cap) and is held in memory here while the response is
	// written, so the cap is what keeps one read from being a memory problem.
	maxBytes int64
	// tooLarge is the refusal for a file over maxBytes, given its size.
	tooLarge func(size int64) error
	// accept refuses a file the rpc does not serve, from the daemon's
	// classification of it; nil serves any file.
	accept func(*fsProxyPreviewInfo) error
	// action completes "update it to …" when the machine's daemon is too old
	// to apply the viewer's credential guard ("view files").
	action string
}

// readWholeFile routes exactly as GetFilePreviewInfo does (resolveScope), so
// the bytes come from the same disk the tree listed, and reads like the viewer
// (resolveForView: an absolute path on that machine is honoured, a credential
// store never is). The size and classification are the daemon's fs.preview_info
// (internal/filepreview applied to the real file); the bytes are
// fs.read_binary_file with the cap passed down, so the daemon refuses an
// oversized file before reading it. Both commands exist on every daemon; one
// from before the credential guard is refused, naming the update.
func (s *Service) readWholeFile(ctx context.Context, userID string, r wholeFileRead) (*fsProxyPreviewInfo, []byte, error) {
	if strings.TrimSpace(r.path) == "" {
		return nil, nil, svcerr.InvalidArgument("path is required")
	}

	scope, err := s.resolveScope(ctx, userID, r.projectID, r.worktreeID, r.chatID)
	if err != nil {
		return nil, nil, err
	}
	resolvedPath, err := s.resolveForView(ctx, userID, scope, r.path)
	if err != nil {
		return nil, nil, err
	}

	info, err := s.previewInfoTo(ctx, userID, scope, resolvedPath, r.path, r.action)
	if err != nil {
		if strings.Contains(err.Error(), "file not found") {
			return nil, nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("file not found: %s", r.path))
		}
		return nil, nil, err
	}
	if r.accept != nil {
		if err := r.accept(info); err != nil {
			return nil, nil, err
		}
	}
	if info.Size > r.maxBytes {
		return nil, nil, r.tooLarge(info.Size)
	}

	var bin struct {
		Data          string `json:"data"`
		ViewerGuarded bool   `json:"viewer_guarded"`
	}
	cmdReq := map[string]any{"path": resolvedPath, "max_bytes": r.maxBytes, "viewer_root": scope.basePath}
	if err := s.sendCommand(ctx, userID, scope.target, "fs.read_binary_file", cmdReq, &bin, wholeFileReadTimeoutMs); err != nil {
		// The file grew past the cap between the stat and the read.
		if strings.Contains(err.Error(), "exceeds maximum of") {
			return nil, nil, r.tooLarge(info.Size)
		}
		return nil, nil, err
	}
	if err := requireViewerGuardTo(bin.ViewerGuarded, r.action); err != nil {
		return nil, nil, err
	}
	content, err := base64.StdEncoding.DecodeString(bin.Data)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("decode file bytes"))
	}
	return info, content, nil
}
