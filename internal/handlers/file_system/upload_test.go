package file_system

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
	"github.com/reliant-labs/reliant/pkg/config"
)

// uploadCall is one daemon command the server sent.
type uploadCall struct {
	command string
	payload []byte
}

// uploadRouter plays the machine: answer decides each reply, and every
// command is recorded.
type uploadRouter struct {
	toolexectest.Stub
	mu     sync.Mutex
	calls  []uploadCall
	answer func(command string, payload []byte) ([]byte, error)
}

func (r *uploadRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *uploadRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, uploadCall{command: commandType, payload: payload})
	r.mu.Unlock()
	if r.answer == nil {
		return []byte(`{}`), nil
	}
	return r.answer(commandType, payload)
}

func (r *uploadRouter) commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		out = append(out, c.command)
	}
	return out
}

// last decodes the payload of the last command sent into v, and returns the
// command's name.
func (r *uploadRouter) last(t *testing.T, v any) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.calls, "no daemon command was sent")
	c := r.calls[len(r.calls)-1]
	require.NoError(t, json.Unmarshal(c.payload, v))
	return c.command
}

// daemonSays is a daemon's own refusal of command, as the router reports it.
func daemonSays(command, text string) error {
	return toolexec.DaemonReportedFailure(command, text)
}

// outdatedFor is an older daemon's answer to a command it does not have.
func outdatedFor(command string) error {
	return daemonSays(command, `this machine is running an older version of Reliant (v1.0.0) that doesn't support "`+command+`"`)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// uploadEnv is one project whose workspace is base, served by svc over router.
type uploadEnv struct {
	repo      *db.Repo
	projectID string
	base      string
}

func newUploadEnv(t *testing.T) uploadEnv {
	t.Helper()
	repo, projectID, base := seedFSProxyProject(t)
	return uploadEnv{repo: repo, projectID: projectID, base: base}
}

func (e uploadEnv) service(t *testing.T, router *uploadRouter, api *config.ApiServerConfig) *Service {
	t.Helper()
	return NewTestFileSystem(t, WithFileSystemDeps(Deps{Router: router, Repo: e.repo, Placement: e.repo, API: api}))
}

func TestBeginFileUpload_OpensASessionOnTheMachine(t *testing.T) {
	env := newUploadEnv(t)
	router := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
		return []byte(`{"upload_id":"0123abcd","path":"ignored"}`), nil
	}}
	svc := env.service(t, router, nil)

	resp, err := svc.BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
		ProjectId:  env.projectID,
		Directory:  "docs/specs",
		Filename:   "report v2.xlsx",
		Size:       12345,
		OnConflict: pb.UploadConflictPolicy_UPLOAD_CONFLICT_POLICY_RENAME,
	}))
	require.NoError(t, err)

	var sent daemon.UploadBeginRequest
	assert.Equal(t, daemon.UploadBeginCommand, router.last(t, &sent))
	assert.Equal(t, daemon.UploadBeginRequest{
		Root:       env.base,
		Path:       filepath.Join(env.base, "docs", "specs", "report v2.xlsx"),
		Size:       12345,
		MaxSize:    daemon.DefaultUploadMaxSize,
		OnConflict: daemon.UploadConflictRename,
	}, sent)

	assert.Equal(t, "0123abcd", resp.Msg.UploadId)
	assert.Equal(t, "docs/specs/report v2.xlsx", resp.Msg.Path, "the client is shown a workspace-relative path")
	assert.EqualValues(t, 1<<20, resp.Msg.ChunkSize)
	assert.Equal(t, daemon.DefaultUploadMaxSize, resp.Msg.MaxSize)
}

