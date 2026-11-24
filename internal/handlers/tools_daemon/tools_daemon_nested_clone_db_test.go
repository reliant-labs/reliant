// Copyright (c) 2026 Reliant Labs
package tools_daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// nestedSetUp queues project's set-up on the machine with one nested
// repository, forge/, and returns it.
func (f *orgMachinesFixture) nestedSetUp(t *testing.T, ctx context.Context, project *db.Project, machine string) *core.Repo {
	t.Helper()
	remote := "https://github.com/acme/forge"
	forge := &core.Repo{ID: uuid.NewString(), ProjectID: project.ID, Name: "forge", RelativePath: "forge", RemoteURL: &remote, CreatedAt: fixedNow, UpdatedAt: fixedNow}
	require.NoError(t, f.repo.CreateRepo(ctx, forge))
	require.NoError(t, f.repo.UpsertQueuedProjectDaemon(ctx, project.ID, machine, f.path, nil, "clone:"+uuid.NewString(), []string{forge.ID}))
	return forge
}

func (f *orgMachinesFixture) nestedStates(t *testing.T, projectID, machine string) []core.NestedRepoState {
	t.Helper()
	rows, err := store.ListProjectDaemonReposForPair(f.ctx, f.repo.ORM(), projectID, machine)
	require.NoError(t, err)
	var out []core.NestedRepoState
	for _, r := range rows {
		out = append(out, r.State)
	}
	return out
}

func (f *orgMachinesFixture) checkout(t *testing.T, projectID, machine string) *core.ProjectDaemon {
	t.Helper()
	rows, err := store.ListProjectDaemonsForProject(f.ctx, f.repo.ORM(), projectID)
	require.NoError(t, err)
	for _, r := range rows {
		if r.DaemonID == machine {
			return r
		}
	}
	t.Fatalf("no checkout of %s on %s", projectID, machine)
	return nil
}

// failNested settles every nested repository of the checkout failed, as a
// pass that then went away before settling the checkout would leave it.
func (f *orgMachinesFixture) failNested(t *testing.T, projectID, machine string) {
	t.Helper()
	for {
		c, err := store.ClaimNextProjectDaemonRepo(f.ctx, f.repo.ORM(), projectID, machine, "gone", nestedCloneLease)
		require.NoError(t, err)
		if c == nil {
			return
		}
		ok, err := store.SettleProjectDaemonRepo(f.ctx, f.repo.ORM(), projectID, machine, c.Repo.RepoID, "gone", "Permission denied (publickey)")
		require.NoError(t, err)
		require.True(t, ok)
	}
}

func refuseClones(t *testing.T) cloneRunner {
	return func(_ context.Context, step cloneStep) error {
		t.Errorf("cloned %s for a project that is not the machine's", step.Path)
		return nil
	}
}

// Review B1: the connect resume authorizes each checkout before it claims or
// clones. A checkout of another org's project the machine still has on
// record, and every checkout once the machine's owner left its org, clone
// nothing; the machine's own org's does.
func TestNestedClonePass_AuthorizesTheProjectAgainstTheMachine(t *testing.T) {
	f := newOrgMachinesFixture(t)
	membership := &leavingMembership{defaultOrgMembership: defaultOrgMembership{def: "acme"}, org: "globex"}
	ctx := authz.WithMembership(context.Background(), membership)
	svc := newTestService(f.repo)
	conn := newTestConn(f.userID, f.globexMachine, nil)
	conn.orgID = "globex"
	f.nestedSetUp(t, ctx, f.acme, f.globexMachine) // retained from before machines kept to their org
	f.nestedSetUp(t, ctx, f.globex, f.globexMachine)

	t.Run("another org's retained checkout", func(t *testing.T) {
		pass := svc.nestedPassFor(conn, f.acme.ID, f.path, landed)
		pass.run = refuseClones(t)
		assert.False(t, pass.runPass(ctx))
		assert.Equal(t, []core.NestedRepoState{core.NestedRepoPending}, f.nestedStates(t, f.acme.ID, f.globexMachine), "another org's repository was claimed")
	})
	t.Run("the owner left the machine's org", func(t *testing.T) {
		membership.left.Store(true)
		defer membership.left.Store(false)
		pass := svc.nestedPassFor(conn, f.globex.ID, f.path, landed)
		pass.run = refuseClones(t)
		assert.False(t, pass.runPass(ctx))
		assert.Equal(t, []core.NestedRepoState{core.NestedRepoPending}, f.nestedStates(t, f.globex.ID, f.globexMachine), "claimed for an owner outside the org")
	})
	t.Run("another user's connection", func(t *testing.T) {
		stranger := newTestConn("someone-else-"+uuid.NewString(), f.globexMachine, nil)
		stranger.orgID = "globex"
		pass := svc.nestedPassFor(stranger, f.globex.ID, f.path, landed)
		pass.run = refuseClones(t)
		assert.False(t, pass.runPass(ctx))
		assert.Equal(t, []core.NestedRepoState{core.NestedRepoPending}, f.nestedStates(t, f.globex.ID, f.globexMachine))
	})
	t.Run("the machine's own org", func(t *testing.T) {
		var cloned []string
		pass := svc.nestedPassFor(conn, f.globex.ID, f.path, landed)
		pass.run = func(_ context.Context, step cloneStep) error { cloned = append(cloned, step.Path); return nil }
		assert.True(t, pass.runPass(ctx))
		assert.Equal(t, []string{f.path + "/forge"}, cloned)
		assert.Equal(t, core.ProjectInstallInstalled, f.checkout(t, f.globex.ID, f.globexMachine).InstallState)
	})
}

