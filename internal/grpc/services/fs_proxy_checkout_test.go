package services

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
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
