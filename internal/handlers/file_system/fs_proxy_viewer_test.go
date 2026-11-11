// Copyright (c) 2025 Reliant Labs
package file_system

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// viewerGuardRouter answers like a daemon that applies the viewer's
// credential guard (daemonpolicy.DenyViewerRead, as cmd_fs.go does) and marks
// its reply as guarded, and records what the server sent.
type viewerGuardRouter struct {
	toolexectest.Stub
	lastPayload []byte
}

func (r *viewerGuardRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *viewerGuardRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	r.lastPayload = payload
	var req struct {
		Path       string `json:"path"`
		ViewerRoot string `json:"viewer_root"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	if req.ViewerRoot != "" {
		if err := daemonpolicy.DenyViewerRead(req.Path, req.ViewerRoot); err != nil {
			return nil, toolexec.DaemonReportedFailure(commandType, err.Error())
		}
		return []byte(`{"viewer_guarded":true}`), nil
	}
	return []byte(`{}`), nil
}

// outdatedDaemonRouter answers like a daemon from before the guard: it
// ignores viewer_root and serves any path, with no viewer_guarded mark.
type outdatedDaemonRouter struct{ toolexectest.Stub }

func (r *outdatedDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (*outdatedDaemonRouter) SendDaemonCommand(context.Context, string, string, []byte, int32) ([]byte, error) {
	return []byte(`{"content":"secret","exists":true}`), nil
}

// A daemon that predates the guard would serve ~/.ssh/id_rsa, so the server
// shows nothing outside the workspace from it (FailedPrecondition: update the
// daemon), while the workspace's own files still open.
func TestFileSystemProxy_OutdatedDaemonViewsOnlyTheWorkspace(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	outside := filepath.Join(t.TempDir(), "id_rsa")
	require.NoError(t, os.WriteFile(filepath.Join(base, "README.md"), []byte("hi\n"), 0o644))

	content := func(p string) error {
		_, err := newProxy(t, &outdatedDaemonRouter{}, repo).GetFileContent(fsProxyContext(),
			connect.NewRequest(&reliantv1.GetFileContentRequest{ProjectId: projectID, Path: p}))
		return err
	}
	metadata := func(p string) error {
		_, err := newProxy(t, &outdatedDaemonRouter{}, repo).GetFileMetadata(fsProxyContext(),
			connect.NewRequest(&reliantv1.GetFileMetadataRequest{ProjectId: projectID, Path: p}))
		return err
	}
	for name, call := range map[string]func(string) error{"GetFileContent": content, "GetFileMetadata": metadata} {
		err := call(outside)
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "%s(%s) from an outdated daemon: %v", name, outside, err)
		assert.NoError(t, call("README.md"), "%s of a workspace file from an outdated daemon", name)
		assert.NoError(t, call(filepath.Join(base, "README.md")), "%s of an absolute workspace path from an outdated daemon", name)
	}
}

// The viewer opens absolute paths outside the workspace, but never a
// credential store, and a symlink in the project does not reach one either
// (the 2026-11-01 review, RF-3). Every viewer rpc tells the daemon it is a
// viewer read, and the daemon's refusal reaches the client as PermissionDenied.
func TestFileSystemProxy_ViewerRefusesCredentialStores(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	key := filepath.Join(home, ".ssh", "id_rsa")
	require.NoError(t, os.MkdirAll(filepath.Dir(key), 0o700))
	require.NoError(t, os.WriteFile(key, []byte("fixture: not a key\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "README.md"), []byte("hi\n"), 0o644))
	require.NoError(t, os.Symlink(key, filepath.Join(base, "innocent.txt")))

	viewers := map[string]func(context.Context, *Service, string) error{
		"GetFileContent": func(ctx context.Context, s *Service, p string) error {
			_, err := s.GetFileContent(ctx, connect.NewRequest(&reliantv1.GetFileContentRequest{ProjectId: projectID, Path: p}))
			return err
		},
		"GetFileMetadata": func(ctx context.Context, s *Service, p string) error {
			_, err := s.GetFileMetadata(ctx, connect.NewRequest(&reliantv1.GetFileMetadataRequest{ProjectId: projectID, Path: p}))
			return err
		},
		"GetFilePreviewInfo": func(ctx context.Context, s *Service, p string) error {
			_, err := s.GetFilePreviewInfo(ctx, connect.NewRequest(&reliantv1.GetFilePreviewInfoRequest{ProjectId: projectID, Path: p}))
			return err
		},
		"GetFilePreview": func(ctx context.Context, s *Service, p string) error {
			_, err := s.GetFilePreview(ctx, connect.NewRequest(&reliantv1.GetFilePreviewRequest{ProjectId: projectID, Path: p}))
			return err
		},
	}
	for name, call := range viewers {
		t.Run(name, func(t *testing.T) {
			for _, p := range []string{key, "innocent.txt"} {
				router := &viewerGuardRouter{}
				err := call(fsProxyContext(), newProxy(t, router, repo), p)
				assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "%s(%s): %v", name, p, err)

				var sent struct {
					ViewerRoot string `json:"viewer_root"`
				}
				require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
				assert.Equal(t, base, sent.ViewerRoot, "%s must mark its read as the viewer's", name)
			}

			// A project file is still served (GetFilePreview may refuse the
			// stub's empty classification; it must not be a refusal).
			err := call(fsProxyContext(), newProxy(t, &viewerGuardRouter{}, repo), "README.md")
			assert.NotEqual(t, connect.CodePermissionDenied, svcerr.Code(err), "%s refused a project file: %v", name, err)
		})
	}
}

// homeRouter is viewerGuardRouter on a machine whose home directory is home:
// it answers skills.get_home_dir, and records the path of every fs command.
type homeRouter struct {
	viewerGuardRouter
	home  string
	paths []string
}

func (r *homeRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *homeRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType == "skills.get_home_dir" {
		return json.Marshal(map[string]string{"home_dir": r.home})
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	r.paths = append(r.paths, req.Path)
	return r.viewerGuardRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

// A "~/..." path names the machine's home directory, not a directory "~" in
// the workspace: prod, 2026-11-05, "/home/workspace/projects/x/~/projects/
// growth/.../post.md" was not found on every click (issues b52cdbdb,
// 584d1a9a). The machine is asked where home is, and the credential guard
// still holds for what the expanded path names.
func TestFileSystemProxy_ViewerExpandsHomeOnTheMachine(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	// The fake daemon's guard reads the process's own home, as the real one does.
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	preview := func(router *homeRouter, p string) error {
		_, err := newProxy(t, router, repo).GetFilePreviewInfo(fsProxyContext(),
			connect.NewRequest(&reliantv1.GetFilePreviewInfoRequest{ProjectId: projectID, Path: p}))
		return err
	}

	for requested, want := range map[string]string{
		"~/projects/growth/post.md": home + "/projects/growth/post.md",
		"~":                         home,
	} {
		router := &homeRouter{home: home}
		require.NoError(t, preview(router, requested), requested)
		assert.Equal(t, []string{want}, router.paths, requested)
	}

	router := &homeRouter{home: home}
	err = preview(router, "~/.ssh/id_rsa")
	assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "a credential store through ~: %v", err)

	// Anything else is resolved as before, without asking the machine.
	router = &homeRouter{home: home}
	require.NoError(t, preview(router, "notes/~draft.md"))
	assert.Equal(t, []string{filepath.Join(base, "notes/~draft.md")}, router.paths)
}

func TestHomeRelative(t *testing.T) {
	for p, want := range map[string]struct {
		rest string
		ok   bool
	}{
		"~":          {"", true},
		"~/a/b.md":   {"a/b.md", true},
		`~\a\b.md`:   {`a\b.md`, true},
		"~user/a.md": {"", false},
		"a/~/b.md":   {"", false},
		"/home/x/~":  {"", false},
		"":           {"", false},
	} {
		rest, ok := homeRelative(p)
		assert.Equal(t, want.rest, rest, p)
		assert.Equal(t, want.ok, ok, p)
	}
}
