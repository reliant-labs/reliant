package tools_daemon

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
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

	missing, added, err := reconcileProjectDaemons(f.ctx, f.repo, f.globexMachine, f.userID, f.scope(t, "globex"), "globex", f.everythingHere)
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

	_, _, err := reconcileProjectDaemons(f.ctx, f.repo, unknown, f.userID, f.scope(t, ""), "", f.everythingHere)
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
		_, _, err := reconcileProjectDaemons(f.ctx, f.repo, f.globexMachine, f.userID, f.scope(t, "globex"), "globex", f.everythingHere)
		require.NoError(t, err, "pass %d", pass)
		assert.Equal(t, []string{acmeMachine}, f.machinesOf(t, f.acme.ID), "pass %d: only the cross-org record goes", pass)
		assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.globex.ID), "pass %d", pass)
		assert.Equal(t, []string{f.path}, f.cleanups(t, f.acme.ID), "pass %d: recorded once, restorable", pass)
	}
	p, err := f.repo.GetProject(f.ctx, f.acme.ID)
	require.NoError(t, err, "the project itself is never deleted")
	assert.Equal(t, f.path, p.Path)
	detachments, err := f.repo.ListProjectDaemonDetachmentsForDaemon(f.ctx, f.globexMachine)
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

	missing, added, err := reconcileProjectDaemons(f.ctx, f.repo, f.globexMachine, f.userID, f.scope(t, "globex"), "globex", moveOutDuringProbe)
	require.NoError(t, err)
	assert.Empty(t, added, "a watcher would start for a checkout of another org's project")
	assert.Empty(t, missing)
	assert.Empty(t, f.machinesOf(t, f.globex.ID), "the moved project was recorded on the other org's machine")
	assert.Empty(t, f.machinesOf(t, f.acme.ID))
}
