package tools_daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// fixedNow is every fixture row's timestamp: nothing here depends on the
// wall clock.
var fixedNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// defaultOrgMembership is a membership whose answer is "a member of every
// org", with def the default org: what authz.MachineScopeFor asks.
type defaultOrgMembership struct{ def string }

func (defaultOrgMembership) IsMember(context.Context, string, string) (bool, bool, error) {
	return true, true, nil
}

func (m defaultOrgMembership) DefaultOrg(context.Context, string) (string, error) { return m.def, nil }

// orgMachinesFixture: one user in two orgs ("acme", their default, and
// "globex"), a project of each at the SAME path — what every cloud machine
// names /home/workspace/projects/<name> — and a machine of globex. The same
// path in two orgs needs the per-org keys alone (db.RelaxProjectKeysForTest):
// the schema of the release after this one, which these readers must already
// handle.
type orgMachinesFixture struct {
	repo           *db.Repo
	ctx            context.Context
	userID         string
	globexMachine  string
	path           string
	acme, globex   *db.Project
	everythingHere daemonPathChecker
}

func newOrgMachinesFixture(t *testing.T) *orgMachinesFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	db.RelaxProjectKeysForTest(t, repo)
	f := &orgMachinesFixture{
		repo:          repo,
		ctx:           authz.WithMembership(context.Background(), defaultOrgMembership{def: "acme"}),
		userID:        "org-user-" + uuid.NewString(),
		globexMachine: "globex-machine-" + uuid.NewString(),
		path:          "/home/workspace/projects/dbpwathces-" + uuid.NewString(),
		everythingHere: func(context.Context, string) (projectPathState, error) {
			return projectPathState{Exists: true}, nil
		},
	}
	globex := "globex"
	require.NoError(t, repo.UpsertDaemon(f.ctx, &db.Daemon{ID: f.globexMachine, UserID: f.userID, OrgID: &globex}))
	f.acme = f.newProject(t, "acme")
	f.globex = f.newProject(t, "globex")
	return f
}

func (f *orgMachinesFixture) newProject(t *testing.T, org string) *db.Project {
	t.Helper()
	p := &db.Project{ID: uuid.NewString(), UserID: f.userID, Name: "dbpwathces", Path: f.path, OrgID: &org,
		CreatedAt: fixedNow, UpdatedAt: fixedNow, LastActive: fixedNow}
	require.NoError(t, f.repo.CreateProject(f.ctx, p))
	return p
}

func (f *orgMachinesFixture) machinesOf(t *testing.T, projectID string) []string {
	t.Helper()
	rows, err := f.repo.ListProjectDaemonsForProject(f.ctx, projectID)
	require.NoError(t, err)
	var out []string
	for _, r := range rows {
		out = append(out, r.DaemonID)
	}
	return out
}

func (f *orgMachinesFixture) scope(t *testing.T, machineOrg string) *core.OrgScope {
	t.Helper()
	scope, _, err := authz.MachineScopeFor(f.ctx, f.userID, machineOrg)
	require.NoError(t, err)
	return scope
}

// cleanups is what project_daemon_org_cleanups recorded for the project.
func (f *orgMachinesFixture) cleanups(t *testing.T, projectID string) []string {
	t.Helper()
	var paths []string
	require.NoError(t, f.repo.ORM().Bun().NewRaw(
		`SELECT detached_row->>'path' FROM project_daemon_org_cleanups WHERE project_id = ? ORDER BY id`, projectID).
		Scan(f.ctx, &paths))
	return paths
}

