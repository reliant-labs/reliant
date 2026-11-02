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
// credential guard (daemonpolicy.DenyViewerRead, as cmd_fs.go does), and
// records what the server sent.
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
	}
	return []byte(`{}`), nil
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
	require.NoError(t, os.WriteFile(key, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600))
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
