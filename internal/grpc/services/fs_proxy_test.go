// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingFSDaemonRouter records the last daemon command it received so tests
// can assert that CreateDirectory routes an fs.mkdir to the daemon. It embeds
// worktreeTestDaemonRouter to satisfy the full DaemonRouter interface.
type recordingFSDaemonRouter struct {
	worktreeTestDaemonRouter
	lastCommandType string
	lastPayload     []byte
}

func (r *recordingFSDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *recordingFSDaemonRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	r.lastCommandType = commandType
	r.lastPayload = payload
	return json.Marshal(struct{}{})
}

func TestFileSystemProxy_CreateDirectory_RoutesMkdirToDaemon(t *testing.T) {
	router := &recordingFSDaemonRouter{}
	svc := NewFileSystemProxyService(router, nil)

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	resp, err := svc.CreateDirectory(ctx, connect.NewRequest(&reliantv1.CreateDirectoryRequest{
		Path: "/home/workspace/projects/new-folder",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "/home/workspace/projects/new-folder", resp.Msg.Path)

	assert.Equal(t, "fs.mkdir", router.lastCommandType)
	var sent map[string]string
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.Equal(t, "/home/workspace/projects/new-folder", sent["path"])
}

func TestFileSystemProxy_CreateDirectory_RejectsEmptyAndRelativePaths(t *testing.T) {
	router := &recordingFSDaemonRouter{}
	svc := NewFileSystemProxyService(router, nil)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

	for _, path := range []string{"", "relative/path"} {
		_, err := svc.CreateDirectory(ctx, connect.NewRequest(&reliantv1.CreateDirectoryRequest{Path: path}))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	}
	// No command should have been forwarded to the daemon.
	assert.Empty(t, router.lastCommandType)
}

func TestFileSystemProxy_CreateDirectory_RequiresAuth(t *testing.T) {
	router := &recordingFSDaemonRouter{}
	svc := NewFileSystemProxyService(router, nil)

	_, err := svc.CreateDirectory(context.Background(), connect.NewRequest(&reliantv1.CreateDirectoryRequest{
		Path: "/home/workspace/projects/new-folder",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// fsTreeStubRouter returns a canned fs.get_tree response and records the payload
// the proxy forwarded, so tests can assert depth threading and node conversion.
type fsTreeStubRouter struct {
	worktreeTestDaemonRouter
	lastPayload []byte
	respJSON    []byte
}

func (r *fsTreeStubRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *fsTreeStubRouter) SendDaemonCommand(_ context.Context, _ string, _ string, payload []byte, _ int32) ([]byte, error) {
	r.lastPayload = payload
	return r.respJSON, nil
}

// GetFileTree must forward depth to the daemon, rebase the daemon's
// subdir-relative node paths onto the project-relative request path, and
// propagate size + has_children into the proto response.
func TestFileSystemProxy_GetFileTree_PrefixesPathsAndForwardsDepth(t *testing.T) {
	daemonTree := `{"nodes":[
		{"name":"inner.txt","path":"inner.txt","type":"file","size":11},
		{"name":"sub","path":"sub","type":"directory","has_children":true}
	]}`
	repo, projectID, base := seedFSProxyProject(t)
	router := &fsTreeStubRouter{respJSON: []byte(daemonTree)}
	svc := NewFileSystemProxyService(router, repo)
	ctx := fsProxyContext()

	resp, err := svc.GetFileTree(ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{
		ProjectId: projectID,
		Path:      "pkg",
		Depth:     1,
	}))
	require.NoError(t, err)

	var sent map[string]any
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.EqualValues(t, 1, sent["depth"], "depth must be forwarded to the daemon")
	assert.Equal(t, filepath.Join(base, "pkg"), sent["path"],
		"the daemon must receive an absolute, workspace-resolved path")

	byName := map[string]*reliantv1.FileNode{}
	for _, f := range resp.Msg.Files {
		byName[f.Name] = f
	}

	inner := byName["inner.txt"]
	require.NotNil(t, inner)
	assert.Equal(t, "pkg/inner.txt", inner.Path, "file path must be rebased onto request path")
	assert.EqualValues(t, 11, inner.GetSize())

	sub := byName["sub"]
	require.NotNil(t, sub)
	assert.Equal(t, "pkg/sub", sub.Path, "dir path must be rebased onto request path")
	assert.Equal(t, reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY, sub.Type)
	assert.True(t, sub.GetHasChildren(), "has_children hint must propagate for lazy expansion")
}

// Root requests carry paths already relative to the project root, so the proxy
// must not prefix them.
func TestFileSystemProxy_GetFileTree_RootNotPrefixed(t *testing.T) {
	daemonTree := `{"nodes":[{"name":"pkg","path":"pkg","type":"directory","has_children":true}]}`
	repo, projectID, base := seedFSProxyProject(t)

	// The client-facing contract is unchanged: "/" and "" both still mean the
	// workspace root. Only what crosses the wire changed — the daemon now
	// receives the absolute workspace path instead of the "/" sentinel.
	for _, requested := range []string{"/", ""} {
		router := &fsTreeStubRouter{respJSON: []byte(daemonTree)}
		svc := NewFileSystemProxyService(router, repo)

		resp, err := svc.GetFileTree(fsProxyContext(), connect.NewRequest(&reliantv1.GetFileTreeRequest{
			ProjectId: projectID,
			Path:      requested,
			Depth:     2,
		}))
		require.NoError(t, err)
		require.Len(t, resp.Msg.Files, 1)
		assert.Equal(t, "pkg", resp.Msg.Files[0].Path, "root-level paths must be left unchanged")

		var sent map[string]any
		require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
		assert.Equal(t, base, sent["path"],
			"a root request (%q) must reach the daemon as the absolute workspace path, never as a sentinel", requested)
	}
}

// ============================================================================
// Path confinement
//
// FileSystemProxyService is the hosted, multi-tenant implementation: it is
// mounted whenever a daemon router exists (internal/grpc/server.go), and every
// path it emits is executed on the user's daemon with no further scoping. It
// used to hand-roll its own path resolution, which joined the request onto the
// workspace base without ever checking the result stayed under it — so
// "../.." escaped, and an empty base forwarded the caller's raw path, letting
// "/" through as the literal filesystem root.
//
// These tests attempt the escapes. They are the evidence that the proxy now
// resolves through the same confined helper as the direct FileSystemService.
// ============================================================================

const fsProxyTestUser = "fs-proxy-test-user"

func fsProxyContext() context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, fsProxyTestUser)
}

// seedFSProxyProject registers a project whose path is a real directory, and
// returns the repository, the project id, and that workspace root.
func seedFSProxyProject(t *testing.T) (db.Repository, string, string) {
	t.Helper()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	base := t.TempDir()
	projectID := "fs-proxy-" + uuid.New().String()
	require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
		ID:     projectID,
		Name:   "fs-proxy-project",
		Path:   base,
		UserID: fsProxyTestUser,
	}))
	return repo, projectID, base
}