func TestBeginFileUpload_RootAndUnspecifiedPolicy(t *testing.T) {
	env := newUploadEnv(t)
	for _, dir := range []string{"", "/"} {
		router := &uploadRouter{answer: func(string, []byte) ([]byte, error) { return []byte(`{"upload_id":"ab"}`), nil }}
		resp, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
			ProjectId: env.projectID, Directory: dir, Filename: "a.txt", Size: 0,
		}))
		require.NoError(t, err)
		assert.Equal(t, "a.txt", resp.Msg.Path)
		var sent daemon.UploadBeginRequest
		router.last(t, &sent)
		assert.Equal(t, filepath.Join(env.base, "a.txt"), sent.Path)
		assert.Equal(t, daemon.UploadConflictFail, sent.OnConflict, "UNSPECIFIED is FAIL")
	}
}

func TestBeginFileUpload_RefusesBadNamesAndPlaces(t *testing.T) {
	env := newUploadEnv(t)
	cases := []struct {
		name, dir, file string
		want            connect.Code
	}{
		{"empty name", "", "", connect.CodeInvalidArgument},
		{"slash in name", "", "a/b.txt", connect.CodeInvalidArgument},
		{"backslash in name", "", `a\b.txt`, connect.CodeInvalidArgument},
		{"dot-dot name", "", "..", connect.CodeInvalidArgument},
		{"dot name", "", ".", connect.CodeInvalidArgument},
		{"NUL in name", "", "a\x00.txt", connect.CodeInvalidArgument},
		{"name too long", "", strings.Repeat("x", 256), connect.CodeInvalidArgument},
		{"directory climbs out", "../elsewhere", "a.txt", connect.CodePermissionDenied},
		{"directory climbs out mid-path", "docs/../../..", "a.txt", connect.CodePermissionDenied},
		{"absolute directory outside", "/etc", "a.txt", connect.CodePermissionDenied},
		{"into .git", ".git/hooks", "pre-commit", connect.CodePermissionDenied},
		{"into a nested .git", "vendor/lib/.git", "config", connect.CodePermissionDenied},
		{"into .GIT on a case-insensitive disk", ".GIT", "config", connect.CodePermissionDenied},
		{"a file named .git", "", ".git", connect.CodePermissionDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := &uploadRouter{}
			_, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
				ProjectId: env.projectID, Directory: tc.dir, Filename: tc.file, Size: 1,
			}))
			require.Error(t, err)
			assert.Equal(t, tc.want, svcerr.Code(err), "%v", err)
			assert.Empty(t, router.commands(), "nothing reaches the machine")
		})
	}
}

func TestBeginFileUpload_SizeLimit(t *testing.T) {
	env := newUploadEnv(t)

	t.Run("over the default 2 GiB", func(t *testing.T) {
		router := &uploadRouter{}
		_, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
			ProjectId: env.projectID, Filename: "big.iso", Size: daemon.DefaultUploadMaxSize + 1,
		}))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
		assert.Contains(t, err.Error(), "2 GB", "the refusal names the limit")
		assert.Empty(t, router.commands())
	})
	t.Run("negative", func(t *testing.T) {
		_, err := env.service(t, &uploadRouter{}, nil).BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
			ProjectId: env.projectID, Filename: "a", Size: -1,
		}))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	})
	t.Run("the configured cap", func(t *testing.T) {
		api := &config.ApiServerConfig{MaxWorkspaceUploadSize: 10 << 20}
		router := &uploadRouter{answer: func(string, []byte) ([]byte, error) { return []byte(`{"upload_id":"ab"}`), nil }}
		svc := env.service(t, router, api)
		_, err := svc.BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
			ProjectId: env.projectID, Filename: "a", Size: 10<<20 + 1,
		}))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
		assert.Contains(t, err.Error(), "10 MB")

		resp, err := svc.BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
			ProjectId: env.projectID, Filename: "a", Size: 10 << 20,
		}))
		require.NoError(t, err)
		assert.EqualValues(t, 10<<20, resp.Msg.MaxSize)
		var sent daemon.UploadBeginRequest
		router.last(t, &sent)
		assert.EqualValues(t, 10<<20, sent.MaxSize, "the daemon enforces the same cap")
	})
}

