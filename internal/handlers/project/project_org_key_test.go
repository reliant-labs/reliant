package project

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// twoOrgs is control-plane's answer for a user in "acme" (their default) and
// "globex".
type twoOrgs struct{ userID string }

func (s twoOrgs) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	if userID != s.userID {
		return accesstokenclient.OrgMembership{OrgID: orgID}, nil
	}
	if orgID == "" {
		return accesstokenclient.OrgMembership{OrgID: "acme", Member: true}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: orgID == "acme" || orgID == "globex"}, nil
}

// orgsFixture is newMachinesFixture's user and machines, in two orgs: the
// "cloud" machine belongs to globex (as control-plane named it at its
// connect); the others' orgs are not known, so they hold the user's default
// org's (acme's) projects. The per-user project keys are relaxed
// (db.RelaxProjectKeysForTest): the schema of the release after this one,
// on which these readers must already pick the right org's project.
func newOrgsFixture(t *testing.T) *machinesFixture {
	t.Helper()
	f := newMachinesFixture(t)
	db.RelaxProjectKeysForTest(t, f.repo)
	resolver := orgs.New(twoOrgs{userID: f.userID})
	f.ctx = authz.WithMembership(f.ctx, resolver)
	globex := "globex"
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: f.cloud, UserID: f.userID, OrgID: &globex}))
	f.svc = NewTestProject(t, WithProjectDeps(withStores(Deps{Repo: f.repo, Projects: projectaccesstest.For(f.repo), Router: f.disk, Orgs: resolver})))
	return f
}

