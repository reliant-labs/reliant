package file_system

import (
	"encoding/base64"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/pkg/config"
)

const testArchiveID = "0123456789abcdef0123456789abcdef"

func pieceReply(t *testing.T, data []byte, total int64, version string, eof, guarded bool) []byte {
	t.Helper()
	return mustJSON(t, daemon.ReadRangeResponse{
		Data:          base64.StdEncoding.EncodeToString(data),
		TotalSize:     total,
		Version:       version,
		EOF:           eof,
		ViewerGuarded: guarded,
	})
}

func assertReason(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, code, svcerr.Code(err), "%v", err)
	got, ok := svcerr.ReasonOf(err)
	assert.True(t, ok, "a reason is attached: %v", err)
	assert.Equal(t, reason, got)
}

// A piece of a file is read on the machine through the viewer's guard, with
// the length clamped to what one command carries and the version passed on.
func TestReadFileRange_ReadsAPieceThroughTheViewerGuard(t *testing.T) {
	env := newUploadEnv(t)
	router := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
		return pieceReply(t, []byte("hello"), 12, "12-99", false, true), nil
	}}
	svc := env.service(t, router, nil)

	resp, err := svc.ReadFileRange(fsProxyContext(), connect.NewRequest(&pb.ReadFileRangeRequest{
		ProjectId: env.projectID,
		Path:      "docs/report.pdf",
		Offset:    7,
		Length:    64 << 20,
		IfVersion: proto.String("12-99"),
	}))
	require.NoError(t, err)

	var sent daemon.ReadRangeRequest
	assert.Equal(t, daemon.ReadRangeCommand, router.last(t, &sent))
	assert.Equal(t, daemon.ReadRangeRequest{
		Path:       filepath.Join(env.base, "docs", "report.pdf"),
		ViewerRoot: env.base,
		Offset:     7,
		Length:     daemon.MaxReadRangeBytes,
		IfVersion:  "12-99",
	}, sent)
	assert.Equal(t, []byte("hello"), resp.Msg.Data)
	assert.EqualValues(t, 12, resp.Msg.TotalSize)
	assert.Equal(t, "12-99", resp.Msg.Version)
	assert.Equal(t, "report.pdf", resp.Msg.Filename)
	assert.Equal(t, "application/pdf", resp.Msg.ContentType)
	assert.False(t, resp.Msg.Eof)

	// Length 0 asks for a whole piece too.
	_, err = svc.ReadFileRange(fsProxyContext(), connect.NewRequest(&pb.ReadFileRangeRequest{ProjectId: env.projectID, Path: "a.bin"}))
	require.NoError(t, err)
	router.last(t, &sent)
	assert.Equal(t, daemon.MaxReadRangeBytes, sent.Length)
}

func TestReadFileRange_RefusesBadRequestsBeforeTheMachine(t *testing.T) {
	env := newUploadEnv(t)
	cases := []struct {
		name string
		req  *pb.ReadFileRangeRequest
		want connect.Code
	}{
		{"path and archive", &pb.ReadFileRangeRequest{Path: "a", ArchiveId: proto.String(testArchiveID)}, connect.CodeInvalidArgument},
		{"neither", &pb.ReadFileRangeRequest{}, connect.CodeInvalidArgument},
		{"negative offset", &pb.ReadFileRangeRequest{Path: "a", Offset: -1}, connect.CodeInvalidArgument},
		{"archive id that is a path", &pb.ReadFileRangeRequest{ArchiveId: proto.String("../../../etc/passwd")}, connect.CodeInvalidArgument},
		{"archive id in capitals", &pb.ReadFileRangeRequest{ArchiveId: proto.String("0123456789ABCDEF0123456789ABCDEF")}, connect.CodeInvalidArgument},
		{"relative path climbing out", &pb.ReadFileRangeRequest{Path: "../outside.txt"}, connect.CodePermissionDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := &uploadRouter{}
			tc.req.ProjectId = env.projectID
			_, err := env.service(t, router, nil).ReadFileRange(fsProxyContext(), connect.NewRequest(tc.req))
			require.Error(t, err)
			assert.Equal(t, tc.want, svcerr.Code(err), "%v", err)
			assert.Empty(t, router.commands(), "nothing reaches the machine")
		})
	}
}

