// Copyright (c) 2025 Reliant Labs
package file_system

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/filepreview"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// diskDaemonRouter answers fs.preview_info and fs.read_binary_file from the
// real files on this disk, the way cmd_fs.go does (the classification is
// filepreview's, the cap is max_bytes, every reply to a viewer read is marked
// guarded), and records each command and the budget it was sent with.
type diskDaemonRouter struct {
	toolexectest.Stub
	commands []string
	budgets  map[string]int32
	// sizeOverride, when set, is the size fs.preview_info reports, so the cap
	// is tested without writing 25 MB.
	sizeOverride int64
}

func (r *diskDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *diskDaemonRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	r.commands = append(r.commands, commandType)
	if r.budgets == nil {
		r.budgets = map[string]int32{}
	}
	r.budgets[commandType] = timeoutMs
	var req struct {
		Path       string `json:"path"`
		MaxBytes   int64  `json:"max_bytes"`
		ViewerRoot string `json:"viewer_root"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	fail := func(msg string) ([]byte, error) { return nil, toolexec.DaemonReportedFailure(commandType, msg) }
	st, err := os.Stat(req.Path)
	if os.IsNotExist(err) {
		return fail("file not found: " + req.Path)
	}
	if err != nil {
		return nil, err
	}
	switch commandType {
	case "fs.preview_info":
		if st.IsDir() {
			return fail("path is a directory: " + req.Path)
		}
		f, err := os.Open(req.Path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		sample := make([]byte, 8192)
		n, err := f.Read(sample)
		if err != nil && err != io.EOF {
			return nil, err
		}
		c := filepreview.Classify(req.Path, sample[:n])
		size := st.Size()
		if r.sizeOverride > 0 {
			size = r.sizeOverride
		}
		return json.Marshal(map[string]any{
			"name": st.Name(), "path": req.Path, "size": size,
			"viewer_kind": string(c.ViewerKind), "mime_type": c.MIMEType,
			"viewer_guarded": req.ViewerRoot != "",
		})
	case "fs.read_binary_file":
		if req.MaxBytes > 0 && st.Size() > req.MaxBytes {
			return fail(fmt.Sprintf("file %s is %d bytes, exceeds maximum of %d bytes", req.Path, st.Size(), req.MaxBytes))
		}
		data, err := os.ReadFile(req.Path)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"data": base64.StdEncoding.EncodeToString(data), "viewer_guarded": req.ViewerRoot != ""})
	}
	return fail("unexpected command " + commandType)
}

func downloadFile(t *testing.T, router Router, repo any, projectID, path string) (*connect.Response[reliantv1.DownloadFileResponse], error) {
	t.Helper()
	return newProxy(t, router, repo).DownloadFile(fsProxyContext(),
		connect.NewRequest(&reliantv1.DownloadFileRequest{ProjectId: projectID, Path: path}))
}

// The owner's ask: a file the viewer cannot show (a spreadsheet, an archive)
// or one it shows as text can still be taken off the machine. Every kind
// downloads, byte for byte, with the type the machine classified it as.
func TestDownloadFile_ServesAnyKindOfFile(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	xlsx := []byte("PK\x03\x04\x00\x00binary spreadsheet bytes \x00\xff\xfe")
	require.NoError(t, os.WriteFile(filepath.Join(base, "report.xlsx"), xlsx, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(base, "notes.md"), []byte("# notes\n"), 0o644))
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	require.NoError(t, os.WriteFile(filepath.Join(base, "shot.png"), png, 0o644))

	for _, tc := range []struct {
		path        string
		want        []byte
		contentType string
	}{
		{"report.xlsx", xlsx, ""}, // classified by the machine; any type is served
		{"notes.md", []byte("# notes\n"), "text/plain"},
		{filepath.Join(base, "shot.png"), png, "image/png"},
	} {
		router := &diskDaemonRouter{}
		resp, err := downloadFile(t, router, repo, projectID, tc.path)
		require.NoError(t, err, tc.path)
		assert.Equal(t, tc.want, resp.Msg.GetContent(), tc.path)
		assert.Equal(t, filepath.Base(tc.path), resp.Msg.GetFilename(), tc.path)
		assert.Equal(t, int64(len(tc.want)), resp.Msg.GetSize(), tc.path)
		assert.NotEmpty(t, resp.Msg.GetContentType(), "%s: a content type is always set", tc.path)
		if tc.contentType != "" {
			assert.Equal(t, tc.contentType, resp.Msg.GetContentType(), tc.path)
		}
		assert.Equal(t, []string{"fs.preview_info", "fs.read_binary_file"}, router.commands, tc.path)
		assert.Equal(t, wholeFileReadTimeoutMs, router.budgets["fs.read_binary_file"], tc.path)
	}
}

// Over the cap the download is refused before a byte is read, with a
// message that says why and a reason the web can branch on.
func TestDownloadFile_RefusesAFileOverTheCap(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(base, "big.zip"), []byte("PK\x03\x04"), 0o644))

	router := &diskDaemonRouter{sizeOverride: maxDownloadFileBytes + 1}
	_, err := downloadFile(t, router, repo, projectID, "big.zip")
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.ErrorContains(t, err, "file is larger than 25 MB")
	reason, ok := svcerr.ReasonOf(err)
	assert.True(t, ok)
	assert.Equal(t, errreason.FileTooLargeToDownload, reason)
	assert.Equal(t, []string{"fs.preview_info"}, router.commands, "an oversized file is never read")

	// Exactly at the cap is served.
	router = &diskDaemonRouter{sizeOverride: maxDownloadFileBytes}
	_, err = downloadFile(t, router, repo, projectID, "big.zip")
	require.NoError(t, err)
}

func TestDownloadFile_MissingFileAndDirectory(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	require.NoError(t, os.Mkdir(filepath.Join(base, "dir"), 0o755))

	_, err := downloadFile(t, &diskDaemonRouter{}, repo, projectID, "nope.bin")
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "missing file: %v", err)

	_, err = downloadFile(t, &diskDaemonRouter{}, repo, projectID, "dir")
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "directory: %v", err)
}

// A daemon from before the viewer's credential guard is refused, naming the
// update and what it is for.
func TestDownloadFile_OutdatedDaemonNamesTheUpdate(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(base, "a.bin"), []byte{1, 2, 3}, 0o644))

	_, err := downloadFile(t, &outdatedDaemonRouter{}, repo, projectID, "a.bin")
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.ErrorContains(t, err, "update it to download files")
}
