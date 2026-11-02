// Copyright (c) 2025 Reliant Labs
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A forge command carries a path that is only true on one machine, so the
// machine and the path come from the same project_daemons row. These tests pin
// that against the prod report: the forge console sent the project's creation
// path (the owner's Mac) to a cloud machine and got back
// "forge project dir does not exist: /Users/seanteeling/src/reliant-labs/control-plane".

func checkoutDetail(t *testing.T, err error) *reliantv1.ProjectCheckoutMissing {
	t.Helper()
	cerr := svcerr.ToConnect(err) // the wire form svcerr.Interceptor sends
	require.NotNil(t, cerr, "want a connect error, got %v", err)
	for _, d := range cerr.Details() {
		v, verr := d.Value()
		require.NoError(t, verr)
		if m, ok := v.(*reliantv1.ProjectCheckoutMissing); ok {
			return m
		}
	}
	t.Fatalf("no ProjectCheckoutMissing detail on %v", err)
	return nil
}

func sentProjectPath(t *testing.T, router *forgeTestRouter) string {
	t.Helper()
	var sent struct {
		ProjectPath string `json:"project_path"`
	}
	require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
	return sent.ProjectPath
}

func topology(svc *Service) error {
	_, err := svc.GetTopology(authedCtx(), connect.NewRequest(&reliantv1.GetForgeTopologyRequest{ProjectId: "proj-1"}))
	return err
}

// The incident: the project was created on the owner's Mac (projects.path is
// the Mac path) and is also checked out on their cloud machine. The Mac is
// offline, so the cloud machine answers — and it must be sent ITS path.
func TestForgeService_SendsTheChosenMachinesOwnCheckoutPath(t *testing.T) {
	const macPath = "/Users/seanteeling/src/reliant-labs/control-plane"
	const cloudPath = "/home/workspace/projects/control-plane"
	repo := forgeTestInstalls(
		&core.ProjectDaemon{DaemonID: "mac", Path: macPath, InstallState: core.ProjectInstallInstalled},
		&core.ProjectDaemon{DaemonID: "cloud", Path: cloudPath, InstallState: core.ProjectInstallInstalled},
	)
	router := &forgeTestRouter{
		reply:         forgeDaemonReply(t, true, true, 0, `{"project":"control-plane"}`),
		defaultDaemon: "cloud",
		unplaceable:   map[string]bool{"mac": true},
	}

	require.NoError(t, topology(newForgeService(router, repo)))

	assert.Equal(t, "cloud", router.lastDaemonID, "the command goes to the machine whose checkout it names")
	assert.Equal(t, cloudPath, sentProjectPath(t, router), "never the Mac's path on the cloud machine")
}

// Both machines have it, at different paths: whichever is chosen, its own
// path rides with it. The router's default wins when it holds a checkout.
func TestForgeService_PathFollowsTheMachine(t *testing.T) {
	repo := forgeTestInstalls(
		&core.ProjectDaemon{DaemonID: "cloud", Path: "/home/workspace/projects/cp", InstallState: core.ProjectInstallInstalled},
		&core.ProjectDaemon{DaemonID: "laptop", Path: "/Users/me/cp", InstallState: core.ProjectInstallInstalled},
	)
	router := &forgeTestRouter{reply: forgeDaemonReply(t, true, true, 0, `{}`), defaultDaemon: "laptop"}

	require.NoError(t, topology(newForgeService(router, repo)))

	assert.Equal(t, "laptop", router.lastDaemonID)
	assert.Equal(t, "/Users/me/cp", sentProjectPath(t, router))
}

// A connected default machine WITHOUT the project is not asked: it has no
// checkout, and asking it about another machine's path is the bug.
func TestForgeService_SkipsADefaultMachineWithoutTheProject(t *testing.T) {
	router := &forgeTestRouter{reply: forgeDaemonReply(t, true, true, 0, `{}`), defaultDaemon: "laptop-without-it"}

	require.NoError(t, topology(newForgeService(router, forgeTestProjects("/home/workspace/projects/cp"))))

	assert.Equal(t, forgeTestDaemon, router.lastDaemonID)
	assert.Equal(t, "/home/workspace/projects/cp", sentProjectPath(t, router))
}

// No machine of the user's holds the project: a FailedPrecondition saying
// what to do, with nothing sent and no other machine's path anywhere in it.
func TestForgeService_NoCheckoutAnywhereIsActionableAndSendsNothing(t *testing.T) {
	repo := forgeTestInstalls()
	remote := "https://github.com/reliant-labs/control-plane"
	router := &forgeTestRouter{defaultDaemon: "cloud"}

	err := topology(newForgeService(router, withRemote(repo, remote)))

	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	msg := svcerr.ToConnect(err).Message()
	assert.Contains(t, msg, "not checked out on any of your machines")
	assert.Contains(t, msg, "clone it onto a machine")
	assert.NotContains(t, msg, forgeTestCreatorPath, "a path from another machine is never the answer")
	assert.Zero(t, router.sends, "no machine has the project, so nothing may be asked")

	detail := checkoutDetail(t, err)
	assert.Equal(t, reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, detail.State)
	assert.Equal(t, remote, detail.RemoteUrl, "the web can offer to clone it")
	assert.Empty(t, detail.Path)
}