// A reply without the guard's mark comes from a daemon that may have served a
// credential store: it is never shown.
func TestReadFileRange_RefusesAnUnguardedReply(t *testing.T) {
	env := newUploadEnv(t)
	router := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
		return pieceReply(t, []byte("secret"), 6, "6-1", true, false), nil
	}}
	_, err := env.service(t, router, nil).ReadFileRange(fsProxyContext(), connect.NewRequest(&pb.ReadFileRangeRequest{ProjectId: env.projectID, Path: "a.bin"}))
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.ErrorContains(t, err, "update it to download files")
}

func TestReadFileRange_MapsTheMachinesAnswers(t *testing.T) {
	env := newUploadEnv(t)
	read := func(t *testing.T, answer error) error {
		router := &uploadRouter{answer: func(command string, _ []byte) ([]byte, error) { return nil, answer }}
		_, err := env.service(t, router, nil).ReadFileRange(fsProxyContext(), connect.NewRequest(&pb.ReadFileRangeRequest{ProjectId: env.projectID, Path: "a.bin"}))
		return err
	}

	t.Run("older daemon names the update", func(t *testing.T) {
		err := read(t, outdatedFor(daemon.ReadRangeCommand))
		assertReason(t, err, connect.CodeFailedPrecondition, errreason.RangedDownloadUnsupported)
		assert.ErrorContains(t, err, "Update Reliant on this machine to download files larger than 25 MB")
	})
	t.Run("file changed between pieces", func(t *testing.T) {
		err := read(t, daemonSays(daemon.ReadRangeCommand, daemon.FileChangedMarker+": /w/a.bin"))
		assertReason(t, err, connect.CodeFailedPrecondition, errreason.FileChangedDuringDownload)
	})
	t.Run("offset past the end", func(t *testing.T) {
		err := read(t, daemonSays(daemon.ReadRangeCommand, daemon.ReadRangeOffsetMarker+": offset 9"))
		assert.Equal(t, connect.CodeOutOfRange, svcerr.Code(err), "%v", err)
	})
	t.Run("credential store", func(t *testing.T) {
		err := read(t, daemonSays(daemon.ReadRangeCommand, "refused: the file viewer does not open credential stores (~/.ssh)"))
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "%v", err)
	})
}

// An archive is read by its id alone, bound to the workspace root the request
// resolves to; the version still pins the download to one archive.
func TestReadFileRange_ReadsAnArchiveByItsID(t *testing.T) {
	env := newUploadEnv(t)
	router := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
		return mustJSON(t, daemon.ReadRangeResponse{
			Data: base64.StdEncoding.EncodeToString([]byte("PK")), TotalSize: 2, Version: "2-5", EOF: true, Filename: "src.zip",
		}), nil
	}}
	svc := env.service(t, router, nil)
	resp, err := svc.ReadFileRange(fsProxyContext(), connect.NewRequest(&pb.ReadFileRangeRequest{
		ProjectId: env.projectID, ArchiveId: proto.String(testArchiveID), Offset: 0, Length: 100,
	}))
	require.NoError(t, err)
	var sent daemon.ArchiveReadRequest
	assert.Equal(t, daemon.ArchiveReadCommand, router.last(t, &sent))
	assert.Equal(t, daemon.ArchiveReadRequest{ArchiveID: testArchiveID, Root: env.base, Offset: 0, Length: 100}, sent)
	assert.Equal(t, "src.zip", resp.Msg.Filename)
	assert.Equal(t, archiveContentType, resp.Msg.ContentType)
	assert.True(t, resp.Msg.Eof)

	_, err = svc.ReadFileRange(fsProxyContext(), connect.NewRequest(&pb.ReadFileRangeRequest{
		ProjectId: env.projectID, ArchiveId: proto.String(testArchiveID), IfVersion: proto.String("3-5"),
	}))
	assertReason(t, err, connect.CodeFailedPrecondition, errreason.FileChangedDuringDownload)

	notFound := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
		return nil, daemonSays(daemon.ArchiveReadCommand, daemon.ArchiveNotFoundMarker)
	}}
	_, err = env.service(t, notFound, nil).ReadFileRange(fsProxyContext(), connect.NewRequest(&pb.ReadFileRangeRequest{
		ProjectId: env.projectID, ArchiveId: proto.String(testArchiveID),
	}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "%v", err)
}

