// Copyright (c) 2025 Reliant Labs
package projectcheckout

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

type installs []*core.ProjectDaemon

func (i installs) ListProjectDaemonsForProject(context.Context, string) ([]*core.ProjectDaemon, error) {
	return i, nil
}

func (installs) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	return &db.Daemon{ID: id, UserID: "u", Name: id}, nil
}

// machines places the named machines it knows; default resolution is def.
type machines struct{ def string }

func (m machines) PlacementDaemonID(_ context.Context, _, id string) (string, error) {
	if id == "" {
		return m.def, nil
	}
	return id, nil
}

type recorder struct{ daemon, command string }

func (r *recorder) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, _ []byte, _ int32) ([]byte, error) {
	r.daemon, r.command = daemonID, commandType
	return nil, nil
}

func detailOf(t *testing.T, err error) *reliantv1.ProjectCheckoutMissing {
	t.Helper()
	for _, d := range svcerr.ToConnect(err).Details() {
		v, verr := d.Value()
		require.NoError(t, verr)
		if m, ok := v.(*reliantv1.ProjectCheckoutMissing); ok {
			return m
		}
	}
	t.Fatalf("no ProjectCheckoutMissing detail on %v", err)
	return nil
}

var mac = &db.Project{ID: "p", Name: "cp", Path: "/Users/creator/cp"}

func TestResolve_TheMachineAndItsOwnPathFromOneRow(t *testing.T) {
	rows := installs{
		{DaemonID: "laptop", Path: "/Users/creator/cp", InstallState: core.ProjectInstallInstalled},
		{DaemonID: "cloud", Path: "/home/workspace/projects/cp", InstallState: core.ProjectInstallInstalled},
	}
	got, err := Resolve(context.Background(), rows, machines{def: "cloud"}, "u", mac, "use it")
	require.NoError(t, err)
	assert.Equal(t, "cloud", got.DaemonID)
	assert.Equal(t, "/home/workspace/projects/cp", got.Path, "never projects.path on another machine")
	assert.Equal(t, "/home/workspace/projects/cp/svc", got.Join("svc"))
	assert.Equal(t, got.Path, got.Join(""))
}

func TestResolve_WithoutARouterNamesTheOldestInstall(t *testing.T) {
	rows := installs{{DaemonID: "first", Path: "/a", InstallState: core.ProjectInstallInstalled}}
	got, err := Resolve(context.Background(), rows, nil, "u", mac, "use it")
	require.NoError(t, err)
	assert.Equal(t, "first", got.DaemonID)
}

func TestResolve_NoCheckoutIsActionable(t *testing.T) {
	remote := "https://github.com/acme/cp"
	withRemote := &db.Project{ID: "p", Name: "cp", Path: "/Users/creator/cp", RemoteURL: &remote}

	cases := map[string]struct {
		rows    installs
		project *db.Project
		state   reliantv1.ProjectCheckoutState
		says    string
	}{
		"nowhere, with a remote":  {nil, withRemote, reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, "clone it onto a machine to use it"},
		"nowhere, a plain folder": {nil, mac, reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, "connect the machine that holds its folder to use it"},
		"cloning": {installs{{DaemonID: "cloud", Path: "/c", InstallState: core.ProjectInstallInstalling}}, withRemote,
			reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING, "still being cloned onto"},
		"clone failed": {installs{{DaemonID: "cloud", Path: "/c", InstallState: core.ProjectInstallFailed, InstallError: "auth"}}, withRemote,
			reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONE_FAILED, "failed (auth): clone it again to use it"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Resolve(context.Background(), tc.rows, machines{def: "cloud"}, "u", tc.project, "use it")
			require.Error(t, err)
			assert.True(t, IsMissing(err))
			assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
			msg := svcerr.ToConnect(err).Message()
			assert.Contains(t, msg, tc.says)
			assert.NotContains(t, msg, "/Users/creator/cp", "no machine's path is the answer")
			d := detailOf(t, err)
			assert.Equal(t, tc.state, d.State)
			assert.Equal(t, "p", d.ProjectId)
		})
	}
}

func TestGoneError_NamesTheMachineAndItsPath(t *testing.T) {
	target := Target{Project: mac, DaemonID: "cloud", Path: "/home/workspace/projects/cp"}
	err := GoneError(context.Background(), installs{}, target, errors.New("dir does not exist"))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	msg := svcerr.ToConnect(err).Message()
	assert.Contains(t, msg, "/home/workspace/projects/cp")
	assert.Contains(t, msg, `"cloud"`)
	d := detailOf(t, err)
	assert.Equal(t, "cloud", d.DaemonId)
	assert.Equal(t, "/home/workspace/projects/cp", d.Path)
}

func TestTargetSendsOnlyToItsMachine(t *testing.T) {
	target := Target{Project: mac, DaemonID: "cloud", Path: "/p"}
	r := &recorder{}
	_, _ = target.Send(context.Background(), r, "u", "x.one", nil, 1)
	assert.Equal(t, "cloud", r.daemon)
	_, _ = target.Pinned(r).SendDaemonCommand(context.Background(), "u", "x.two", nil, 1)
	assert.Equal(t, "cloud", r.daemon)
	assert.Equal(t, "x.two", r.command)
}
