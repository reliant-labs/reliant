package services

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// fsMissingDirRouter answers fs.get_tree the way a daemon does when the
// project directory is not there, verbatim from the reliant-api-server log on
// 2026-10-07 (15:04:10, the queued forge clone; 14:48:21, houndersclub).
type fsMissingDirRouter struct {
	worktreeTestDaemonRouter
	daemonID string
}

func (r *fsMissingDirRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *fsMissingDirRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	return nil, fmt.Errorf(`daemon command %q failed: read dir /home/workspace/projects/forge: open /home/workspace/projects/forge: no such file or directory`, commandType)
}

func (r *fsMissingDirRouter) ResolveDaemonID(context.Context, string) (string, error) {
	return r.daemonID, nil
}

func checkoutMissingDetail(t *testing.T, err error) *reliantv1.ProjectCheckoutMissing {
	t.Helper()
	var cerr *connect.Error
	require.True(t, errors.As(err, &cerr), "want a connect error, got %v", err)
	for _, d := range cerr.Details() {
		v, verr := d.Value()
		if verr != nil {
			continue
		}
		if m, ok := v.(*reliantv1.ProjectCheckoutMissing); ok {
			return m
		}
	}
	t.Fatalf("no ProjectCheckoutMissing detail on %v", err)
	return nil
}

