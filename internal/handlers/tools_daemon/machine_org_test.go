package tools_daemon

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// defaultOrgMembership is a membership whose answer is "a member of every
// org", with def the default org: what authz.OrgScopeFor asks.
type defaultOrgMembership struct{ def string }

func (defaultOrgMembership) IsMember(context.Context, string, string) (bool, bool, error) {
	return true, true, nil
}

func (m defaultOrgMembership) DefaultOrg(context.Context, string) (string, error) { return m.def, nil }

// orgMachinesFixture: one user in two orgs ("acme", their default, and
// "globex"), a project of each at the SAME path — what every cloud machine
// names /home/workspace/projects/<name> — and a machine of globex.
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
	now := time.Now().UTC()
	p := &db.Project{ID: uuid.NewString(), UserID: f.userID, Name: "dbpwathces", Path: f.path, OrgID: &org,
		CreatedAt: now, UpdatedAt: now, LastActive: now}
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

func (f *orgMachinesFixture) scope(t *testing.T) *core.OrgScope {
	t.Helper()
	scope, err := authz.OrgScopeFor(f.ctx, f.userID, "globex")
	require.NoError(t, err)
	return scope
}

// The owner's bug: a cloud machine of org B with a folder at the path of an
// org-A project. Its connect reconcile records org B's project there and
// never records, or reports missing, org A's.
func TestReconcile_MachineOfOneOrgNeverRecordsAnotherOrgsProject(t *testing.T) {
	f := newOrgMachinesFixture(t)

	missing, added, err := reconcileProjectDaemons(f.ctx, f.repo, f.globexMachine, f.userID, f.scope(t), f.everythingHere)
	require.NoError(t, err)

	assert.Equal(t, []string{f.path}, added)
	assert.Empty(t, missing, "another org's project is not missing from this machine either: nothing restores it here")
	assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.globex.ID))
	assert.Empty(t, f.machinesOf(t, f.acme.ID), "org A's project was recorded on org B's machine")
}

// A machine whose org is not known keeps the behaviour of before: every org's
// project at a folder it has is recorded.
func TestReconcile_MachineWithNoKnownOrgMatchesEveryOrg(t *testing.T) {
	f := newOrgMachinesFixture(t)

	_, _, err := reconcileProjectDaemons(f.ctx, f.repo, f.globexMachine, f.userID, nil, f.everythingHere)
	require.NoError(t, err)
	assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.acme.ID))
	assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.globex.ID))
}

// The one-off cleanup: a checkout of another org's project the machine
// already has on record (written before machines kept to their org) is
// detached — the record only — on its next connect, and a second pass changes
// nothing. The project, and its checkout on another machine, are untouched.
func TestReconcile_DetachesAnotherOrgsCheckoutIdempotently(t *testing.T) {
	f := newOrgMachinesFixture(t)
	acmeMachine := "acme-machine-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: acmeMachine, UserID: f.userID}))
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.acme.ID, acmeMachine, f.path, nil))
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, f.acme.ID, f.globexMachine, f.path, nil))

	for pass := range 2 {
		_, _, err := reconcileProjectDaemons(f.ctx, f.repo, f.globexMachine, f.userID, f.scope(t), f.everythingHere)
		require.NoError(t, err, "pass %d", pass)
		assert.Equal(t, []string{acmeMachine}, f.machinesOf(t, f.acme.ID), "pass %d: only the cross-org record goes", pass)
		assert.Equal(t, []string{f.globexMachine}, f.machinesOf(t, f.globex.ID), "pass %d", pass)
	}
	p, err := f.repo.GetProject(f.ctx, f.acme.ID)
	require.NoError(t, err, "the project itself is never deleted")
	assert.Equal(t, f.path, p.Path)
	detachments, err := f.repo.ListProjectDaemonDetachmentsForDaemon(f.ctx, f.globexMachine)
	require.NoError(t, err)
	assert.Empty(t, detachments, "the cleanup is not a user's detach: nothing is recorded to undo")
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

// The machine's org comes from its credential when connecting; otherwise it
// is the one recorded by an earlier connect (an unbound login token, or an
// older control plane, never forgets it); and it is "" when neither knows.
func TestMachineOrgAtConnect(t *testing.T) {
	f := newOrgMachinesFixture(t)
	svc := newTestService(f.repo)

	assert.Equal(t, "acme", svc.machineOrgAtConnect(f.ctx, f.userID, f.globexMachine, "acme"))
	assert.Equal(t, "globex", svc.machineOrgAtConnect(f.ctx, f.userID, f.globexMachine, ""))

	// A registration naming no org keeps the recorded one.
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: f.globexMachine, UserID: f.userID}))
	assert.Equal(t, "globex", svc.machineOrgAtConnect(f.ctx, f.userID, f.globexMachine, ""))

	unknown := "unknown-" + uuid.NewString()
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: unknown, UserID: f.userID}))
	assert.Equal(t, "", svc.machineOrgAtConnect(f.ctx, f.userID, unknown, ""))
}