func (f *machinesFixture) create(t *testing.T, path, org string, machine *string) (*reliantv1.CreateProjectResponse, error) {
	t.Helper()
	resp, err := f.svc.CreateProject(f.ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name: "dbpwathces", Path: path, OrgId: &org, DaemonId: machine,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// The same folder is a project in each of two orgs.
func TestCreateProject_SamePathInTwoOrgs_BothCreatable(t *testing.T) {
	f := newOrgsFixture(t)
	path := "/home/workspace/projects/dbpwathces-" + uuid.NewString()

	f.disk.folder(f.cloud, path, "")
	acme, err := f.create(t, path, "acme", nil)
	require.NoError(t, err)
	globex, err := f.create(t, path, "globex", &f.cloud)
	require.NoError(t, err, "the same path in another org is a new project")

	assert.NotEqual(t, acme.Project.Id, globex.Project.Id)
	assert.Equal(t, "acme", acme.Project.GetOrgId())
	assert.Equal(t, "globex", globex.Project.GetOrgId())
}

// The same folder twice in one org is still refused, naming the org.
func TestCreateProject_SamePathInOneOrg_RefusedNamingTheOrg(t *testing.T) {
	f := newOrgsFixture(t)
	path := "/home/workspace/projects/dbpwathces-" + uuid.NewString()

	_, err := f.create(t, path, "acme", nil)
	require.NoError(t, err)
	_, err = f.create(t, path, "acme", nil)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, codeOf(err))
	assert.Contains(t, err.Error(), "organization acme")
}

// A project with no org yet is the default org's (lazily): a create at its
// path in the default org is refused, in another org it is not.
func TestCreateProject_AnUnassignedProjectIsTheDefaultOrgs(t *testing.T) {
	f := newOrgsFixture(t)
	path := "/home/workspace/projects/legacy-" + uuid.NewString()
	f.project(t, "legacy", path, f.laptop, nil)

	_, err := f.create(t, path, "acme", nil)
	assert.Equal(t, connect.CodeAlreadyExists, codeOf(err), "acme is the default org: %v", err)
	f.disk.folder(f.cloud, path, "")
	_, err = f.create(t, path, "globex", &f.cloud)
	assert.NoError(t, err)
}

// The owner's bug: Add Project in org B on org B's machine, at a folder whose
// path is an org-A project's — recorded on that machine by the old org-blind
// reconcile. It used to fail ("a project already exists at this path:
// \"dbpwathces\" on \"default\"") or attach org A's project to org B's machine.
// Now it is a new org-B project, and org A's project gains nothing.
func TestCreateProject_NamedMachine_IgnoresAnotherOrgsProjectAtThePath(t *testing.T) {
	f := newOrgsFixture(t)
	path := "/home/workspace/projects/dbpwathces-" + uuid.NewString()
	acme, err := f.create(t, path, "acme", nil)
	require.NoError(t, err)
	require.NoError(t, f.repo.UpsertProjectDaemon(f.ctx, acme.Project.Id, f.cloud, path, nil)) // the stale cross-org record
	f.disk.folder(f.cloud, path, "")

	globex, err := f.create(t, path, "globex", &f.cloud)
	require.NoError(t, err)
	assert.False(t, globex.Attached, "org A's project was attached to org B's machine")
	assert.NotEqual(t, acme.Project.Id, globex.Project.Id)
	assert.Equal(t, "globex", globex.Project.GetOrgId())
	assert.Equal(t, map[string]string{f.cloud: path}, f.rows(t, globex.Project.Id))
}

// A request in one org naming a machine of another is refused: what it would
// record there is a checkout across orgs.
func TestCreateProject_NamedMachineOfAnotherOrg_Refused(t *testing.T) {
	f := newOrgsFixture(t)
	path := "/home/workspace/projects/x-" + uuid.NewString()

	_, err := f.create(t, path, "acme", &f.cloud)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	assert.Contains(t, err.Error(), "organization globex")
	assert.Empty(t, f.disk.callsTo(f.cloud), "nothing ran on the machine")
}

// Attaching a project to a machine of another org is refused.
func TestAttachProjectToDaemon_AnotherOrgsMachine_Refused(t *testing.T) {
	f := newOrgsFixture(t)
	path := "/home/workspace/projects/y-" + uuid.NewString()
	acme, err := f.create(t, path, "acme", nil)
	require.NoError(t, err)
	f.disk.folder(f.cloud, path, "")

	_, err = f.svc.AttachProjectToDaemon(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToDaemonRequest{
		ProjectId: acme.Project.Id, DaemonId: f.cloud, Path: path,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	assert.NotContains(t, f.rows(t, acme.Project.Id), f.cloud)
}

// Review B1: a create with no daemon_id resolves its machine and authorizes it
// against the org the request acts in BEFORE anything runs. The default
// machine of another org is refused with nothing sent to it, nothing queued
// and no project created.
func TestCreateProject_DefaultMachineOfAnotherOrg_RefusedBeforeAnyCommand(t *testing.T) {
	f := newOrgsFixture(t)
	globex := "globex"
	require.NoError(t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: f.laptop, UserID: f.userID, OrgID: &globex}))
	path := "/home/workspace/projects/z-" + uuid.NewString()

	_, err := f.create(t, path, "acme", nil)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	assert.Contains(t, err.Error(), "organization globex")
	assert.Empty(t, f.disk.callsTo(f.laptop), "a command reached the machine of another org")
	projects, err := f.repo.ListProjects(f.ctx, db.ProjectFilters{UserID: f.userID})
	require.NoError(t, err)
	for _, p := range projects {
		assert.NotEqual(t, path, p.Path, "the project was created")
	}
}

// A machine whose org is not known holds only its owner's default org's
// projects: another org's project is not attached to it, the default org's is.
func TestAttachProjectToDaemon_MachineOfNoKnownOrgHoldsOnlyTheDefaultOrg(t *testing.T) {
	f := newOrgsFixture(t)
	path := "/home/workspace/projects/w-" + uuid.NewString()
	f.disk.folder(f.cloud, path, "")
	f.disk.folder(f.desktop, path, "")
	globex, err := f.create(t, path, "globex", &f.cloud)
	require.NoError(t, err)

	_, err = f.svc.AttachProjectToDaemon(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToDaemonRequest{
		ProjectId: globex.Project.Id, DaemonId: f.desktop, Path: path,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	assert.Contains(t, err.Error(), "default organization")

	acme, err := f.create(t, "/home/workspace/projects/v-"+uuid.NewString(), "acme", nil)
	require.NoError(t, err)
	f.disk.folder(f.desktop, "/home/workspace/projects/elsewhere", "")
	_, err = f.svc.AttachProjectToDaemon(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToDaemonRequest{
		ProjectId: acme.Project.Id, DaemonId: f.desktop, Path: "/home/workspace/projects/elsewhere",
	}))
	assert.NoError(t, err)
}