func TestBeginFileUpload_MapsTheMachinesRefusals(t *testing.T) {
	env := newUploadEnv(t)
	cases := []struct {
		text string
		want connect.Code
	}{
		{daemon.UploadExistsMarker + ": /w/a.txt", connect.CodeAlreadyExists},
		{daemon.UploadRefusedPathMarker + ": the destination is a directory", connect.CodePermissionDenied},
		{daemon.UploadTooLargeMarker + ": 3 GB over 2 GB", connect.CodeFailedPrecondition},
		{daemon.UploadNoSpaceMarker + ": 1 GB free", connect.CodeFailedPrecondition},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			router := &uploadRouter{answer: func(cmd string, _ []byte) ([]byte, error) { return nil, daemonSays(cmd, tc.text) }}
			_, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
				ProjectId: env.projectID, Filename: "a.txt", Size: 1,
			}))
			assert.Equal(t, tc.want, svcerr.Code(err), "%v", err)
			assert.Contains(t, err.Error(), tc.text, "the machine's own words reach the user")
		})
	}
}

func TestUploadFileChunk_ForwardsThePiece(t *testing.T) {
	env := newUploadEnv(t)
	router := &uploadRouter{answer: func(string, []byte) ([]byte, error) { return []byte(`{"bytes_received":1029}`), nil }}
	data := []byte{0x00, 0xff, 0xfe, 'h', 'i'}

	resp, err := env.service(t, router, nil).UploadFileChunk(fsProxyContext(), connect.NewRequest(&pb.UploadFileChunkRequest{
		ProjectId: env.projectID, UploadId: "0123abcd", Offset: 1024, Data: data,
	}))
	require.NoError(t, err)
	assert.EqualValues(t, 1029, resp.Msg.BytesReceived)
	assert.Nil(t, resp.Msg.CommittedPath, "only a legacy chunk places the file")

	var sent daemon.UploadChunkRequest
	assert.Equal(t, daemon.UploadChunkCommand, router.last(t, &sent))
	assert.Equal(t, "0123abcd", sent.UploadID)
	assert.Equal(t, env.base, sent.Root)
	assert.EqualValues(t, 1024, sent.Offset)
	decoded, err := base64.StdEncoding.DecodeString(sent.Data)
	require.NoError(t, err)
	assert.Equal(t, data, decoded, "bytes cross as base64, unaltered")
}

func TestUploadFileChunk_Refusals(t *testing.T) {
	env := newUploadEnv(t)

	t.Run("machine refusals", func(t *testing.T) {
		cases := []struct {
			text string
			want connect.Code
		}{
			{daemon.UploadOffsetMarker + ": 1024 bytes received", connect.CodeFailedPrecondition},
			{daemon.UploadNotFoundMarker, connect.CodeNotFound},
			{daemon.UploadTooLargeMarker + ": past the declared size", connect.CodeFailedPrecondition},
			{daemon.UploadNoSpaceMarker, connect.CodeFailedPrecondition},
		}
		for _, tc := range cases {
			router := &uploadRouter{answer: func(cmd string, _ []byte) ([]byte, error) { return nil, daemonSays(cmd, tc.text) }}
			_, err := env.service(t, router, nil).UploadFileChunk(fsProxyContext(), connect.NewRequest(&pb.UploadFileChunkRequest{
				ProjectId: env.projectID, UploadId: "ab", Offset: 2048, Data: []byte("x"),
			}))
			assert.Equal(t, tc.want, svcerr.Code(err), "%s: %v", tc.text, err)
			assert.Contains(t, err.Error(), tc.text)
		}
	})

	for _, tc := range []struct {
		name string
		req  *pb.UploadFileChunkRequest
	}{
		{"oversize chunk", &pb.UploadFileChunkRequest{UploadId: "ab", Data: make([]byte, 4<<20+1)}},
		{"negative offset", &pb.UploadFileChunkRequest{UploadId: "ab", Offset: -1}},
		{"no upload id", &pb.UploadFileChunkRequest{}},
		{"an id that is a path", &pb.UploadFileChunkRequest{UploadId: "../../etc/passwd"}},
		{"a malformed legacy id", &pb.UploadFileChunkRequest{UploadId: legacyUploadIDPrefix + "!!!"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := &uploadRouter{}
			tc.req.ProjectId = env.projectID
			_, err := env.service(t, router, nil).UploadFileChunk(fsProxyContext(), connect.NewRequest(tc.req))
			assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "%v", err)
			assert.Empty(t, router.commands())
		})
	}

	t.Run("a 4 MiB chunk is accepted", func(t *testing.T) {
		router := &uploadRouter{answer: func(string, []byte) ([]byte, error) { return []byte(`{"bytes_received":4194304}`), nil }}
		_, err := env.service(t, router, nil).UploadFileChunk(fsProxyContext(), connect.NewRequest(&pb.UploadFileChunkRequest{
			ProjectId: env.projectID, UploadId: "ab", Data: make([]byte, 4<<20),
		}))
		require.NoError(t, err)
	})
}