// The owner's bug: a cloud machine of org B with a folder at the path of an
// org-A project. Its connect reconcile records org B's project there and
// never records, or reports missing, org A's.
func TestReconcile_MachineOfOneOrgNeverRecordsAnotherOrgsProject(t *testing.T) {
	f := newOrgMachinesFixture(t)

	missing, added, err := reconcileProjectDaemons(f.ctx, reconcileStore{f.repo, store.NewProjectDaemonDetachmentExtStore(f.repo.ORM()), store.NewProjectExtStore(f.repo.ORM()), store.NewProjectDaemonExtStore(f.repo.ORM())}, f.globexMachine, f.userID, f.scope(t, "globex"), "globex", f.everythingHere)
	require.NoError(t, err)

	assert.Equal(t, []string{f.path}, added)
	assert.Empty(t, missing, "another org's project is not missing from this machine either: nothing restores it here")
	assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.globex.ID))
	assert.Empty(t, f.machinesOf(t, f.acme.ID), "org A's project was recorded on org B's machine")
}

// A machine whose org is not known is held to its owner's DEFAULT org, never
// to every org: it records the default org's project at the folder and not
// the other org's, and detaches nothing (only a known org may).
func TestReconcile_MachineWithNoKnownOrgHoldsOnlyTheDefaultOrg(t *testing.T) {
	f := newOrgMachinesFixture(t)
	unknown := "unknown-machine-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: unknown, UserID: f.userID}))
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.globex.ID, unknown, f.path, nil)) // an old cross-org record

	_, _, err := reconcileProjectDaemons(f.ctx, reconcileStore{f.repo, store.NewProjectDaemonDetachmentExtStore(f.repo.ORM()), store.NewProjectExtStore(f.repo.ORM()), store.NewProjectDaemonExtStore(f.repo.ORM())}, unknown, f.userID, f.scope(t, ""), "", f.everythingHere)
	require.NoError(t, err)
	assert.Contains(t, f.machinesOf(t, f.acme.ID), unknown, "the default org's project is recorded")
	assert.Contains(t, f.machinesOf(t, f.globex.ID), unknown,
		"an unknown org is not authoritative enough to detach anything")
	assert.Empty(t, f.cleanups(t, f.globex.ID))
}

// The one-off cleanup: a checkout of another org's project the machine
// already has on record is detached on its next connect — the record only,
// kept whole in project_daemon_org_cleanups — and a second pass changes
// nothing. The project, and its checkout on another machine, are untouched.
func TestReconcile_DetachesAnotherOrgsCheckoutIdempotentlyAndRecordsIt(t *testing.T) {
	f := newOrgMachinesFixture(t)
	acmeMachine := "acme-machine-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: acmeMachine, UserID: f.userID}))
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.acme.ID, acmeMachine, f.path, nil))
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.acme.ID, f.globexMachine, f.path, nil))

	for pass := range 2 {
		_, _, err := reconcileProjectDaemons(f.ctx, reconcileStore{f.repo, store.NewProjectDaemonDetachmentExtStore(f.repo.ORM()), store.NewProjectExtStore(f.repo.ORM()), store.NewProjectDaemonExtStore(f.repo.ORM())}, f.globexMachine, f.userID, f.scope(t, "globex"), "globex", f.everythingHere)
		require.NoError(t, err, "pass %d", pass)
		assert.Equal(t, []string{acmeMachine}, f.machinesOf(t, f.acme.ID), "pass %d: only the cross-org record goes", pass)
		assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.globex.ID), "pass %d", pass)
		assert.Equal(t, []string{f.path}, f.cleanups(t, f.acme.ID), "pass %d: recorded once, restorable", pass)
	}
	p, err := f.repo.GetProject(f.ctx, f.acme.ID)
	require.NoError(t, err, "the project itself is never deleted")
	assert.Equal(t, f.path, p.Path)
	detachments, err := store.ListProjectDaemonDetachmentsForDaemon(f.ctx, f.repo.ORM(), f.globexMachine)
	require.NoError(t, err)
	assert.Empty(t, detachments, "the cleanup is not a user's detach: nothing is recorded to undo")
}