// TestFileSystemProxy_GetFileTree_QueuedCloneIsCloningNotInternal is the
// owner's forge clone: CreateProjectFromRepo queued the clone at 15:04:10.74,
// the UI opened the project at once, and every tree read until the clone
// landed eight seconds later came back INTERNAL with "no such file or
// directory" — rendered verbatim, repeatedly. The project root missing while a
// clone onto THIS machine is installing is a state, not a failure.
func TestFileSystemProxy_GetFileTree_QueuedCloneIsCloningNotInternal(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	ctx := fsProxyContext()
	remote := "https://github.com/reliant-labs/forge.git"
	require.NoError(t, repo.UpdateProject(ctx, &db.Project{ID: projectID, Name: "forge", Path: base, UserID: fsProxyTestUser, RemoteURL: &remote}, fsProxyTestUser))
	require.NoError(t, repo.UpsertQueuedProjectDaemon(ctx, projectID, "2aab1465", base, nil, "clone:"+projectID+":2aab1465"))

	svc := NewFileSystemProxyService(&fsMissingDirRouter{daemonID: "2aab1465"}, repo)
	_, err := svc.GetFileTree(ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{ProjectId: projectID, Path: "/"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "a missing project root is NOT_FOUND, not INTERNAL")

	d := checkoutMissingDetail(t, err)
	assert.Equal(t, reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING, d.GetState())
	assert.Equal(t, "2aab1465", d.GetDaemonId())
	assert.Equal(t, projectID, d.GetProjectId())
	assert.Equal(t, remote, d.GetRemoteUrl())
}

// TestFileSystemProxy_GetFileTree_ProjectNotOnThisMachineIsAbsent is
// houndersclub: a repository project whose checkout was never cloned onto the
// owner's new machine. Nothing is putting it there, and project-dir-heal
// deliberately never mkdirs a repository's directory, so the honest answer is
// ABSENT — with the remote, so the UI can offer to clone it here.
func TestFileSystemProxy_GetFileTree_ProjectNotOnThisMachineIsAbsent(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	ctx := fsProxyContext()
	remote := "https://github.com/acme/houndersclub.git"
	require.NoError(t, repo.UpdateProject(ctx, &db.Project{ID: projectID, Name: "houndersclub", Path: base, UserID: fsProxyTestUser, RemoteURL: &remote}, fsProxyTestUser))
	// Cloned onto a DIFFERENT machine, still installing there: that must not
	// make this machine read as "cloning".
	require.NoError(t, repo.UpsertQueuedProjectDaemon(ctx, projectID, "old-machine", base, nil, "clone:"+projectID+":old-machine"))

	svc := NewFileSystemProxyService(&fsMissingDirRouter{daemonID: "2aab1465"}, repo)
	_, err := svc.GetFileTree(ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{ProjectId: projectID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	d := checkoutMissingDetail(t, err)
	assert.Equal(t, reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, d.GetState())
	assert.Equal(t, remote, d.GetRemoteUrl())
}

// TestFileSystemProxy_GetFileTree_MissingSubdirStaysAsItWas: only the project
// ROOT gets the checkout treatment. A missing subdirectory is an ordinary race
// with the user's own edits and keeps its existing handling.
func TestFileSystemProxy_GetFileTree_MissingSubdirStaysAsItWas(t *testing.T) {
	repo, projectID, _ := seedFSProxyProject(t)
	svc := NewFileSystemProxyService(&fsMissingDirRouter{daemonID: "2aab1465"}, repo)
	_, err := svc.GetFileTree(fsProxyContext(), connect.NewRequest(&reliantv1.GetFileTreeRequest{ProjectId: projectID, Path: "pkg"}))
	require.Error(t, err)
	var cerr *connect.Error
	require.True(t, errors.As(err, &cerr))
	assert.Empty(t, cerr.Details(), "a missing subdirectory must not be reported as a missing checkout")
	// ...but it is still the state of the user's disk, not a server fault:
	// as INTERNAL it was reported to Sentry by the server and the browser.
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// ELECTRON-9Y: the client names the project's MAIN worktree when the user is on
// the main checkout, so a missing project root arrived with a worktree id and
// skipped the checkout classification entirely, coming back INTERNAL.
func TestFileSystemProxy_GetFileTree_MissingRootViaMainWorktreeIsACheckoutState(t *testing.T) {
	repo, projectID, base := seedFSProxyProject(t)
	ctx := fsProxyContext()
	now := time.Now()
	mainID := uuid.NewString()
	require.NoError(t, repo.CreateWorktree(ctx, &db.Worktree{
		ID: mainID, Name: "main", Path: base, Branch: "main", BaseBranch: "main", ProjectID: projectID,
		Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), IsMain: true,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	svc := NewFileSystemProxyService(&fsMissingDirRouter{daemonID: "2aab1465"}, repo)
	_, err := svc.GetFileTree(ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{ProjectId: projectID, WorktreeId: &mainID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	d := checkoutMissingDetail(t, err)
	assert.Equal(t, reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, d.GetState())
}

// fsFailRouter fails every daemon command with a fixed error.
type fsFailRouter struct {
	worktreeTestDaemonRouter
	err error
}

func (r *fsFailRouter) SendDaemonCommand(context.Context, string, string, []byte, int32) ([]byte, error) {
	return nil, r.err
}

// sendCommand is where every proxied fs command's daemon failure gets its
// code, so a missing path is NOT_FOUND for all of them — not only the tree.
func TestFileSystemProxy_SendCommand_ClassifiesMissingPath(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want connect.Code
	}{
		{"missing dir (linux)", `daemon command "fs.get_tree" failed: read dir /home/workspace/projects/laptop: open /home/workspace/projects/laptop: no such file or directory`, connect.CodeNotFound},
		{"missing file", `daemon command "fs.read_file" failed: open /w/a.txt: no such file or directory`, connect.CodeNotFound},
		{"missing path (windows)", `daemon command "fs.stat" failed: CreateFile C:\\w\\a: The system cannot find the path specified.`, connect.CodeNotFound},
		// The daemon's own install, not the user's files: still a server fault.
		{"missing binary", `daemon command "fs.search" failed: ripgrep failed: fork/exec /usr/local/bin/rg: no such file or directory`, connect.CodeInternal},
		{"anything else", `daemon command "fs.read_file" failed: read /w/a.txt: input/output error`, connect.CodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &FileSystemProxyService{router: &fsFailRouter{err: errors.New(tc.err)}}
			svc.wake = machineWake{router: svc.router}
			var resp struct{}
			err := svc.sendCommand(fsProxyContext(), fsProxyTestUser, wakeTarget{}, "fs.read_file", map[string]any{}, &resp, 1000)
			require.Error(t, err)
			assert.Equal(t, tc.want, connect.CodeOf(err))
		})
	}
}

func TestCheckoutStateOn(t *testing.T) {
	rows := []*db.ProjectDaemon{
		{DaemonID: "a", InstallState: core.ProjectInstallFailed, InstallError: "auth"},
		{DaemonID: "b", InstallState: core.ProjectInstallInstalling},
	}
	cases := []struct {
		daemon string
		want   reliantv1.ProjectCheckoutState
		reason string
	}{
		{"a", reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONE_FAILED, "auth"},
		{"b", reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING, ""},
		{"c", reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, ""},
		// Unknown machine: only an in-flight clone counts.
		{"", reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING, ""},
	}
	for _, c := range cases {
		got, reason := checkoutStateOn(rows, c.daemon)
		assert.Equal(t, c.want, got, "daemon %q", c.daemon)
		assert.Equal(t, c.reason, reason, "daemon %q", c.daemon)
	}
}