func TestCommitFileUpload(t *testing.T) {
	env := newUploadEnv(t)

	t.Run("returns the workspace-relative path the file took", func(t *testing.T) {
		router := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
			return mustJSON(t, daemon.UploadCommitResponse{Path: filepath.Join(env.base, "docs", "a (1).txt"), Size: 7, Created: true}), nil
		}}
		resp, err := env.service(t, router, nil).CommitFileUpload(fsProxyContext(), connect.NewRequest(&pb.CommitFileUploadRequest{
			ProjectId: env.projectID, UploadId: "ab",
		}))
		require.NoError(t, err)
		assert.Equal(t, "docs/a (1).txt", resp.Msg.Path)
		assert.EqualValues(t, 7, resp.Msg.Size)
		assert.True(t, resp.Msg.Created)

		var sent daemon.UploadSessionRequest
		assert.Equal(t, daemon.UploadCommitCommand, router.last(t, &sent))
		assert.Equal(t, daemon.UploadSessionRequest{UploadID: "ab", Root: env.base}, sent, "no override unless the client sets one")
	})

	t.Run("an existing file is ALREADY_EXISTS", func(t *testing.T) {
		router := &uploadRouter{answer: func(cmd string, _ []byte) ([]byte, error) {
			return nil, daemonSays(cmd, daemon.UploadExistsMarker+": "+env.base+"/a.txt")
		}}
		_, err := env.service(t, router, nil).CommitFileUpload(fsProxyContext(), connect.NewRequest(&pb.CommitFileUploadRequest{
			ProjectId: env.projectID, UploadId: "ab",
		}))
		assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))
	})

	t.Run("the override reaches the machine", func(t *testing.T) {
		router := &uploadRouter{answer: func(string, []byte) ([]byte, error) {
			return mustJSON(t, daemon.UploadCommitResponse{Path: filepath.Join(env.base, "a.txt"), Size: 1}), nil
		}}
		overwrite := pb.UploadConflictPolicy_UPLOAD_CONFLICT_POLICY_OVERWRITE
		resp, err := env.service(t, router, nil).CommitFileUpload(fsProxyContext(), connect.NewRequest(&pb.CommitFileUploadRequest{
			ProjectId: env.projectID, UploadId: "ab", OnConflict: &overwrite,
		}))
		require.NoError(t, err)
		assert.False(t, resp.Msg.Created)
		var sent daemon.UploadSessionRequest
		router.last(t, &sent)
		assert.Equal(t, daemon.UploadConflictOverwrite, sent.OnConflict)
	})

	t.Run("incomplete", func(t *testing.T) {
		router := &uploadRouter{answer: func(cmd string, _ []byte) ([]byte, error) {
			return nil, daemonSays(cmd, daemon.UploadIncompleteMarker+": 5 of 7 bytes")
		}}
		_, err := env.service(t, router, nil).CommitFileUpload(fsProxyContext(), connect.NewRequest(&pb.CommitFileUploadRequest{
			ProjectId: env.projectID, UploadId: "ab",
		}))
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	})
}

