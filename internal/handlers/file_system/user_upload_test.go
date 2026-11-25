package file_system

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/errreason"
)

const testUploadsFolder = "8d1c2f6e-5b0a-4c1e-9f3e-2a7b6c5d4e3f"

func userUploadRequest(projectID, folder, filename string, size int64) *connect.Request[pb.BeginFileUploadRequest] {
	return connect.NewRequest(&pb.BeginFileUploadRequest{
		ProjectId:     projectID,
		Filename:      filename,
		Size:          size,
		Destination:   pb.UploadDestination_UPLOAD_DESTINATION_USER_UPLOADS,
		UploadsFolder: folder,
		// Ignored for a user upload: the daemon resolves the folder itself.
		Directory: "../../../etc",
	})
}

// A user upload sends the daemon only the two names and the workspace root
// the session is bound to, never a path; the reply carries the absolute path
// the daemon chose.
func TestBeginFileUpload_UserUploadsLetsTheDaemonResolveTheFolder(t *testing.T) {
	env := newUploadEnv(t)
	const placed = "/home/me/.reliant/uploads/" + testUploadsFolder + "/report.xlsx"
	router := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
		return []byte(`{"upload_id":"0123abcd","path":"` + placed + `"}`), nil
	}}
	resp, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), userUploadRequest(env.projectID, testUploadsFolder, "report.xlsx", 4096))
	require.NoError(t, err)

	var sent daemon.UserUploadBeginRequest
	assert.Equal(t, daemon.UserUploadBeginCommand, router.last(t, &sent))
	assert.Equal(t, daemon.UserUploadBeginRequest{
		Root:     env.base,
		Folder:   testUploadsFolder,
		Filename: "report.xlsx",
		Size:     4096,
		MaxSize:  daemon.DefaultUploadMaxSize,
	}, sent)
	assert.Equal(t, "0123abcd", resp.Msg.UploadId)
	assert.Equal(t, placed, resp.Msg.Path)
	assert.Equal(t, pb.UploadDestination_UPLOAD_DESTINATION_USER_UPLOADS, resp.Msg.Destination,
		"the echo is how a client tells this server from one that predates USER_UPLOADS")
	assert.EqualValues(t, uploadChunkSize, resp.Msg.ChunkSize)
}

func TestBeginFileUpload_WorkspaceUploadEchoesItsDestination(t *testing.T) {
	env := newUploadEnv(t)
	router := &uploadRouter{answer: func(string, []byte) ([]byte, error) { return []byte(`{"upload_id":"ab"}`), nil }}
	resp, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
		ProjectId: env.projectID, Filename: "a.txt", Size: 1,
	}))
	require.NoError(t, err)
	assert.Equal(t, pb.UploadDestination_UPLOAD_DESTINATION_WORKSPACE, resp.Msg.Destination)
	assert.Equal(t, daemon.UploadBeginCommand, router.last(t, &daemon.UploadBeginRequest{}))
}

func TestBeginFileUpload_UserUploadsRefusesBadNames(t *testing.T) {
	env := newUploadEnv(t)
	cases := []struct{ name, folder, file string }{
		{"no folder", "", "a.txt"},
		{"dot-dot folder", "..", "a.txt"},
		{"traversing folder", "../x", "a.txt"},
		{"nested folder", "a/b", "a.txt"},
		{"absolute folder", "/tmp", "a.txt"},
		{"the staging folder", ".partial", "a.txt"},
		{"folder too long", strings.Repeat("a", 129), "a.txt"},
		{"no name", testUploadsFolder, ""},
		{"dot-dot name", testUploadsFolder, ".."},
		{"traversing name", testUploadsFolder, "../a.txt"},
		{"backslash name", testUploadsFolder, `..\a.txt`},
		{"NUL in name", testUploadsFolder, "a\x00.txt"},
		{"newline in name", testUploadsFolder, "a\n.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := &uploadRouter{}
			_, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), userUploadRequest(env.projectID, tc.folder, tc.file, 1))
			assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "%v", err)
			assert.Empty(t, router.commands(), "nothing reaches the machine")
		})
	}

	t.Run("an unknown destination", func(t *testing.T) {
		router := &uploadRouter{}
		_, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
			ProjectId: env.projectID, Filename: "a.txt", Size: 1, Destination: pb.UploadDestination(99),
		}))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
		assert.Empty(t, router.commands())
	})
	t.Run("over the size cap", func(t *testing.T) {
		router := &uploadRouter{}
		_, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), userUploadRequest(env.projectID, testUploadsFolder, "big.zip", daemon.DefaultUploadMaxSize+1))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
		assert.Empty(t, router.commands())
	})
}

// A daemon without fs.user_upload_begin gets no whole-file fallback (no older
// command may write outside the workspace): the user is told to update it.
func TestBeginFileUpload_UserUploadsOnAnOlderDaemon(t *testing.T) {
	env := newUploadEnv(t)
	router := &uploadRouter{answer: func(cmd string, _ []byte) ([]byte, error) { return nil, outdatedFor(cmd) }}
	_, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), userUploadRequest(env.projectID, testUploadsFolder, "a.zip", 10))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.ErrorContains(t, err, "Update Reliant on")
	reason, ok := svcerr.ReasonOf(err)
	assert.True(t, ok)
	assert.Equal(t, errreason.MachineNeedsUpdateForUploads, reason)
	assert.Equal(t, []string{daemon.UserUploadBeginCommand}, router.commands(), "nothing else is tried")
}

func TestBeginFileUpload_UserUploadsMapsTheMachinesRefusals(t *testing.T) {
	env := newUploadEnv(t)
	router := &uploadRouter{answer: func(cmd string, _ []byte) ([]byte, error) {
		return nil, daemonSays(cmd, daemon.UploadRefusedPathMarker+": the folder resolves outside the uploads folder")
	}}
	_, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), userUploadRequest(env.projectID, testUploadsFolder, "a.zip", 10))
	assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err))
}

func TestCommitFileUpload_UserUploadReturnsTheAbsolutePath(t *testing.T) {
	env := newUploadEnv(t)
	const placed = "/home/me/.reliant/uploads/chat/data (1).csv"
	router := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
		return mustJSON(t, daemon.UploadCommitResponse{Path: placed, Size: 3, Created: true, Destination: daemon.UploadDestinationUserUploads}), nil
	}}
	resp, err := env.service(t, router, nil).CommitFileUpload(fsProxyContext(), connect.NewRequest(&pb.CommitFileUploadRequest{
		ProjectId: env.projectID, UploadId: "ab",
	}))
	require.NoError(t, err)
	assert.Equal(t, placed, resp.Msg.Path, "a user upload has no workspace-relative path")
	assert.Equal(t, placed, resp.Msg.AbsolutePath)
	var sent daemon.UploadSessionRequest
	router.last(t, &sent)
	assert.Equal(t, env.base, sent.Root, "the session is scoped by its workspace root, like any upload")
}

func TestCommitFileUpload_WorkspaceUploadAlsoCarriesTheAbsolutePath(t *testing.T) {
	env := newUploadEnv(t)
	placed := env.base + "/docs/a.txt"
	router := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
		return mustJSON(t, daemon.UploadCommitResponse{Path: placed, Size: 1, Created: true}), nil
	}}
	resp, err := env.service(t, router, nil).CommitFileUpload(fsProxyContext(), connect.NewRequest(&pb.CommitFileUploadRequest{
		ProjectId: env.projectID, UploadId: "ab",
	}))
	require.NoError(t, err)
	assert.Equal(t, "docs/a.txt", resp.Msg.Path)
	assert.Equal(t, placed, resp.Msg.AbsolutePath)
}