// Review B4: once a nested repository failed, neither a repeated filesystem
// announcement of the root nor the stale-install reconcile reports the
// checkout installed: both settle it failed, naming the repository.
func TestNestedFailure_NeverReadsInstalled(t *testing.T) {
	t.Run("repeated announcements", func(t *testing.T) {
		f := newOrgMachinesFixture(t)
		svc := newTestService(f.repo)
		conn := newTestConn(f.userID, f.globexMachine, nil)
		conn.orgID = "globex"
		f.nestedSetUp(t, f.ctx, f.globex, f.globexMachine)
		f.failNested(t, f.globex.ID, f.globexMachine)

		for range 2 {
			require.NoError(t, svc.handleFileSystemChanged(f.ctx, conn, &reliantv1.FileSystemChanged{ProjectPath: f.path}))
			row := f.checkout(t, f.globex.ID, f.globexMachine)
			assert.Equal(t, core.ProjectInstallFailed, row.InstallState)
			assert.Contains(t, row.InstallError, "forge (forge/)")
		}
	})
	t.Run("stale-install reconcile", func(t *testing.T) {
		f := newOrgMachinesFixture(t)
		f.nestedSetUp(t, f.ctx, f.globex, f.globexMachine)
		f.failNested(t, f.globex.ID, f.globexMachine)
		rs := reconcileStore{f.repo, store.NewProjectDaemonDetachmentExtStore(f.repo.ORM()), store.NewProjectExtStore(f.repo.ORM()), store.NewProjectDaemonExtStore(f.repo.ORM())}

		for range 2 {
			installed, err := settleStaleInstall(f.ctx, rs, f.everythingHere, f.checkout(t, f.globex.ID, f.globexMachine), f.scope(t, "globex"))
			require.NoError(t, err)
			assert.False(t, installed)
			row := f.checkout(t, f.globex.ID, f.globexMachine)
			assert.Equal(t, core.ProjectInstallFailed, row.InstallState)
			assert.Contains(t, row.InstallError, "forge (forge/)")
		}
	})
}

// Review B5: an older gateway settled the root installed before this release
// cloned its nested repositories; when one then fails, the checkout reads
// failed with the reason, not installed.
func TestNestedFailure_FailsAnInstalledRoot(t *testing.T) {
	f := newOrgMachinesFixture(t)
	svc := newTestService(f.repo)
	conn := newTestConn(f.userID, f.globexMachine, nil)
	conn.orgID = "globex"
	f.nestedSetUp(t, f.ctx, f.globex, f.globexMachine)
	require.NoError(t, store.MarkProjectDaemonInstalled(f.ctx, f.repo.ORM(), f.globex.ID, f.globexMachine))
	require.Equal(t, core.ProjectInstallInstalled, f.checkout(t, f.globex.ID, f.globexMachine).InstallState)

	pass := svc.nestedPassFor(conn, f.globex.ID, f.path, landed)
	pass.run = func(context.Context, cloneStep) error { return errors.New("Permission denied (publickey)") }
	assert.True(t, pass.runPass(f.ctx))

	row := f.checkout(t, f.globex.ID, f.globexMachine)
	assert.Equal(t, core.ProjectInstallFailed, row.InstallState)
	assert.Contains(t, row.InstallError, "Permission denied (publickey)")
}