func TestAbortAndStatus_PassThrough(t *testing.T) {
	env := newUploadEnv(t)

	router := &uploadRouter{answer: func(cmd string, _ []byte) ([]byte, error) {
		if cmd == daemon.UploadStatusCommand {
			return mustJSON(t, daemon.UploadStatusResponse{Exists: true, BytesReceived: 3 << 20, Size: 5 << 20}), nil
		}
		return []byte(`{}`), nil
	}}
	svc := env.service(t, router, nil)

	status, err := svc.GetFileUploadStatus(fsProxyContext(), connect.NewRequest(&pb.GetFileUploadStatusRequest{ProjectId: env.projectID, UploadId: "ab"}))
	require.NoError(t, err)
	assert.True(t, status.Msg.Exists)
	assert.EqualValues(t, 3<<20, status.Msg.BytesReceived)
	assert.EqualValues(t, 5<<20, status.Msg.Size)
	var sent daemon.UploadSessionRequest
	assert.Equal(t, daemon.UploadStatusCommand, router.last(t, &sent))
	assert.Equal(t, daemon.UploadSessionRequest{UploadID: "ab", Root: env.base}, sent)

	_, err = svc.AbortFileUpload(fsProxyContext(), connect.NewRequest(&pb.AbortFileUploadRequest{ProjectId: env.projectID, UploadId: "ab"}))
	require.NoError(t, err)
	sent = daemon.UploadSessionRequest{}
	assert.Equal(t, daemon.UploadAbortCommand, router.last(t, &sent))
	assert.Equal(t, daemon.UploadSessionRequest{UploadID: "ab", Root: env.base}, sent)
}

// ---------------------------------------------------------------------------
// A daemon without the staged-upload commands.
// ---------------------------------------------------------------------------

// legacyMachine answers like a daemon from before fs.upload_*: stat reports
// the names in existing, create_binary_file refuses them (and those in
// racing, which appear between a stat and a create), write_binary_file
// writes anything.
func legacyMachine(t *testing.T, existing, racing map[string]bool) *uploadRouter {
	return &uploadRouter{answer: func(cmd string, payload []byte) ([]byte, error) {
		var req struct {
			Path string `json:"path"`
		}
		require.NoError(t, json.Unmarshal(payload, &req))
		switch cmd {
		case daemon.UploadBeginCommand, daemon.UploadChunkCommand, daemon.UploadCommitCommand:
			return nil, outdatedFor(cmd)
		case "fs.stat":
			return mustJSON(t, map[string]bool{"exists": existing[filepath.Base(req.Path)]}), nil
		case daemon.CreateBinaryFileCommand:
			if existing[filepath.Base(req.Path)] || racing[filepath.Base(req.Path)] {
				return nil, daemonSays(cmd, daemon.UploadExistsMarker+": "+req.Path)
			}
			return []byte(`{"created":true}`), nil
		case "fs.write_binary_file":
			return []byte(`{"created":false}`), nil
		}
		t.Fatalf("unexpected command %s", cmd)
		return nil, nil
	}}
}