// The kill switch (RELIANT_MACHINE_ORG_CLEANUP=off) stops the cleanup and
// nothing else: new records still keep to the machine's org.
func TestReconcileAndWatch_KillSwitchStopsTheCleanup(t *testing.T) {
	for _, tc := range []struct {
		setting  string
		detaches bool
	}{{"off", false}, {"on", true}, {"", true}} {
		t.Run("cleanup="+tc.setting, func(t *testing.T) {
			f := newOrgMachinesFixture(t)
			require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.acme.ID, f.globexMachine, f.path, nil))
			svc := newTestService(f.repo)
			svc.deps.Config = &configv1.ReliantConfig{MachineOrg: &configv1.MachineOrgConfig{CrossOrgCleanup: tc.setting}}
			conn := newTestConn(f.userID, f.globexMachine, nil)
			conn.orgID = "globex"

			_, err := svc.reconcileAndWatch(f.ctx, conn, f.everythingHere)
			require.NoError(t, err)
			assert.Equal(t, !tc.detaches, len(f.machinesOf(t, f.acme.ID)) == 1)
			assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.globex.ID))
		})
	}
}

// A connection whose machine's org could not be read runs nothing
// org-sensitive: no record, no cleanup, no by-path resolution. It is never
// treated as a machine of no org.
func TestUnresolvedMachineOrg_RunsNothingOrgSensitive(t *testing.T) {
	f := newOrgMachinesFixture(t)
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.acme.ID, f.globexMachine, f.path, nil))
	svc := newTestService(f.repo)
	conn := newTestConn(f.userID, f.globexMachine, nil)
	conn.orgUnresolved = true

	_, err := svc.reconcileAndWatch(f.ctx, conn, f.everythingHere)
	require.NoError(t, err)
	assert.Empty(t, f.machinesOf(t, f.globex.ID), "nothing recorded")
	assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.acme.ID), "nothing detached")

	_, err = svc.ensureOwnedProjectForPath(f.ctx, conn, f.path, nil)
	assert.ErrorIs(t, err, errMachineOrgUnresolved)
}

// A folder the org-B machine reports at a path that is a project in org A
// (and not in org B) is a new project of org B, never org A's.
func TestEnsureOwnedProjectForPath_ResolvesOnlyTheMachinesOrg(t *testing.T) {
	f := newOrgMachinesFixture(t)
	require.NoError(t, f.repo.DeleteProject(f.ctx, f.globex.ID, f.userID))
	svc := newTestService(f.repo)
	conn := newTestConn(f.userID, f.globexMachine, nil)
	conn.orgID = "globex"

	got, err := svc.ensureOwnedProjectForPath(f.ctx, conn, f.path, nil)
	require.NoError(t, err)
	assert.NotEqual(t, f.acme.ID, got.ID, "org A's project was resolved for org B's machine")
	require.NotNil(t, got.OrgID)
	assert.Equal(t, "globex", *got.OrgID, "the project the machine reported is its org's")
	assert.Empty(t, f.machinesOf(t, f.acme.ID))
}

// Review B2: the first OUTBOUND connect of an org-B managed machine (the
// gateway dials the pod the control plane's snapshot names) records the org
// the snapshot carries, and the connection resolves the folder at a path that
// is a project in both orgs to org B's.
func TestRegisterOutboundConnection_RecordsTheSnapshotsMachineOrg(t *testing.T) {
	f := newOrgMachinesFixture(t)
	pod := "pod-machine-" + uuid.NewString()
	svc := newTestService(f.repo)

	t.Cleanup(svc.Close)
	out := dialInPodOfOrg(t, svc, f.userID, pod, MachineOrg{ID: "globex"})
	t.Cleanup(out.Disconnect)

	d, err := f.repo.GetDaemon(f.ctx, pod)
	require.NoError(t, err)
	require.NotNil(t, d.OrgID)
	assert.Equal(t, "globex", *d.OrgID, "the machine's org is recorded on its first connect")
	assert.Equal(t, "globex", out.conn.orgID)

	got, err := svc.ensureOwnedProjectForPath(f.ctx, out.conn, f.path, nil)
	require.NoError(t, err)
	assert.Equal(t, f.globex.ID, got.ID, "the path resolves to the machine's org's project")
}

