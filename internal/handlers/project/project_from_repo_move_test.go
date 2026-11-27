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

// seedCheckout records a project checked out at path on machine; a deleted
// project is scheduled for deletion, as DeleteProject leaves it.
func seedCheckout(t *testing.T, ctx context.Context, repo *db.Repo, userID, machine, path string, deleted bool) *db.Project {
	t.Helper()
	now := time.Now().UTC()
	p := &db.Project{
		ID: uuid.NewString(), Name: "p-" + uuid.NewString()[:8], Path: path,
		UserID: userID, IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	if deleted {
		after := now.Add(core.ProjectDeleteWindow)
		p.DeleteAfter, p.DeletedBy = &after, &userID
	}
	require.NoError(t, repo.CreateProject(ctx, p))
	require.NoError(t, repo.UpsertProjectDaemon(ctx, p.ID, machine, path, nil))
	return p
}

// TestMachineProjectsDir_StaleCheckoutAtTheWorkingDirectoryDoesNotDecide is
// the prod failure of 2026-11-26: a self-hosted machine whose daemon runs in
// /home/workspace still had an installed row for a deleted project at
// /home/workspace itself, older than every live checkout under
// /home/workspace/projects. Its parent won, and the clone failed with "mkdir
// /home/.reliant-clone-reliant-…: permission denied". The clone goes where
// the machine's live checkouts are, whether that old project is deleted or
// not.
func TestMachineProjectsDir_StaleCheckoutAtTheWorkingDirectoryDoesNotDecide(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deleted bool
	}{
		{"deleted project", true},
		{"live project", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
			svc, repo, cleanup := newFromRepoService(t, cp)
			defer cleanup()
			ctx, userID := fromRepoCtx()
			selfHosted := "self_hosted"
			box := "box-" + uuid.NewString()[:8]
			require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: box, UserID: userID, DaemonType: &selfHosted, WorkingDir: ptrStr("/home/workspace")}))
			seedCheckout(t, ctx, repo, userID, box, "/home/workspace", tc.deleted)
			seedCheckout(t, ctx, repo, userID, box, "/home/workspace/projects/a", false)
			seedCheckout(t, ctx, repo, userID, box, "/home/workspace/projects/b", false)

			machine, err := repo.GetDaemon(ctx, box)
			require.NoError(t, err)
			assert.Equal(t, "/home/workspace/projects/reliant", svc.defaultClonePath(ctx, machine, "reliant"),
				"the clone goes beside the machine's live checkouts, never above its working directory")
		})
	}
}

// TestMachineProjectsDir_AFailedCloneIsNoEvidence: a clone that failed left
// nothing on disk, so however many there are, the directory they were bound
// for is not where the machine keeps projects.
func TestMachineProjectsDir_AFailedCloneIsNoEvidence(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()
	ctx, userID := fromRepoCtx()
	selfHosted := "self_hosted"
	box := "box-" + uuid.NewString()[:8]
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: box, UserID: userID, DaemonType: &selfHosted, WorkingDir: ptrStr("/home/workspace")}))
	for _, path := range []string{"/opt/elsewhere/a", "/opt/elsewhere/b"} {
		p := seedCheckout(t, ctx, repo, userID, box, path, false)
		recorded, err := repo.RecordFailedProjectDaemonRestoreInScope(ctx, p.ID, box, path, nil, "clone failed", nil)
		require.NoError(t, err)
		require.True(t, recorded)
	}
	machine, err := repo.GetDaemon(ctx, box)
	require.NoError(t, err)
	assert.Equal(t, "/home/workspace/new", svc.defaultClonePath(ctx, machine, "new"),
		"with no live checkout the working directory decides, not failed clones")

	seedCheckout(t, ctx, repo, userID, box, "/home/workspace/projects/a", false)
	assert.Equal(t, "/home/workspace/projects/new", svc.defaultClonePath(ctx, machine, "new"),
		"one live checkout outweighs any number of failed ones")
}