func TestBeginFileUpload_LegacyDaemon(t *testing.T) {
	env := newUploadEnv(t)

	t.Run("a small file gets a legacy session", func(t *testing.T) {
		router := legacyMachine(t, nil, nil)
		resp, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
			ProjectId: env.projectID, Directory: "docs", Filename: "a.txt", Size: 5 << 20,
		}))
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(resp.Msg.UploadId, legacyUploadIDPrefix), resp.Msg.UploadId)
		assert.EqualValues(t, 5<<20, resp.Msg.ChunkSize, "the whole file in one chunk")
		assert.Equal(t, "docs/a.txt", resp.Msg.Path)
		assert.Equal(t, []string{daemon.UploadBeginCommand, "fs.stat"}, router.commands())
	})

	t.Run("an empty file is still one chunk", func(t *testing.T) {
		resp, err := env.service(t, legacyMachine(t, nil, nil), nil).BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
			ProjectId: env.projectID, Filename: "empty", Size: 0,
		}))
		require.NoError(t, err)
		assert.EqualValues(t, 1, resp.Msg.ChunkSize)
	})

	t.Run("an existing file is ALREADY_EXISTS under fail", func(t *testing.T) {
		_, err := env.service(t, legacyMachine(t, map[string]bool{"a.txt": true}, nil), nil).BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
			ProjectId: env.projectID, Filename: "a.txt", Size: 1,
		}))
		assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))
	})

	t.Run("a large file asks for an update", func(t *testing.T) {
		router := legacyMachine(t, nil, nil)
		_, err := env.service(t, router, nil).BeginFileUpload(fsProxyContext(), connect.NewRequest(&pb.BeginFileUploadRequest{
			ProjectId: env.projectID, Filename: "big.bin", Size: 20<<20 + 1,
		}))
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		assert.Contains(t, err.Error(), "Update Reliant on")
		assert.Contains(t, err.Error(), "20 MB")
		assert.Equal(t, []string{daemon.UploadBeginCommand}, router.commands())
	})
}

// legacyID is the id BeginFileUpload mints for a legacy daemon.
func legacyID(dir, name string, size int64, policy string) string {
	return encodeLegacyUploadID(legacyUpload{Dir: dir, Name: name, Size: size, Policy: policy})
}

