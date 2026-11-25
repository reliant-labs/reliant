package file_system

import (
	"context"
	"encoding/base64"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// ReadFileRange returns one piece of a file, or of a prepared folder archive
// (transfer.go). A file is scoped and guarded exactly like DownloadFile; an
// archive is named only by the id its machine minted.
func (s *Service) ReadFileRange(
	ctx context.Context,
	req *connect.Request[pb.ReadFileRangeRequest],
) (*connect.Response[pb.ReadFileRangeResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}
	archiveID := req.Msg.GetArchiveId()
	hasPath := strings.TrimSpace(req.Msg.Path) != ""
	switch {
	case hasPath && archiveID != "":
		return nil, svcerr.InvalidArgument("name a path or an archive_id, not both")
	case !hasPath && archiveID == "":
		return nil, svcerr.InvalidArgument("path or archive_id is required")
	case req.Msg.Offset < 0:
		return nil, svcerr.InvalidArgument("offset must not be negative")
	}
	if archiveID != "" {
		if err := validateArchiveID(archiveID); err != nil {
			return nil, err
		}
	}

	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	length := readRangeLength(req.Msg.Length)

	var (
		piece       daemon.ReadRangeResponse
		filename    string
		contentType string
	)
	if archiveID != "" {
		root, err := scope.uploadRoot()
		if err != nil {
			return nil, err
		}
		cmdReq := daemon.ArchiveReadRequest{ArchiveID: archiveID, Root: root, Offset: req.Msg.Offset, Length: length}
		if err := s.sendTransferCommand(ctx, userID, scope.target, daemon.ArchiveReadCommand, cmdReq, &piece, readRangeTimeoutMs); err != nil {
			if daemonerr.IsOutdated(err) {
				return nil, s.rangedDownloadUnsupported(ctx, scope.target, "download folders", err)
			}
			return nil, err
		}
		// An archive's version is its own, and it never changes once ready;
		// if_version still holds it to the archive the download began with.
		if v := req.Msg.GetIfVersion(); v != "" && v != piece.Version {
			return nil, transferDaemonErrorChanged()
		}
		filename, contentType = piece.Filename, archiveContentType
	} else {
		resolvedPath, err := s.resolveForView(ctx, userID, scope, req.Msg.Path)
		if err != nil {
			return nil, err
		}
		cmdReq := daemon.ReadRangeRequest{
			Path:       resolvedPath,
			ViewerRoot: scope.basePath,
			Offset:     req.Msg.Offset,
			Length:     length,
			IfVersion:  req.Msg.GetIfVersion(),
		}
		if err := s.sendTransferCommand(ctx, userID, scope.target, daemon.ReadRangeCommand, cmdReq, &piece, readRangeTimeoutMs); err != nil {
			if daemonerr.IsOutdated(err) {
				return nil, s.rangedDownloadUnsupported(ctx, scope.target, "download files larger than 25 MB", err)
			}
			return nil, err
		}
		if err := requireViewerGuardTo(piece.ViewerGuarded, "download files"); err != nil {
			return nil, err
		}
		filename = ospath.Base(resolvedPath)
		contentType = contentTypeFor(filename)
	}

	data, err := base64.StdEncoding.DecodeString(piece.Data)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("decode file bytes"))
	}
	if len(data) > length {
		return nil, svcerr.Internal("the machine answered with more bytes than asked for")
	}
	return connect.NewResponse(&pb.ReadFileRangeResponse{
		Data:        data,
		TotalSize:   piece.TotalSize,
		Version:     piece.Version,
		Filename:    filename,
		ContentType: contentType,
		Eof:         piece.EOF,
	}), nil
}