// The machine's org at connect: the one the control plane names now; else the
// one an earlier connect recorded (an unbound login token, or an older
// control plane, never forgets it); else none, held to the owner's default
// org. An unreadable answer is unresolved, never "none".
func TestMachineOrgAtConnect(t *testing.T) {
	f := newOrgMachinesFixture(t)
	svc := newTestService(f.repo)
	at := func(daemonID string, named MachineOrg) (string, bool) {
		return svc.machineOrgAtConnect(f.ctx, f.userID, daemonID, named)
	}

	org, unresolved := at(f.globexMachine, MachineOrg{ID: "acme"})
	assert.Equal(t, "acme", org)
	assert.False(t, unresolved)
	org, unresolved = at(f.globexMachine, MachineOrg{})
	assert.Equal(t, "globex", org)
	assert.False(t, unresolved)
	org, unresolved = at(f.globexMachine, MachineOrg{Unresolved: true})
	assert.Equal(t, "globex", org, "a recorded org stands when the snapshot cannot read it")
	assert.False(t, unresolved)

	// A registration naming no org keeps the recorded one.
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: f.globexMachine, UserID: f.userID}))
	org, _ = at(f.globexMachine, MachineOrg{})
	assert.Equal(t, "globex", org)

	unknown := "unknown-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: unknown, UserID: f.userID}))
	org, unresolved = at(unknown, MachineOrg{})
	assert.Equal(t, "", org)
	assert.False(t, unresolved)
	org, unresolved = at(unknown, MachineOrg{Unresolved: true})
	assert.Equal(t, "", org)
	assert.True(t, unresolved, "an org the control plane could not read is unresolved, not none")
}

// Review round 4: a project that moves OUT of the machine's org while the
// reconcile probes the machine is not recorded on it, and no config watcher
// starts for it. The move commits from inside the probe; the insert re-reads
// the project's org under its row lock and refuses.
func TestReconcile_ProjectMovedOutOfTheMachinesOrgDuringTheProbeIsNotRecorded(t *testing.T) {
	f := newOrgMachinesFixture(t)
	moveOutDuringProbe := func(_ context.Context, path string) (projectPathState, error) {
		// MoveProjectOrg's commit, landing between the list and the write.
		_, err := f.repo.ORM().Bun().ExecContext(f.ctx, `UPDATE projects SET org_id = 'initech' WHERE id = ?`, f.globex.ID)
		require.NoError(t, err)
		return projectPathState{Exists: true}, nil
	}

	missing, added, err := reconcileProjectDaemons(f.ctx, reconcileStore{f.repo, store.NewProjectDaemonDetachmentExtStore(f.repo.ORM()), store.NewProjectExtStore(f.repo.ORM()), store.NewProjectDaemonExtStore(f.repo.ORM())}, f.globexMachine, f.userID, f.scope(t, "globex"), "globex", moveOutDuringProbe)
	require.NoError(t, err)
	assert.Empty(t, added, "a watcher would start for a checkout of another org's project")
	assert.Empty(t, missing)
	assert.Empty(t, f.machinesOf(t, f.globex.ID), "the moved project was recorded on the other org's machine")
	assert.Empty(t, f.machinesOf(t, f.acme.ID))
}