func TestUploadFileChunk_LegacyWritesTheWholeFile(t *testing.T) {
	env := newUploadEnv(t)
	data := []byte("hello\x00world")
	size := int64(len(data))

	t.Run("fail creates the file only where nothing is", func(t *testing.T) {
		router := legacyMachine(t, nil, nil)
		resp, err := env.service(t, router, nil).UploadFileChunk(fsProxyContext(), connect.NewRequest(&pb.UploadFileChunkRequest{
			ProjectId: env.projectID, UploadId: legacyID("docs", "a.txt", size, daemon.UploadConflictFail), Data: data,
		}))
		require.NoError(t, err)
		assert.EqualValues(t, size, resp.Msg.BytesReceived)
		require.NotNil(t, resp.Msg.CommittedPath)
		assert.Equal(t, "docs/a.txt", *resp.Msg.CommittedPath)

		var sent struct{ Path, Data string }
		assert.Equal(t, daemon.CreateBinaryFileCommand, router.last(t, &sent))
		assert.Equal(t, filepath.Join(env.base, "docs", "a.txt"), sent.Path)
		decoded, err := base64.StdEncoding.DecodeString(sent.Data)
		require.NoError(t, err)
		assert.Equal(t, data, decoded)
	})

	t.Run("fail refuses an existing file", func(t *testing.T) {
		_, err := env.service(t, legacyMachine(t, map[string]bool{"a.txt": true}, nil), nil).UploadFileChunk(fsProxyContext(), connect.NewRequest(&pb.UploadFileChunkRequest{
			ProjectId: env.projectID, UploadId: legacyID("", "a.txt", size, daemon.UploadConflictFail), Data: data,
		}))
		assert.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err))
	})

	t.Run("overwrite replaces it", func(t *testing.T) {
		router := legacyMachine(t, map[string]bool{"a.txt": true}, nil)
		resp, err := env.service(t, router, nil).UploadFileChunk(fsProxyContext(), connect.NewRequest(&pb.UploadFileChunkRequest{
			ProjectId: env.projectID, UploadId: legacyID("", "a.txt", size, daemon.UploadConflictOverwrite), Data: data,
		}))
		require.NoError(t, err)
		assert.Equal(t, "a.txt", resp.Msg.GetCommittedPath())
		var sent struct{ Path string }
		assert.Equal(t, "fs.write_binary_file", router.last(t, &sent))
		assert.Equal(t, filepath.Join(env.base, "a.txt"), sent.Path)
	})

	t.Run("rename takes the first free name", func(t *testing.T) {
		// a.txt is taken, a (1).txt is taken between the stat and the
		// create, and a (2).txt is free.
		router := legacyMachine(t, map[string]bool{"a.txt": true}, map[string]bool{"a (1).txt": true})
		resp, err := env.service(t, router, nil).UploadFileChunk(fsProxyContext(), connect.NewRequest(&pb.UploadFileChunkRequest{
			ProjectId: env.projectID, UploadId: legacyID("docs", "a.txt", size, daemon.UploadConflictRename), Data: data,
		}))
		require.NoError(t, err)
		assert.Equal(t, "docs/a (2).txt", resp.Msg.GetCommittedPath())
		assert.Equal(t, []string{
			"fs.stat",
			"fs.stat", daemon.CreateBinaryFileCommand,
			"fs.stat", daemon.CreateBinaryFileCommand,
		}, router.commands(), "a taken name costs a stat, not a copy of the file")
	})

	t.Run("the chunk must be the whole file from offset 0", func(t *testing.T) {
		svc := env.service(t, legacyMachine(t, nil, nil), nil)
		_, err := svc.UploadFileChunk(fsProxyContext(), connect.NewRequest(&pb.UploadFileChunkRequest{
			ProjectId: env.projectID, UploadId: legacyID("", "a.txt", size, daemon.UploadConflictFail), Offset: 1, Data: data[1:],
		}))
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		_, err = svc.UploadFileChunk(fsProxyContext(), connect.NewRequest(&pb.UploadFileChunkRequest{
			ProjectId: env.projectID, UploadId: legacyID("", "a.txt", size, daemon.UploadConflictFail), Data: data[1:],
		}))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	})

	t.Run("a tampered id cannot leave the workspace", func(t *testing.T) {
		for _, dir := range []string{"../..", "/etc", ".git"} {
			router := legacyMachine(t, nil, nil)
			_, err := env.service(t, router, nil).UploadFileChunk(fsProxyContext(), connect.NewRequest(&pb.UploadFileChunkRequest{
				ProjectId: env.projectID, UploadId: legacyID(dir, "passwd", size, daemon.UploadConflictOverwrite), Data: data,
			}))
			assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "%s: %v", dir, err)
			assert.Empty(t, router.commands(), dir)
		}
		router := legacyMachine(t, nil, nil)
		_, err := env.service(t, router, nil).UploadFileChunk(fsProxyContext(), connect.NewRequest(&pb.UploadFileChunkRequest{
			ProjectId: env.projectID, UploadId: legacyID("", "../x", size, daemon.UploadConflictOverwrite), Data: data,
		}))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
		assert.Empty(t, router.commands())
	})
}

func TestLegacyUpload_CommitAbortStatus(t *testing.T) {
	env := newUploadEnv(t)
	id := legacyID("", "a.txt", 1, daemon.UploadConflictFail)
	router := &uploadRouter{}
	svc := env.service(t, router, nil)

	_, err := svc.CommitFileUpload(fsProxyContext(), connect.NewRequest(&pb.CommitFileUploadRequest{ProjectId: env.projectID, UploadId: id}))
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))

	_, err = svc.AbortFileUpload(fsProxyContext(), connect.NewRequest(&pb.AbortFileUploadRequest{ProjectId: env.projectID, UploadId: id}))
	require.NoError(t, err)

	status, err := svc.GetFileUploadStatus(fsProxyContext(), connect.NewRequest(&pb.GetFileUploadStatusRequest{ProjectId: env.projectID, UploadId: id}))
	require.NoError(t, err)
	assert.False(t, status.Msg.Exists)

	assert.Empty(t, router.commands(), "a legacy upload has no session to ask about")
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