// TestFileSystemProxy_RefusesPathsThatEscapeTheWorkspace walks the actual
// escape shapes, not just the happy path.
func TestFileSystemProxy_RefusesPathsThatEscapeTheWorkspace(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)

	escapes := []struct {
		name string
		path string
	}{
		{"bare parent traversal", "../.."},
		{"traversal onto a real system file", "../../../../../../../etc/passwd"},
		{"traversal hidden mid-path", "src/../../../../../../../etc/passwd"},
		{"absolute path outside the workspace", "/etc/passwd"},
		{"the filesystem root itself, spelled out", "/"[:1] + "etc"},
		{"sibling directory sharing the base's prefix", base + "-secrets/creds.txt"},
	}

	for _, tc := range escapes {
		t.Run(tc.name, func(t *testing.T) {
			router := &fsTreeStubRouter{respJSON: []byte(`{"nodes":[]}`)}
			svc := NewFileSystemProxyService(router, repo)

			_, err := svc.GetFileTree(fsProxyContext(), connect.NewRequest(&reliantv1.GetFileTreeRequest{
				ProjectId: projectID,
				Path:      tc.path,
			}))
			require.Error(t, err, "path %q escaped the workspace", tc.path)
			assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
				"an escape is a refusal, not a missing file")
			assert.Nil(t, router.lastPayload, "a refused path must never reach the daemon")
		})
	}
}