// Review round 5: a clone whose outcome was lost is settled from its row's
// own path only while the project is in the machine's org. One that moves out
// of it while the reconcile probes that path is settled neither installed nor
// failed, and no config watcher starts for it; in the org, it settles.
func TestReconcile_LostCloneOfAProjectMovedOutDuringTheProbeIsNotSettled(t *testing.T) {
	// staleSince is long past staleInstallAfter, and fixed.
	staleSince := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		landed    bool
		moveOut   bool
		wantState core.ProjectInstallState
		wantAdded bool
	}{
		{name: "landed, moved out", landed: true, moveOut: true, wantState: core.ProjectInstallInstalling},
		{name: "never landed, moved out", landed: false, moveOut: true, wantState: core.ProjectInstallInstalling},
		{name: "landed, still the machine's org", landed: true, wantState: core.ProjectInstallInstalled, wantAdded: true},
		{name: "never landed, still the machine's org", landed: false, wantState: core.ProjectInstallFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrgMachinesFixture(t)
			require.NoError(t, f.repo.UpsertQueuedProjectDaemon(f.ctx, f.globex.ID, f.globexMachine, f.path, nil, "clone:"+f.globex.ID))
			_, err := f.repo.ORM().Bun().ExecContext(f.ctx,
				`UPDATE project_daemons SET cloned_at = ?, clone_started_at = ? WHERE project_id = ? AND daemon_id = ?`,
				staleSince, staleSince, f.globex.ID, f.globexMachine)
			require.NoError(t, err)
			probe := func(_ context.Context, path string) (projectPathState, error) {
				assert.Equal(t, f.path, path, "a lost clone is settled from its row's own path")
				if tc.moveOut {
					// MoveProjectOrg's commit, landing between the list
					// and the settle. initech: the per-org path key
					// leaves no room for a second project at this path
					// in acme.
					_, err := f.repo.ORM().Bun().ExecContext(f.ctx, `UPDATE projects SET org_id = 'initech' WHERE id = ?`, f.globex.ID)
					require.NoError(t, err)
				}
				return projectPathState{Exists: tc.landed}, nil
			}

			_, added, err := reconcileProjectDaemons(f.ctx, reconcileStore{f.repo, store.NewProjectDaemonDetachmentExtStore(f.repo.ORM()), store.NewProjectExtStore(f.repo.ORM()), store.NewProjectDaemonExtStore(f.repo.ORM())}, f.globexMachine, f.userID, f.scope(t, "globex"), "globex", probe)
			require.NoError(t, err)
			if tc.wantAdded {
				assert.Equal(t, []string{f.path}, added)
			} else {
				assert.Empty(t, added, "a watcher would start for a checkout that did not settle installed")
			}
			rows, err := f.repo.ListProjectDaemonsForProject(f.ctx, f.globex.ID)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, tc.wantState, rows[0].InstallState)
		})
	}
}

// leavingMembership is defaultOrgMembership with one org its user can leave
// while their machine stays connected: IsMember answers from left, read on
// every call, as the live membership is.
type leavingMembership struct {
	defaultOrgMembership
	org  string
	left atomic.Bool
}

func (m *leavingMembership) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return orgID != m.org || !m.left.Load(), true, nil
}

// Review round 6 (B1): a machine of org globex stays connected after its
// owner leaves globex. From then on nothing it reports creates, resolves or
// records a globex project: its connection has no org scope at all.
func TestMachineOfAnOrgItsOwnerLeft_CreatesAndResolvesNothing(t *testing.T) {
	f := newOrgMachinesFixture(t)
	membership := &leavingMembership{defaultOrgMembership: defaultOrgMembership{def: "acme"}, org: "globex"}
	ctx := authz.WithMembership(context.Background(), membership)
	svc := newTestService(f.repo)
	conn := newTestConn(f.userID, f.globexMachine, nil)
	conn.orgID = "globex"

	// A member: the machine resolves its org's project at the path, and
	// records its checkout there.
	got, err := svc.ensureOwnedProjectForPath(ctx, conn, f.path, nil)
	require.NoError(t, err)
	assert.Equal(t, f.globex.ID, got.ID)
	assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.globex.ID))
	// Gone again, so what follows shows what an owner outside the org records.
	require.NoError(t, f.repo.DeleteProjectDaemon(ctx, f.globex.ID, f.globexMachine))

	membership.left.Store(true)

	// The folder of an existing globex project resolves to nothing.
	got, err = svc.ensureOwnedProjectForPath(ctx, conn, f.path, nil)
	require.ErrorIs(t, err, authz.ErrNotOrgMember)
	assert.Nil(t, got, "a machine whose owner left its org resolved one of the org's projects")

	// A new folder creates nothing.
	newPath := "/home/workspace/projects/new-" + uuid.NewString()
	got, err = svc.ensureOwnedProjectForPath(ctx, conn, newPath, nil)
	require.ErrorIs(t, err, authz.ErrNotOrgMember)
	assert.Nil(t, got)
	_, err = store.GetProjectByPathAndUserInScope(ctx, f.repo.ORM(), newPath, f.userID, nil)
	assert.Error(t, err, "a machine whose owner left its org created a project")

	// The connect reconcile records and detaches nothing, folder or not.
	require.NoError(t, f.repo.UpsertProjectDaemon(ctx, f.acme.ID, f.globexMachine, f.path, nil))
	_, err = svc.reconcileAndWatch(ctx, conn, f.everythingHere)
	require.NoError(t, err)
	assert.Empty(t, f.machinesOf(t, f.globex.ID), "the reconcile recorded a checkout for an owner outside the org")
	assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.acme.ID), "nothing is detached either")

	// Back in the org: resolved again.
	membership.left.Store(false)
	got, err = svc.ensureOwnedProjectForPath(ctx, conn, f.path, nil)
	require.NoError(t, err)
	assert.Equal(t, f.globex.ID, got.ID)
}

