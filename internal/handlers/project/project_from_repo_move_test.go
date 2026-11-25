// Copyright (c) 2025 Reliant Labs
package project

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Setting a project up on another machine (owner, 2026-11-24): a fresh clone
// of the project including its nested repositories, found again under any
// spelling of its remote, into the machine's own projects directory.

func fromRepoCtx() (context.Context, string) {
	userID := "user-move-" + uuid.NewString()
	return context.WithValue(context.Background(), auth.UserIDContextKey, userID), userID
}

// seedMultiRepoProject is a project with a root repository and two nested
// ones, one of which has no remote.
func seedMultiRepoProject(t *testing.T, ctx context.Context, repo *db.Repo, userID, rootRemote string) *db.Project {
	t.Helper()
	now := time.Now().UTC()
	p := &db.Project{
		ID: uuid.NewString(), Name: "reliant-labs-" + uuid.NewString()[:8], Path: "/Users/me/src/reliant-labs",
		UserID: userID, IsGitRepo: true, RemoteURL: &rootRemote, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, repo.CreateProject(ctx, p))
	forgeRemote := "git@github.com:reliant-labs/forge.git"
	for _, r := range []*core.Repo{
		{ID: uuid.NewString(), ProjectID: p.ID, Name: p.Name, RemoteURL: &rootRemote},
		{ID: uuid.NewString(), ProjectID: p.ID, Name: "forge", RelativePath: "forge", RemoteURL: &forgeRemote},
		{ID: uuid.NewString(), ProjectID: p.ID, Name: "scratch", RelativePath: "scratch"},
	} {
		r.CreatedAt, r.UpdatedAt = now, now
		require.NoError(t, repo.CreateRepo(ctx, r))
	}
	return p
}

func TestCreateProjectFromRepo_PlansTheNestedReposWithARemote(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()
	ctx, userID := fromRepoCtx()
	rootRemote := "git@github.com:reliant-labs/" + uuid.NewString() + ".git"
	p := seedMultiRepoProject(t, ctx, repo, userID, rootRemote)

	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: rootRemote, DaemonId: "daemon-abc", Name: &p.Name,
		Path: ptrStr("/home/workspace/projects/reliant-labs"),
	}))
	require.NoError(t, err)
	require.Equal(t, p.ID, resp.Msg.GetProject().GetId(), "setting a project up on another machine is that project, not a new one")
	assert.Equal(t, reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLING, resp.Msg.GetProjectDaemon().GetInstallState())

	nested, err := store.ListProjectDaemonReposForPair(ctx, repo.ORM(), p.ID, "daemon-abc")
	require.NoError(t, err)
	require.Len(t, nested, 1, "the nested repository with a remote is cloned after the root; the one without cannot be")
	assert.Equal(t, "forge", nested[0].RelativePath)
	assert.Equal(t, "git@github.com:reliant-labs/forge.git", nested[0].RemoteURL)
	assert.Equal(t, core.NestedRepoPending, nested[0].State)

	// ListProjectDaemons carries them, so a client can show each one.
	list, err := svc.ListProjectDaemons(ctx, connect.NewRequest(&reliantv1.ListProjectDaemonsRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetProjectDaemons(), 1)
	got := list.Msg.GetProjectDaemons()[0].GetNestedRepos()
	require.Len(t, got, 1)
	assert.Equal(t, "forge", got[0].GetName())
	assert.Equal(t, reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLING, got[0].GetInstallState())
}

func TestCreateProjectFromRepo_FindsTheProjectUnderAnotherSpellingOfItsRemote(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()
	ctx, userID := fromRepoCtx()
	name := "control-plane-" + uuid.NewString()[:8]
	p := seedMultiRepoProject(t, ctx, repo, userID, "git@github.com:reliant-labs/"+name+".git")

	// Picked from the GitHub list on another machine: the HTTPS form.
	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/reliant-labs/" + name, DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)
	assert.Equal(t, p.ID, resp.Msg.GetProject().GetId(), "the same repository over HTTPS must find the SSH project, not make a second one")

	projects, err := repo.ListProjects(ctx, db.ProjectFilters{UserID: userID})
	require.NoError(t, err)
	assert.Len(t, projects, 1)
	// What is stored is left as it was: nothing is rewritten.
	require.NotNil(t, projects[0].RemoteURL)
	assert.Equal(t, "git@github.com:reliant-labs/"+name+".git", *projects[0].RemoteURL)
}

func TestCreateProjectFromRepo_DefaultsToTheMachinesProjectsDirectory(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()
	ctx, userID := fromRepoCtx()
	selfHosted := "self_hosted"
	build := "build-" + uuid.NewString()[:8]
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: build, UserID: userID, DaemonType: &selfHosted, WorkingDir: ptrStr("/srv/src")}))

	name := "widgets-" + uuid.NewString()[:8]
	_, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/" + name + ".git", DaemonId: build,
	}))
	require.NoError(t, err)
	assert.Equal(t, "/srv/src/"+name, cp.lastReq.Path, "a self-hosted machine clones beside its other projects, not into /home/workspace/projects")

	// A managed machine keeps the managed default.
	managed := "managed"
	cloud := "cloud-" + uuid.NewString()[:8]
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: cloud, UserID: userID, DaemonType: &managed, WorkingDir: ptrStr("/home/workspace")}))
	other := "gadgets-" + uuid.NewString()[:8]
	_, err = svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/" + other + ".git", DaemonId: cloud,
	}))
	require.NoError(t, err)
	assert.Equal(t, "/home/workspace/projects/"+other, cp.lastReq.Path)
}

func TestMachineProjectsDir_ADaemonStartedInsideACheckoutUsesItsParent(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()
	ctx, userID := fromRepoCtx()
	selfHosted := "self_hosted"
	laptop := "laptop-" + uuid.NewString()[:8]
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: laptop, UserID: userID, DaemonType: &selfHosted, WorkingDir: ptrStr("/Users/me/src/reliant/web")}))
	p := seedMultiRepoProject(t, ctx, repo, userID, "https://github.com/acme/"+uuid.NewString())
	require.NoError(t, repo.UpsertProjectDaemon(ctx, p.ID, laptop, "/Users/me/src/reliant", nil))

	machine, err := repo.GetDaemon(ctx, laptop)
	require.NoError(t, err)
	assert.Equal(t, "/Users/me/src/new", svc.defaultClonePath(ctx, machine, "new"),
		"a daemon running inside a checkout must not clone into it")
	assert.Equal(t, "/home/workspace/projects/new", svc.defaultClonePath(ctx, nil, "new"))
	root := &db.Daemon{ID: "x", WorkingDir: ptrStr("/")}
	assert.Equal(t, "/home/workspace/projects/new", svc.defaultClonePath(ctx, root, "new"), "the filesystem root is no projects directory")
}

func ptrStr(s string) *string { return &s }