// TestFileSystemProxy_EveryPathRPCIsConfined is the coverage check: the
// confinement has to hold for every RPC that forwards a client path, not only
// the one that was reported.
func TestFileSystemProxy_EveryPathRPCIsConfined(t *testing.T) {
	repo, projectID, _ := seedFSProxyProject(t)
	const escape = "../../../../../../../etc/passwd"
	escapePtr := escape

	calls := map[string]func(context.Context, *FileSystemProxyService) error{
		"GetFileTree": func(ctx context.Context, s *FileSystemProxyService) error {
			_, err := s.GetFileTree(ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{ProjectId: projectID, Path: escape}))
			return err
		},
		"GetFileContent": func(ctx context.Context, s *FileSystemProxyService) error {
			_, err := s.GetFileContent(ctx, connect.NewRequest(&reliantv1.GetFileContentRequest{ProjectId: projectID, Path: escape}))
			return err
		},
		"SaveFileContent": func(ctx context.Context, s *FileSystemProxyService) error {
			_, err := s.SaveFileContent(ctx, connect.NewRequest(&reliantv1.SaveFileContentRequest{ProjectId: projectID, Path: escape, Content: "pwned"}))
			return err
		},
		"GetFileMetadata": func(ctx context.Context, s *FileSystemProxyService) error {
			_, err := s.GetFileMetadata(ctx, connect.NewRequest(&reliantv1.GetFileMetadataRequest{ProjectId: projectID, Path: escape}))
			return err
		},
		"GetFilePreviewInfo": func(ctx context.Context, s *FileSystemProxyService) error {
			_, err := s.GetFilePreviewInfo(ctx, connect.NewRequest(&reliantv1.GetFilePreviewInfoRequest{ProjectId: projectID, Path: escape}))
			return err
		},
		"CreateFileOrFolder": func(ctx context.Context, s *FileSystemProxyService) error {
			_, err := s.CreateFileOrFolder(ctx, connect.NewRequest(&reliantv1.CreateFileOrFolderRequest{ProjectId: projectID, Path: escape}))
			return err
		},
		"DeleteFileOrFolder": func(ctx context.Context, s *FileSystemProxyService) error {
			_, err := s.DeleteFileOrFolder(ctx, connect.NewRequest(&reliantv1.DeleteFileOrFolderRequest{ProjectId: projectID, Path: escape}))
			return err
		},
		"SearchFiles": func(ctx context.Context, s *FileSystemProxyService) error {
			_, err := s.SearchFiles(ctx, connect.NewRequest(&reliantv1.SearchFilesRequest{ProjectId: projectID, Query: "secret", Path: &escapePtr}))
			return err
		},
		"CopyFile source": func(ctx context.Context, s *FileSystemProxyService) error {
			_, err := s.CopyFile(ctx, connect.NewRequest(&reliantv1.CopyFileRequest{ProjectId: projectID, SourcePath: escape, DestinationPath: "copy.txt"}))
			return err
		},
		"CopyFile destination": func(ctx context.Context, s *FileSystemProxyService) error {
			_, err := s.CopyFile(ctx, connect.NewRequest(&reliantv1.CopyFileRequest{ProjectId: projectID, SourcePath: "README.md", DestinationPath: escape}))
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			router := &fsTreeStubRouter{respJSON: []byte(`{}`)}
			svc := NewFileSystemProxyService(router, repo)

			err := call(fsProxyContext(), svc)
			require.Error(t, err, "%s forwarded an escaping path", name)
			assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			assert.Nil(t, router.lastPayload, "%s reached the daemon with a refused path", name)
		})
	}
}