func TestWorkspaceRelative(t *testing.T) {
	cases := []struct {
		root, abs, want string
		ok              bool
	}{
		{"/w/app", "/w/app/docs/a.txt", "docs/a.txt", true},
		{"/w/app", "/w/app", "", true},
		{"/w/app", "/w/app-secrets/a.txt", "", false},
		{"/w/app", "/w/a.txt", "", false},
		{"/", "/a/b", "a/b", true},
		{"/w/app", `/w/app/a\b.txt`, `a\b.txt`, true}, // a POSIX name may hold a backslash
		{`C:\src\proj`, `C:\src\proj\web\a.txt`, "web/a.txt", true},
		{"C:/src/proj", "C:/src/proj/web/a.txt", "web/a.txt", true},
		{`C:\src\proj`, `C:\src\proj2\a.txt`, "", false},
		{`C:\`, `C:\a.txt`, "a.txt", true},
	}
	for _, tc := range cases {
		got, ok := workspaceRelative(tc.root, tc.abs)
		assert.Equal(t, tc.ok, ok, "%s in %s", tc.abs, tc.root)
		assert.Equal(t, tc.want, got, "%s in %s", tc.abs, tc.root)
	}
}

func TestUploadRenameCandidate(t *testing.T) {
	assert.Equal(t, "report.xlsx", uploadRenameCandidate("report.xlsx", 0))
	assert.Equal(t, "report (1).xlsx", uploadRenameCandidate("report.xlsx", 1))
	assert.Equal(t, "archive.tar (2).gz", uploadRenameCandidate("archive.tar.gz", 2))
	assert.Equal(t, "README (1)", uploadRenameCandidate("README", 1))
	assert.Equal(t, ".env (1)", uploadRenameCandidate(".env", 1))
}

func TestLegacyUploadID_RoundTrips(t *testing.T) {
	want := legacyUpload{Dir: "docs/x", Name: "a b.txt", Size: 42, Policy: daemon.UploadConflictRename}
	got, isLegacy, err := decodeLegacyUploadID(encodeLegacyUploadID(want))
	require.NoError(t, err)
	assert.True(t, isLegacy)
	assert.Equal(t, want, got)

	_, isLegacy, err = decodeLegacyUploadID("0123abcd")
	require.NoError(t, err)
	assert.False(t, isLegacy)

	for _, bad := range []legacyUpload{
		{Name: "a", Size: legacyUploadMaxSize + 1, Policy: daemon.UploadConflictFail},
		{Name: "a", Size: -1, Policy: daemon.UploadConflictFail},
		{Name: "a", Size: 1, Policy: "clobber"},
		{Name: "", Size: 1, Policy: daemon.UploadConflictFail},
	} {
		_, _, err := decodeLegacyUploadID(encodeLegacyUploadID(bad))
		assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err), "%+v", bad)
	}
}

// The user chain reads requests of any size (ReadMaxBytes -1); this service
// caps its own at fileSystemReadMaxBytes, after the chain's options.
func TestRegister_CapsRequestSize(t *testing.T) {
	svc := NewTestFileSystem(t)
	mux := http.NewServeMux()
	svc.Register(mux, connect.WithReadMaxBytes(-1))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := reliantv1connect.NewFileSystemServiceClient(srv.Client(), srv.URL)

	_, err := client.UploadFileChunk(context.Background(), connect.NewRequest(&pb.UploadFileChunkRequest{
		UploadId: "ab", Data: make([]byte, fileSystemReadMaxBytes+1),
	}))
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err), "%v", err)

	// Under the cap the request reaches the handler, which refuses the
	// anonymous caller (as Unknown: this bare mux has no error interceptor).
	_, err = client.UploadFileChunk(context.Background(), connect.NewRequest(&pb.UploadFileChunkRequest{
		UploadId: "ab", Data: make([]byte, 4<<20),
	}))
	require.Error(t, err)
	assert.NotEqual(t, connect.CodeResourceExhausted, connect.CodeOf(err), "%v", err)
	assert.Contains(t, err.Error(), "user ID not found", "the handler ran")
}