func TestPrepareFolderArchive_StartsZippingOnTheMachine(t *testing.T) {
	env := newUploadEnv(t)
	router := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
		return mustJSON(t, daemon.ArchivePrepareResponse{ArchiveID: testArchiveID, TotalFiles: 3, TotalBytes: 300}), nil
	}}

	resp, err := env.service(t, router, nil).PrepareFolderArchive(fsProxyContext(), connect.NewRequest(&pb.PrepareFolderArchiveRequest{
		ProjectId: env.projectID, Path: "docs/specs",
	}))
	require.NoError(t, err)
	var sent daemon.ArchivePrepareRequest
	assert.Equal(t, daemon.ArchivePrepareCommand, router.last(t, &sent))
	assert.Equal(t, daemon.ArchivePrepareRequest{
		Root: env.base, Path: filepath.Join(env.base, "docs", "specs"), MaxBytes: daemon.DefaultArchiveMaxBytes,
	}, sent)
	assert.Equal(t, testArchiveID, resp.Msg.ArchiveId)
	assert.Equal(t, "specs.zip", resp.Msg.Filename)
	assert.EqualValues(t, 3, resp.Msg.TotalFiles)
	assert.EqualValues(t, 300, resp.Msg.TotalBytes)
	assert.Equal(t, daemon.DefaultArchiveMaxBytes, resp.Msg.MaxSize)

	// The workspace root is named after its folder; the cap is the config's.
	resp, err = env.service(t, router, &config.ApiServerConfig{MaxFolderArchiveSize: 1 << 20}).PrepareFolderArchive(fsProxyContext(),
		connect.NewRequest(&pb.PrepareFolderArchiveRequest{ProjectId: env.projectID, Path: ""}))
	require.NoError(t, err)
	router.last(t, &sent)
	assert.Equal(t, env.base, sent.Path)
	assert.EqualValues(t, 1<<20, sent.MaxBytes)
	assert.Equal(t, filepath.Base(env.base)+".zip", resp.Msg.Filename)
}

func TestPrepareFolderArchive_Refusals(t *testing.T) {
	env := newUploadEnv(t)
	for _, tc := range []struct{ name, path string }{
		{"climbs out", "../elsewhere"},
		{"absolute outside", "/etc"},
		{".git", ".git"},
		{"inside .git", "vendor/.git/objects"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := &uploadRouter{}
			_, err := env.service(t, router, nil).PrepareFolderArchive(fsProxyContext(), connect.NewRequest(&pb.PrepareFolderArchiveRequest{
				ProjectId: env.projectID, Path: tc.path,
			}))
			assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "%v", err)
			assert.Empty(t, router.commands())
		})
	}

	prepare := func(t *testing.T, answer error) error {
		router := &uploadRouter{answer: func(string, []byte) ([]byte, error) { return nil, answer }}
		_, err := env.service(t, router, nil).PrepareFolderArchive(fsProxyContext(), connect.NewRequest(&pb.PrepareFolderArchiveRequest{
			ProjectId: env.projectID, Path: "src",
		}))
		return err
	}
	t.Run("older daemon names the update", func(t *testing.T) {
		err := prepare(t, outdatedFor(daemon.ArchivePrepareCommand))
		assertReason(t, err, connect.CodeFailedPrecondition, errreason.RangedDownloadUnsupported)
		assert.ErrorContains(t, err, "to download folders")
	})
	t.Run("too large", func(t *testing.T) {
		err := prepare(t, daemonSays(daemon.ArchivePrepareCommand, daemon.ArchiveTooLargeMarker+": it holds more than 2147483648 bytes"))
		assertReason(t, err, connect.CodeFailedPrecondition, errreason.FolderTooLargeToDownload)
	})
	t.Run("escaping on the machine's disk", func(t *testing.T) {
		err := prepare(t, daemonSays(daemon.ArchivePrepareCommand, daemon.ArchiveRefusedPathMarker+": /w/src is outside the workspace"))
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "%v", err)
	})
	t.Run("malformed id from the machine", func(t *testing.T) {
		router := &uploadRouter{answer: func(string, []byte) ([]byte, error) { return []byte(`{"archive_id":"../x"}`), nil }}
		_, err := env.service(t, router, nil).PrepareFolderArchive(fsProxyContext(), connect.NewRequest(&pb.PrepareFolderArchiveRequest{
			ProjectId: env.projectID, Path: "src",
		}))
		assert.Equal(t, connect.CodeInternal, svcerr.Code(err), "%v", err)
	})
}

