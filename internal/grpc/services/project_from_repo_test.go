// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
)

// CreateProjectFromRepo replaces a four-call client chain (ListGitRepos ->
// CloneRepo -> CreateProject -> MarkProjectInstalled) whose steps each failed
// independently, so a failure mid-sequence left a project with no checkout or
// a checkout with no project. These pin the two properties that made the
// chain worth replacing: nothing is persisted when the clone is refused, and
// a queued clone is recorded AS queued rather than as a finished one.

// cloneStubControlPlane records what the service asked the control plane to
// do, and replays a canned result.
type cloneStubControlPlane struct {
	result  controlplane.CloneRepoResult
	err     error
	calls   int
	lastReq controlplane.CloneRepoRequest
	lastJWT string
}

func (s *cloneStubControlPlane) MintLLMKey(context.Context, string, string) (controlplane.LLMKey, error) {
	return controlplane.LLMKey{}, nil
}

func (s *cloneStubControlPlane) DeleteCurrentUserAccount(context.Context, string) ([]controlplane.AccountDeletionBlocker, error) {
	return nil, nil
}

func (s *cloneStubControlPlane) CloneRepoOntoDaemon(
	_ context.Context, jwt string, req controlplane.CloneRepoRequest,
) (controlplane.CloneRepoResult, error) {
	s.calls++
	s.lastReq = req
	s.lastJWT = jwt
	if s.err != nil {
		return controlplane.CloneRepoResult{}, s.err
	}
	return s.result, nil
}

func newFromRepoService(t *testing.T, cp *cloneStubControlPlane) (*ProjectService, db.Repository, func()) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	svc := NewProjectService(repo, &createProjectRowRouter{resolveID: "daemon-abc"}).
		WithControlPlaneClient(cp)
	return svc, repo, cleanup
}

func TestCreateProjectFromRepo_QueuedCloneIsRecordedAsInstalling(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{
		ClonedPath: "/home/workspace/projects/widgets",
		Queued:     true,
		DaemonName: "my-machine",
	}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	name := "widgets-" + uuid.NewString()
	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/" + name + ".git",
		DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)

	// The whole point: the checkout does not exist yet, and the response
	// must not imply otherwise.
	assert.True(t, resp.Msg.GetQueued(), "a queued clone must report queued=true")
	assert.Equal(t, "my-machine", resp.Msg.GetDaemonName(),
		"the UI needs the machine's name to say what it is waiting on")
	require.NotNil(t, resp.Msg.GetProject())
	assert.Equal(t, reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLING,
		resp.Msg.GetProjectDaemon().GetInstallState())

	// And it is persisted that way, so a page reload still shows the truth.
	rows, err := repo.ListProjectDaemonsForProject(ctx, resp.Msg.GetProject().GetId())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "installing", string(rows[0].InstallState))
	assert.NotEmpty(t, rows[0].InstallRequestID,
		"without a request id the outcome can never be matched back and the project sits in installing forever")
}

func TestCreateProjectFromRepo_RefusedClonePersistsNothing(t *testing.T) {
	// The reason this is one RPC: a refused clone must not leave a project
	// row the user has to clean up by hand.
	cp := &cloneStubControlPlane{err: connect.NewError(
		connect.CodeFailedPrecondition, errors.New(`no git credential found for provider "github"`))}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	name := "widgets-" + uuid.NewString()
	_, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/" + name + ".git",
		DaemonId: "daemon-abc",
	}))
	require.Error(t, err)
	// The control plane's own code reaches the user, rather than being
	// flattened to Internal — "connect GitHub" is actionable, "internal
	// error" is not.
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	projects, err := repo.ListProjects(ctx, db.ProjectFilters{UserID: userID})
	require.NoError(t, err)
	assert.Empty(t, projects, "a refused clone must not leave a project behind")
}

func TestCreateProjectFromRepo_CompletedCloneIsRecordedAsInstalled(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{
		ClonedPath: "/home/workspace/projects/widgets",
		Queued:     false,
	}}
	svc, repo, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	name := "widgets-" + uuid.NewString()
	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/" + name + ".git",
		DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetQueued())

	rows, err := repo.ListProjectDaemonsForProject(ctx, resp.Msg.GetProject().GetId())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "installed", string(rows[0].InstallState))
}

func TestCreateProjectFromRepo_RequiresATargetMachine(t *testing.T) {
	// Not defaulted server-side on purpose: with several machines the choice
	// is the user's, and picking one silently puts the checkout somewhere
	// they did not ask for.
	cp := &cloneStubControlPlane{}
	svc, _, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-x")
	_, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/widgets.git",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Zero(t, cp.calls, "must not dispatch a clone with no target")
}

func TestCreateProjectFromRepo_ForwardsTheCallersJWT(t *testing.T) {
	// reliant has no GitHub token of its own — the control plane holds it,
	// scoped to the calling user. Forwarding the caller's own credential is
	// what keeps the clone authorized as THEM.
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, _, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	req := connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/widgets-" + uuid.NewString() + ".git",
		DaemonId: "daemon-abc",
	})
	req.Header().Set("Authorization", "Bearer caller-jwt-123")

	_, err := svc.CreateProjectFromRepo(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "caller-jwt-123", cp.lastJWT)
}

func TestCreateProjectFromRepo_DerivesNameAndPathFromTheCloneURL(t *testing.T) {
	cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{Queued: true}}
	svc, _, cleanup := newFromRepoService(t, cp)
	defer cleanup()

	userID := "user-from-repo-" + uuid.NewString()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	name := "widgets-" + uuid.NewString()
	resp, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
		CloneUrl: "https://github.com/acme/" + name + ".git",
		DaemonId: "daemon-abc",
	}))
	require.NoError(t, err)

	assert.Equal(t, name, resp.Msg.GetProject().GetName(),
		"the repo name is the obvious default; making the user retype it is friction")
	assert.Equal(t, "/home/workspace/projects/"+name, cp.lastReq.Path)
}

func TestRepoNameFromCloneURL(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/acme/widgets.git":  "widgets",
		"https://github.com/acme/widgets":      "widgets",
		"https://github.com/acme/widgets/":     "widgets",
		"git@github.com:acme/widgets.git":      "widgets",
		"https://github.com/acme/dots.in.name": "dots.in.name",
	} {
		assert.Equal(t, want, repoNameFromCloneURL(url), "clone url %q", url)
	}
}