// A plain folder (no remote) cannot be cloned: the sentence says to connect
// the machine that has it instead.
func TestForgeService_NoCheckoutOfAPlainFolderAsksForItsMachine(t *testing.T) {
	err := topology(newForgeService(&forgeTestRouter{}, forgeTestInstalls()))

	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Contains(t, svcerr.ToConnect(err).Message(), "connect the machine that holds its folder")
}

func TestForgeService_CloneInFlightOrFailedIsSaidAsSuch(t *testing.T) {
	t.Run("cloning", func(t *testing.T) {
		repo := forgeTestInstalls(&core.ProjectDaemon{DaemonID: "cloud", Path: "/home/workspace/projects/cp", InstallState: core.ProjectInstallInstalling})
		router := &forgeTestRouter{}
		err := topology(newForgeService(router, repo))

		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		assert.Contains(t, svcerr.ToConnect(err).Message(), "still being cloned")
		detail := checkoutDetail(t, err)
		assert.Equal(t, reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING, detail.State)
		assert.Equal(t, "cloud", detail.DaemonId)
		assert.Zero(t, router.sends)
	})
	t.Run("failed", func(t *testing.T) {
		repo := forgeTestInstalls(&core.ProjectDaemon{DaemonID: "cloud", Path: "/p", InstallState: core.ProjectInstallFailed, InstallError: "repository not found"})
		err := topology(newForgeService(&forgeTestRouter{}, repo))

		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		assert.Contains(t, svcerr.ToConnect(err).Message(), "repository not found")
		detail := checkoutDetail(t, err)
		assert.Equal(t, reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONE_FAILED, detail.State)
		assert.Equal(t, "repository not found", detail.InstallError)
	})
}

// A checkout recorded on another user's machine is not the caller's to use.
func TestForgeService_IgnoresAnotherUsersCheckout(t *testing.T) {
	repo := forgeTestProjects("/theirs")
	repo.GetDaemonFunc = func(_ context.Context, id string) (*db.Daemon, error) {
		return &db.Daemon{ID: id, UserID: "someone-else"}, nil
	}
	router := &forgeTestRouter{}

	err := topology(newForgeService(router, repo))

	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Zero(t, router.sends)
}

// The chosen machine reported a checkout, but its directory is gone now
// (moved or deleted since it last reconciled). NotFound naming the machine and
// the path ON IT, with the detail — not the daemon's bare marker text.
func TestForgeService_ACheckoutGoneFromItsMachineNamesTheMachine(t *testing.T) {
	const cloudPath = "/home/workspace/projects/control-plane"
	router := &forgeTestRouter{replyErr: toolexec.DaemonReportedFailure("forge.topology", "forge project dir does not exist: "+cloudPath)}

	err := topology(newForgeService(router, forgeTestProjects(cloudPath)))

	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	msg := svcerr.ToConnect(err).Message()
	assert.Contains(t, msg, cloudPath)
	assert.Contains(t, msg, "is missing on")
	detail := checkoutDetail(t, err)
	assert.Equal(t, forgeTestDaemon, detail.DaemonId)
	assert.Equal(t, cloudPath, detail.Path)
	assert.Equal(t, reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, detail.State)
}

// A deploy's status lives in memory on the machine that started it, so the
// poll is pinned to the same machine the start was.
func TestForgeService_DeployStatusIsAskedOfTheCheckoutsMachine(t *testing.T) {
	router := &forgeTestRouter{reply: forgeDaemonReply(t, true, true, 0, "")}
	_, err := newForgeTestService(router).GetDeployStatus(authedCtx(),
		connect.NewRequest(&reliantv1.GetForgeDeployStatusRequest{ProjectId: "proj-1", Handle: "h-1"}))
	require.NoError(t, err)
	assert.Equal(t, forgeTestDaemon, router.lastDaemonID)
}

// A failure reading the install records is Internal, never a guessed path.
func TestForgeService_InstallLookupFailureIsInternal(t *testing.T) {
	repo := forgeTestProjects("/p")
	repo.ListProjectDaemonsForProjectFunc = func(context.Context, string) ([]*core.ProjectDaemon, error) {
		return nil, errors.New("db down")
	}
	router := &forgeTestRouter{}

	err := topology(newForgeService(router, repo))

	assert.Equal(t, connect.CodeInternal, svcerr.Code(err))
	assert.Zero(t, router.sends)
}

// withRemote gives the project row a git remote, so it can be cloned.
func withRemote(repo *MockRepository, remote string) *MockRepository {
	get := repo.GetProjectWithUserCheckFunc
	repo.GetProjectWithUserCheckFunc = func(ctx context.Context, id, userID string) (*db.Project, error) {
		p, err := get(ctx, id, userID)
		if p != nil {
			p.RemoteURL = &remote
		}
		return p, err
	}
	return repo
}