// Review round 8 (B1): a machine that discovers an existing project of its
// org after its connect reconcile ran (the folder was not there then, so the
// reconcile recorded nothing) gets the checkout recorded by the discovery
// itself, and its config snapshot is stored at once, with no reconnect. A
// project the user detached from the machine still gets nothing.
func TestDiscoveredExistingProject_RecordsTheCheckoutAndStoresTheSnapshot(t *testing.T) {
	snapshot := func(f *orgMachinesFixture) *reliantv1.ProjectConfigSnapshot {
		return &reliantv1.ProjectConfigSnapshot{ProjectPath: f.path, DaemonTimestampUnixMs: fixedNow.UnixMilli(),
			UserConfigYaml: []byte("from the machine")}
	}
	t.Run("discovered after the reconcile", func(t *testing.T) {
		f := newOrgMachinesFixture(t)
		svc := newTestService(f.repo)
		conn := newTestConn(f.userID, f.globexMachine, nil)
		conn.orgID = "globex"
		nothingHere := func(context.Context, string) (projectPathState, error) { return projectPathState{}, nil }
		_, err := svc.reconcileAndWatch(f.ctx, conn, nothingHere)
		require.NoError(t, err)
		require.Empty(t, f.machinesOf(t, f.globex.ID), "precondition: the reconcile recorded nothing")

		require.NoError(t, svc.persistProjectConfigSnapshot(f.ctx, conn, snapshot(f), false))

		assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.globex.ID), "the discovery recorded the machine's checkout")
		record, synced, err := store.GetProjectMachineConfigRecord(f.ctx, f.repo.ORM(), f.globex.ID, f.globexMachine)
		require.NoError(t, err)
		require.True(t, synced, "the snapshot was dropped until a reconnect")
		assert.Equal(t, "from the machine", *record.UserConfigYAML)
		assert.Empty(t, f.machinesOf(t, f.acme.ID), "another org's project at the same path is untouched")
	})
	t.Run("detached by the user", func(t *testing.T) {
		f := newOrgMachinesFixture(t)
		svc := newTestService(f.repo)
		conn := newTestConn(f.userID, f.globexMachine, nil)
		conn.orgID = "globex"
		require.NoError(t, store.DetachProjectFromDaemon(f.ctx, f.repo.ORM(), f.globex.ID, f.globexMachine, f.path, nil))

		require.NoError(t, svc.persistProjectConfigSnapshot(f.ctx, conn, snapshot(f), false))

		assert.Empty(t, f.machinesOf(t, f.globex.ID), "a detached project was recorded on the machine again")
		_, synced, err := store.GetProjectMachineConfigRecord(f.ctx, f.repo.ORM(), f.globex.ID, f.globexMachine)
		require.NoError(t, err)
		assert.False(t, synced, "a detached project's snapshot was stored")
	})
}