func TestGetFolderArchiveStatus_ReportsTheMachinesProgress(t *testing.T) {
	env := newUploadEnv(t)
	for _, tc := range []struct {
		state string
		want  pb.FolderArchiveState
	}{
		{daemon.ArchiveStateRunning, pb.FolderArchiveState_FOLDER_ARCHIVE_STATE_RUNNING},
		{daemon.ArchiveStateReady, pb.FolderArchiveState_FOLDER_ARCHIVE_STATE_READY},
		{daemon.ArchiveStateFailed, pb.FolderArchiveState_FOLDER_ARCHIVE_STATE_FAILED},
	} {
		router := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
			return mustJSON(t, daemon.ArchiveStatusResponse{State: tc.state, FilesDone: 1, FilesTotal: 2, BytesDone: 10, BytesTotal: 20, ArchiveSize: 15, Error: "e", Skipped: 4}), nil
		}}
		resp, err := env.service(t, router, nil).GetFolderArchiveStatus(fsProxyContext(), connect.NewRequest(&pb.GetFolderArchiveStatusRequest{
			ProjectId: env.projectID, ArchiveId: testArchiveID,
		}))
		require.NoError(t, err)
		var sent daemon.ArchiveRequest
		assert.Equal(t, daemon.ArchiveStatusCommand, router.last(t, &sent))
		assert.Equal(t, daemon.ArchiveRequest{ArchiveID: testArchiveID, Root: env.base}, sent)
		assert.Equal(t, tc.want, resp.Msg.State)
		assert.EqualValues(t, 2, resp.Msg.FilesTotal)
		assert.EqualValues(t, 15, resp.Msg.ArchiveSize)
		assert.EqualValues(t, 4, resp.Msg.Skipped)
	}

	router := &uploadRouter{answer: func(string, []byte) ([]byte, error) { return []byte(`{"state":"exploded"}`), nil }}
	_, err := env.service(t, router, nil).GetFolderArchiveStatus(fsProxyContext(), connect.NewRequest(&pb.GetFolderArchiveStatusRequest{
		ProjectId: env.projectID, ArchiveId: testArchiveID,
	}))
	assert.Equal(t, connect.CodeInternal, svcerr.Code(err))
}

func TestReleaseFolderArchive(t *testing.T) {
	env := newUploadEnv(t)
	router := &uploadRouter{}
	_, err := env.service(t, router, nil).ReleaseFolderArchive(fsProxyContext(), connect.NewRequest(&pb.ReleaseFolderArchiveRequest{
		ProjectId: env.projectID, ArchiveId: testArchiveID,
	}))
	require.NoError(t, err)
	var sent daemon.ArchiveRequest
	assert.Equal(t, daemon.ArchiveReleaseCommand, router.last(t, &sent))
	assert.Equal(t, daemon.ArchiveRequest{ArchiveID: testArchiveID, Root: env.base}, sent)

	// A machine too old to have made an archive has nothing to release.
	old := &uploadRouter{answer: func(string, []byte) ([]byte, error) { return nil, outdatedFor(daemon.ArchiveReleaseCommand) }}
	_, err = env.service(t, old, nil).ReleaseFolderArchive(fsProxyContext(), connect.NewRequest(&pb.ReleaseFolderArchiveRequest{
		ProjectId: env.projectID, ArchiveId: testArchiveID,
	}))
	require.NoError(t, err)
}
