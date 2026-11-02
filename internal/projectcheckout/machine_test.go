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
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// fleet is installs whose machines belong to the owners named in owner
// (default "u") and carry a display name.
type fleet struct {
	rows  []*core.ProjectDaemon
	owner map[string]string
}

func (f fleet) ListProjectDaemonsForProject(context.Context, string) ([]*core.ProjectDaemon, error) {
	return f.rows, nil
}

func (f fleet) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	owner := "u"
	if o, ok := f.owner[id]; ok {
		owner = o
	}
	return &db.Daemon{ID: id, UserID: owner, Name: map[string]string{"laptop": "Sean's Mac", "cloud": "Cloud box"}[id]}, nil
}

var withRemote = func() *db.Project {
	url := "https://github.com/acme/cp.git"
	return &db.Project{ID: "p", Name: "cp", Path: "/Users/creator/cp", RemoteURL: &url}
}()

func installed(daemonID, path string) *core.ProjectDaemon {
	return &core.ProjectDaemon{DaemonID: daemonID, Path: path, InstallState: core.ProjectInstallInstalled}
}

// A chat already placed on a machine runs at THAT machine's checkout, from
// its own row — never projects.path, never another machine's row.
func TestOn_TheNamedMachinesOwnCheckout(t *testing.T) {
	f := fleet{rows: []*core.ProjectDaemon{installed("laptop", "/Users/creator/cp"), installed("cloud", "/home/workspace/projects/cp")}}
	got, err := On(context.Background(), f, "u", mac, "cloud", "keep working")
	require.NoError(t, err)
	assert.Equal(t, "cloud", got.DaemonID)
	assert.Equal(t, "/home/workspace/projects/cp", got.Path)
}

// The machine has no checkout: the answer names where the project IS and
// both ways out, carries the detail for that machine, and reads as missing.
func TestOn_NotThereNamesTheMachinesThatHaveItAndHowToRecover(t *testing.T) {
	f := fleet{rows: []*core.ProjectDaemon{installed("laptop", "/Users/creator/cp")}}
	_, err := On(context.Background(), f, "u", withRemote, "cloud", "keep working in this chat")
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.ToConnect(err).Code())
	msg := svcerr.ToConnect(err).Message()
	assert.Contains(t, msg, `"cp" isn't checked out on "Cloud box"`)
	assert.Contains(t, msg, `switch to "Sean's Mac", which has it`)
	assert.Contains(t, msg, `clone it onto "Cloud box"`)
	assert.Contains(t, msg, "to keep working in this chat")
	assert.True(t, IsMissing(err), "a caller reads it like no checkout anywhere")
	assert.True(t, errors.Is(err, daemonplacement.ErrNoCheckout))

	d := detailOf(t, err)
	assert.Equal(t, "cloud", d.DaemonId)
	assert.Equal(t, reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, d.State)
	assert.Equal(t, "https://github.com/acme/cp.git", d.RemoteUrl)
}

// A clone still landing fixes itself: say so, offer no clone.
func TestOn_CloningSaysWaitNotClone(t *testing.T) {
	f := fleet{rows: []*core.ProjectDaemon{{DaemonID: "cloud", Path: "/home/workspace/projects/cp", InstallState: core.ProjectInstallInstalling}}}
	_, err := On(context.Background(), f, "u", withRemote, "cloud", "keep working")
	require.Error(t, err)
	msg := svcerr.ToConnect(err).Message()
	assert.Contains(t, msg, `still being cloned onto "Cloud box"`)
	assert.NotContains(t, msg, "clone it onto")
	assert.Equal(t, reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING, detailOf(t, err).State)
}

// Another user's machine is never the answer, nor offered as a way out.
func TestOn_AnotherUsersMachineIsNeitherAnswerNorSuggestion(t *testing.T) {
	f := fleet{
		rows:  []*core.ProjectDaemon{installed("cloud", "/home/workspace/projects/cp"), installed("laptop", "/Users/creator/cp")},
		owner: map[string]string{"cloud": "someone-else", "laptop": "someone-else"},
	}
	_, err := On(context.Background(), f, "u", mac, "cloud", "keep working")
	require.Error(t, err)
	msg := svcerr.ToConnect(err).Message()
	assert.NotContains(t, msg, "switch to")
	assert.Contains(t, msg, "connect the machine that holds its folder", "a plain folder with no remote cannot be cloned")
}

func TestForMachine_EmptyIsTheProjectsPlacedMachine(t *testing.T) {
	f := fleet{rows: []*core.ProjectDaemon{installed("laptop", "/Users/creator/cp"), installed("cloud", "/home/workspace/projects/cp")}}
	got, err := ForMachine(context.Background(), f, machines{def: "cloud"}, "u", mac, "", "use it")
	require.NoError(t, err)
	assert.Equal(t, "cloud", got.DaemonID)
	assert.Equal(t, "/home/workspace/projects/cp", got.Path)

	got, err = ForMachine(context.Background(), f, machines{def: "cloud"}, "u", mac, "laptop", "use it")
	require.NoError(t, err)
	assert.Equal(t, "/Users/creator/cp", got.Path, "a named machine is On, not placement")
}

// A run path computed for the machine the chat was on before it moved is the
// same place relative to the checkout on the machine it runs on now.
func TestRebase_MovesARunPathOntoThisMachinesCheckout(t *testing.T) {
	f := fleet{rows: []*core.ProjectDaemon{installed("laptop", "/Users/creator/cp"), installed("cloud", "/home/workspace/projects/cp")}}
	cloud, err := On(context.Background(), f, "u", mac, "cloud", "use it")
	require.NoError(t, err)

	assert.Equal(t, "/home/workspace/projects/cp", cloud.Rebase("/Users/creator/cp"))
	assert.Equal(t, "/home/workspace/projects/cp/svc/api", cloud.Rebase("/Users/creator/cp/svc/api"))
	assert.Equal(t, "/home/workspace/projects/cp/svc", cloud.Rebase("/home/workspace/projects/cp/svc"), "already on this machine")
	assert.Equal(t, "/home/workspace/projects/cp", cloud.Rebase(""), "no run path is the checkout itself")
	assert.Equal(t, "/home/u/.reliant/worktrees/cp/feat", cloud.Rebase("/home/u/.reliant/worktrees/cp/feat"), "a branch worktree is not under a checkout")
	assert.Equal(t, "/Users/creator/cpx", cloud.Rebase("/Users/creator/cpx"), "a sibling with the checkout as a name prefix is not inside it")
}

// A run started before checkouts were resolved per machine carries the
// project's creation path. The machine that created it may have no installed
// row any more, but that path is still recognised and moved onto the chosen
// machine's checkout, so the model is not told on every batch that its
// workspace is missing.
func TestRebase_TheCreationPathOfARunInFlightIsMovedOntoThisMachine(t *testing.T) {
	f := fleet{rows: []*core.ProjectDaemon{installed("cloud", "/home/workspace/projects/cp")}}
	cloud, err := On(context.Background(), f, "u", mac, "cloud", "use it")
	require.NoError(t, err)
	assert.Equal(t, "/home/workspace/projects/cp/svc", cloud.Rebase(mac.Path+"/svc"))
	assert.Equal(t, "/home/workspace/projects/cp", cloud.Rebase(mac.Path))
}
