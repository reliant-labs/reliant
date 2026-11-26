// Copyright (c) 2026 Reliant Labs
package file_system

// Large downloads and folder archives: what ReadFileRange,
// PrepareFolderArchive, GetFolderArchiveStatus and ReleaseFolderArchive
// share.
//
// A file is read in pieces (fs.read_range) through the same scope and
// credential guard as DownloadFile: resolveScope places it, resolveForView
// resolves the path, and the daemon refuses a credential store and marks its
// reply viewer_guarded. A folder is zipped on its machine into a temporary
// archive outside the workspace (fs.archive_prepare); the client reads the
// archive by the id the daemon minted (fs.archive_read) and releases it. The
// id is validated here and there as the daemon's own format, and every
// archive command names the workspace root the request resolves to, so a
// client can neither read an arbitrary path through an id nor another
// workspace's archive. internal/daemon/transfer.go is the command contract.
//
// A daemon older than these commands answers with daemonerr.IsOutdated. The
// client then falls back to DownloadFile (files up to 25 MB), so the refusal
// carries errreason.RangedDownloadUnsupported and names the machine to update.

import (
	"context"
	"fmt"
	"mime"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Daemon command budgets. FileSystemRPCDeadlines (deadlines.go) derives each
// rpc's request deadline from them. A piece is at most 1 MiB, ~1.4 MB of
// base64 in the reply; a prepare walks the whole folder before it answers.
const (
	readRangeTimeoutMs      int32 = 30_000
	archivePrepareTimeoutMs int32 = 55_000
	archiveStatusTimeoutMs  int32 = 10_000
	archiveReleaseTimeoutMs int32 = 25_000
)

// archiveContentType is a folder archive's MIME type.
const archiveContentType = "application/zip"

// archiveMaxSize is the most file content a folder archive may hold:
// ApiServerConfig.max_folder_archive_size, else 2 GiB.
func (s *Service) archiveMaxSize() int64 {
	if s.deps.API == nil {
		return daemon.DefaultArchiveMaxBytes
	}
	if n := s.deps.API.GetMaxFolderArchiveSize(); n > 0 {
		return n
	}
	return daemon.DefaultArchiveMaxBytes
}

// readRangeLength is the piece a request asks for, clamped to what one
// command carries; 0 (or less) asks for that much.
func readRangeLength(n int32) int {
	if n <= 0 || n > daemon.MaxReadRangeBytes {
		return daemon.MaxReadRangeBytes
	}
	return int(n)
}

// validateArchiveID refuses an id the daemon cannot have minted: 32
// lowercase hex digits. The daemon names the archive's files after it, so
// this keeps a path out of it here as well as there.
func validateArchiveID(id string) error {
	if len(id) != 32 {
		return svcerr.InvalidArgument("malformed archive_id")
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return svcerr.InvalidArgument("malformed archive_id")
		}
	}
	return nil
}

// contentTypeFor is a file's MIME type from its extension, for the browser
// to save it under; application/octet-stream when unknown. The bytes are
// saved, never rendered, so the extension is all this needs.
func contentTypeFor(name string) string {
	base := ospath.Base(name)
	i := strings.LastIndexByte(base, '.')
	if i <= 0 {
		return octetStream
	}
	ext := base[i:]
	if t := mime.TypeByExtension(strings.ToLower(ext)); t != "" {
		return t
	}
	return octetStream
}

// rangedDownloadUnsupported is the refusal for a machine whose daemon
// predates the command: the client falls back where it can, and otherwise
// shows this, which names the machine to update.
func (s *Service) rangedDownloadUnsupported(ctx context.Context, target machinewake.Target, what string, cause error) error {
	err := svcerr.FailedPrecondition(fmt.Sprintf("Update Reliant on %s to %s", s.machineName(ctx, target), what))
	return svcerr.WithCause(svcerr.WithReason(err, errreason.RangedDownloadUnsupported), cause)
}

// transferDaemonError maps a failed transfer command by the daemon's own
// words (the markers in internal/daemon/transfer.go) to the code that says
// what happened; anything else stays sendCommand's mapping. Only text the
// daemon reported is matched, never the transport's.
func transferDaemonError(err error) error {
	text, ok := toolexec.DaemonReportedText(err)
	if !ok {
		return err
	}
	switch {
	case strings.Contains(text, daemon.FileChangedMarker):
		return svcerr.WithCause(transferDaemonErrorChanged(), err)
	case strings.Contains(text, daemon.ArchiveTooLargeMarker):
		return svcerr.WithCause(svcerr.WithReason(svcerr.WithMessage(svcerr.ErrFailedPrecondition, text), errreason.FolderTooLargeToDownload), err)
	case strings.Contains(text, daemon.ArchiveNotFoundMarker):
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrNotFound, text), err)
	case strings.Contains(text, daemon.ReadRangeOffsetMarker):
		return svcerr.WithCause(svcerr.OutOfRange(text), err)
	case strings.Contains(text, daemon.ArchiveNotReadyMarker),
		strings.Contains(text, daemon.UploadNoSpaceMarker):
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrFailedPrecondition, text), err)
	case strings.Contains(text, daemon.ArchiveRefusedPathMarker):
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrPermissionDenied, text), err)
	}
	return err
}

// transferDaemonErrorChanged is the refusal for a piece of a file that is no
// longer the version the download began with.
func transferDaemonErrorChanged() error {
	return svcerr.WithReason(svcerr.FailedPrecondition(
		"the file changed while it was being downloaded; download it again"), errreason.FileChangedDuringDownload)
}

// sendTransferCommand is sendCommand with the transfer markers mapped. A
// daemon too old for the command keeps daemonerr.IsOutdated, for the caller
// to word the refusal.
func (s *Service) sendTransferCommand(ctx context.Context, userID string, target machinewake.Target, commandType string, req, resp any, timeoutMs int32) error {
	if err := s.sendCommand(ctx, userID, target, commandType, req, resp, timeoutMs); err != nil {
		if daemonerr.IsOutdated(err) {
			return err
		}
		return transferDaemonError(err)
	}
	return nil
}