// TestFileSystemProxy_SendsOnlyAbsolutePathsToTheDaemon pins the wire contract:
// the sentinels are normalised on the server, so the client-facing API keeps
// accepting "/" while the daemon only ever sees a resolved absolute path.
func TestFileSystemProxy_SendsOnlyAbsolutePathsToTheDaemon(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)

	cases := []struct {
		requested string
		want      string
	}{
		{"/", base},
		{"", base},
		{"src", filepath.Join(base, "src")},
		{"./src/../src/main.go", filepath.Join(base, "src/main.go")},
		// An absolute path INSIDE the workspace names the file it says,
		// instead of being concatenated onto the base a second time.
		{filepath.Join(base, "src/main.go"), filepath.Join(base, "src/main.go")},
	}

	for _, tc := range cases {
		router := &fsTreeStubRouter{respJSON: []byte(`{"nodes":[]}`)}
		svc := NewFileSystemProxyService(router, repo)

		_, err := svc.GetFileTree(fsProxyContext(), connect.NewRequest(&reliantv1.GetFileTreeRequest{
			ProjectId: projectID,
			Path:      tc.requested,
		}))
		require.NoError(t, err, "path %q", tc.requested)

		var sent map[string]any
		require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
		assert.Equal(t, tc.want, sent["path"], "wire path for request %q", tc.requested)
		assert.True(t, filepath.IsAbs(sent["path"].(string)), "the wire path must be absolute")
	}
}

// TestFileSystemProxy_RefusesRequestWithoutAWorkspace covers the decision about
// an absent base: it fails closed.
//
// The old code returned the caller's raw path whenever the base was empty,
// which turned a request with no project into an unscoped command against the
// daemon's filesystem.
func TestFileSystemProxy_RefusesRequestWithoutAWorkspace(t *testing.T) {
	t.Run("no project id", func(t *testing.T) {
		router := &fsTreeStubRouter{respJSON: []byte(`{"nodes":[]}`)}
		// A nil repository also proves the refusal happens before any DB call.
		svc := NewFileSystemProxyService(router, nil)

		_, err := svc.GetFileTree(fsProxyContext(), connect.NewRequest(&reliantv1.GetFileTreeRequest{
			Path: "/",
		}))
		require.Error(t, err, "a request naming no workspace must not reach the daemon")
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Nil(t, router.lastPayload)
	})

	t.Run("project row with an empty path", func(t *testing.T) {
		repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
		t.Cleanup(cleanup)

		projectID := "fs-proxy-empty-" + uuid.New().String()
		_, err := rawDB.Exec(
			`INSERT INTO projects (id, user_id, name, path, created_at, updated_at, last_active)
			 VALUES ($1, $2, 'no-path', '', NOW(), NOW(), NOW())`,
			projectID, fsProxyTestUser)
		require.NoError(t, err)

		router := &fsTreeStubRouter{respJSON: []byte(`{"nodes":[]}`)}
		svc := NewFileSystemProxyService(router, repo)

		_, err = svc.GetFileTree(fsProxyContext(), connect.NewRequest(&reliantv1.GetFileTreeRequest{
			ProjectId: projectID,
			Path:      "/",
		}))
		require.Error(t, err, "a project with no workspace path must not be forwarded unscoped")
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		assert.Nil(t, router.lastPayload)
	})
}

// TestFileSystemProxy_ReplaceInFilesAlwaysBoundsItsWalk guards the one RPC
// whose scope is carried entirely by base_dir: without it the daemon falls back
// to its own working directory and rewrites files across the machine.
func TestFileSystemProxy_ReplaceInFilesAlwaysBoundsItsWalk(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)

	router := &fsTreeStubRouter{respJSON: []byte(`{"files_changed":0}`)}
	svc := NewFileSystemProxyService(router, repo)

	_, err := svc.ReplaceInFiles(fsProxyContext(), connect.NewRequest(&reliantv1.ReplaceInFilesRequest{
		ProjectId:   projectID,
		SearchText:  "a",
		ReplaceText: "b",
	}))
	require.NoError(t, err)

	var sent struct {
		Opts map[string]any `json:"opts"`
	}
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	assert.Equal(t, base, sent.Opts["base_dir"], "the replace walk must always be bounded by the workspace")

	// And with no workspace it is refused rather than sent unbounded.
	unscoped := &fsTreeStubRouter{respJSON: []byte(`{"files_changed":0}`)}
	_, err = NewFileSystemProxyService(unscoped, nil).ReplaceInFiles(
		fsProxyContext(),
		connect.NewRequest(&reliantv1.ReplaceInFilesRequest{SearchText: "a", ReplaceText: "b"}))
	require.Error(t, err)
	assert.Nil(t, unscoped.lastPayload)
}