// TestCreateProjectFromRepo_ClonesAGitHubSSHRemoteOverHTTPS: the clone is
// queued only when the control plane holds the user's GitHub token, and the
// machine's git authenticates with that token through a credential helper,
// which SSH never consults. control-plane's remote is
// git@github.com:reliant-labs/control-plane.git: on a machine with no SSH key
// for it the clone could only fail ("Permission denied (publickey)"). So the
// clone goes over the https form, and the project keeps the remote as given.
// A remote on another host keeps SSH: the token answers github.com alone.
func TestCreateProjectFromRepo_ClonesAGitHubSSHRemoteOverHTTPS(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()
	ctx, userID := fromRepoCtx()

	name := "control-plane-" + uuid.NewString()[:8]
	sshRemote := "git@github.com:reliant-labs/" + name + ".git"
	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: sshRemote, DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/reliant-labs/"+name+".git", cp.lastReq.CloneURL, "a token cannot authenticate an SSH clone")
	assert.Equal(t, sshRemote, resp.Msg.GetProject().GetRemoteUrl(), "the project's remote is left as given")
	projects, err := repo.ListProjects(ctx, db.ProjectFilters{UserID: userID})
	require.NoError(t, err)
	require.Len(t, projects, 1)
	require.NotNil(t, projects[0].RemoteURL)
	assert.Equal(t, sshRemote, *projects[0].RemoteURL)

	gitlab := "git@gitlab.com:acme/" + name + ".git"
	_, err = svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: gitlab, DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)
	assert.Equal(t, gitlab, cp.lastReq.CloneURL, "another host's SSH remote keeps SSH")
}

// TestCreateProjectFromRepo_IgnoresAnotherPlatformsCheckoutPath is the prod
// failure of 2026-11-23: control-plane sat at /Users/.../control-plane on two
// Macs, and the new-chat "Set up on ovh-build-1" sent that path to the Linux
// box, whose daemon failed at once with "mkdir /Users: permission denied". A
// path only machines of another OS hold the project at is never a
// destination for this one, whatever an (older) client sends: the clone
// takes the target's default instead. The same path on a machine of the SAME
// OS (a second Mac) is honored, and so is any other explicit destination
// (Settings' "Clone onto a machine…").
func TestCreateProjectFromRepo_IgnoresAnotherPlatformsCheckoutPath(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()
	ctx, userID := fromRepoCtx()

	suffix := uuid.NewString()[:8]
	mac, mac2, linuxBox := "mac-"+suffix, "mac2-"+suffix, "ovh-"+suffix
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: mac, UserID: userID, Platform: ptrStr("darwin/arm64")}))
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: mac2, UserID: userID, Platform: ptrStr("darwin/arm64")}))
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: linuxBox, UserID: userID, Platform: ptrStr("linux/amd64")}))

	name := "control-plane-" + suffix
	cloneURL := "https://github.com/reliant-labs/" + name + ".git"
	macPath := "/Users/me/src/reliant-labs/" + name
	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: cloneURL, DaemonId: mac, Name: &name, Path: &macPath,
	}))
	require.NoError(t, err)
	projectID := resp.Msg.GetProject().GetId()
	require.NoError(t, repo.UpsertProjectDaemon(ctx, projectID, mac, macPath, nil))

	// The Mac's path, sent for the Linux box: the box's default instead.
	resp, err = svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: cloneURL, DaemonId: linuxBox, Name: &name, Path: &macPath,
	}))
	require.NoError(t, err)
	assert.Equal(t, projectID, resp.Msg.GetProject().GetId(), "the same project, on a second machine")
	assert.Equal(t, "/home/workspace/projects/"+name, cp.lastReq.Path, "the clone must not be sent the Mac's path")
	assert.Equal(t, "/home/workspace/projects/"+name, resp.Msg.GetProjectDaemon().GetPath())

	// A destination chosen for the Linux box is its own: honored.
	_, err = svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: cloneURL, DaemonId: linuxBox, Name: &name, Path: ptrStr("/srv/checkouts/" + name),
	}))
	require.NoError(t, err)
	assert.Equal(t, "/srv/checkouts/"+name, cp.lastReq.Path)

	// The same path on a second Mac is a real destination: honored.
	_, err = svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: cloneURL, DaemonId: mac2, Name: &name, Path: &macPath,
	}))
	require.NoError(t, err)
	assert.Equal(t, macPath, cp.lastReq.Path)
}

func ptrStr(s string) *string { return &s }
