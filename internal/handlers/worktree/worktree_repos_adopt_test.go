// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// adoptTestDaemonRouter serves canned repo.discover results so tests can
// simulate a filesystem that has repos the registry doesn't know about.
type adoptTestDaemonRouter struct {
	toolexectest.Stub
	discovered    []map[string]any
	discoverErr   error
	discoverCalls int
	// discoverOn is the daemon each repo.discover was pinned to ("" for
	// default resolution).
	discoverOn []string
}

func (r *adoptTestDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType == "repo.discover" {
		return r.discover(daemonID)
	}
	return r.Stub.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
}

func (r *adoptTestDaemonRouter) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType == "repo.discover" {
		return r.discover("")
	}
	return r.Stub.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *adoptTestDaemonRouter) discover(daemonID string) ([]byte, error) {
	r.discoverCalls++
	r.discoverOn = append(r.discoverOn, daemonID)
	if r.discoverErr != nil {
		return nil, r.discoverErr
	}
	return json.Marshal(map[string]any{"discovered": r.discovered})
}

// placeOn is a placement that names daemonID.
func placeOn(daemonID string) func() (machinewake.Target, error) {
	return func() (machinewake.Target, error) { return machinewake.Target{DaemonID: daemonID}, nil }
}

func newAdoptTestProject(t *testing.T, repo *db.Repo) *db.Project {
	t.Helper()
	now := time.Now().UTC()
	project := &db.Project{
		ID:         uuid.New().String(),
		UserID:     "user-" + uuid.New().String(),
		Name:       "adopt-test",
		Path:       "/home/workspace/projects/adopt-test",
		IsGitRepo:  false,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}
	require.NoError(t, repo.CreateProject(context.Background(), project))
	return project
}

// TestListProjectReposAdoptsUnregisteredRepos pins the self-heal: a repo that
// exists on disk (e.g. manual `git init` in the workspace terminal) but has no
// registry row is adopted at the gate instead of failing the precondition.
func TestListProjectReposAdoptsUnregisteredRepos(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	project := newAdoptTestProject(t, repo)
	router := &adoptTestDaemonRouter{
		discovered: []map[string]any{{"relative_path": "", "name": "adopt-test"}},
	}
	s := newWorktreeService(t, repo, router)

	repos, err := s.listProjectRepos(ctx, project.UserID, project, placeOn("test-daemon-id"))
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "", repos[0].RelativePath)

	// The adoption persisted: registry row exists and IsGitRepo trued up.
	persisted, err := repo.ListReposByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	updated, err := repo.GetProject(ctx, project.ID)
	require.NoError(t, err)
	assert.True(t, updated.IsGitRepo)

	// Second call is served from the registry — no rediscovery, no duplicates.
	repos, err = s.listProjectRepos(ctx, project.UserID, project, placeOn("test-daemon-id"))
	require.NoError(t, err)
	assert.Len(t, repos, 1)
	assert.Equal(t, 1, router.discoverCalls)
}

// TestListProjectReposStillFailsWhenNothingOnDisk pins that the precondition
// error survives when the daemon confirms there is genuinely no repo.
func TestListProjectReposStillFailsWhenNothingOnDisk(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	project := newAdoptTestProject(t, repo)
	router := &adoptTestDaemonRouter{discovered: nil}
	s := newWorktreeService(t, repo, router)

	_, err := s.listProjectRepos(ctx, project.UserID, project, placeOn("test-daemon-id"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Contains(t, svcerr.ToConnect(err).Message(), "no git repository found in /home/workspace/projects/adopt-test")
	assert.Equal(t, 1, router.discoverCalls)
}

// TestListProjectReposAdoptsFromThePlacementMachine pins the prod failure on
// a cloud-cloned project: its repos were never registered, and adoption asked
// the user's DEFAULT daemon — a connected laptop is preferred — which has no
// checkout at the cloud path, so a real git repo was refused as "project has
// no git repos". Adoption must ask the machine the worktree is placed on.
func TestListProjectReposAdoptsFromThePlacementMachine(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	project := newAdoptTestProject(t, repo)
	router := &adoptTestDaemonRouter{
		discovered: []map[string]any{{"relative_path": "", "name": "adopt-test"}},
	}
	s := newWorktreeService(t, repo, router)

	repos, err := s.listProjectRepos(ctx, project.UserID, project, placeOn("cloud-machine"))
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, []string{"cloud-machine"}, router.discoverOn)
}

// TestListProjectReposPlacesOnlyWhenTheRegistryIsEmpty pins that a project
// with registered repos reaches no machine at the gate (ImportWorktree relies
// on it: nothing reaches the daemon before confirm_move).
func TestListProjectReposPlacesOnlyWhenTheRegistryIsEmpty(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	project := newAdoptTestProject(t, repo)
	router := &adoptTestDaemonRouter{
		discovered: []map[string]any{{"relative_path": "", "name": "adopt-test"}},
	}
	s := newWorktreeService(t, repo, router)
	_, err := s.listProjectRepos(ctx, project.UserID, project, placeOn("m"))
	require.NoError(t, err)

	repos, err := s.listProjectRepos(ctx, project.UserID, project, func() (machinewake.Target, error) {
		t.Fatal("placement must not run when the registry has repos")
		return machinewake.Target{}, nil
	})
	require.NoError(t, err)
	assert.Len(t, repos, 1)
	assert.Equal(t, 1, router.discoverCalls)
}

// TestListProjectReposNeverBlamesTheProjectForAnUnreachableMachine pins the
// error contract: "no git repository" is said only when the machine looked
// and found none. A machine that could not answer, or that has no project
// folder, says that instead.
func TestListProjectReposNeverBlamesTheProjectForAnUnreachableMachine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		err      error
		code     connect.Code
		contains string
	}{
		{
			name:     "folder missing on the machine",
			err:      toolexec.DaemonReportedFailure("repo.discover", "repo discovery failed: project path: stat /home/workspace/projects/adopt-test: no such file or directory"),
			code:     connect.CodeFailedPrecondition,
			contains: "does not exist on the machine",
		},
		{
			name:     "machine unreachable",
			err:      errors.New("daemon command repo.discover: nats: timeout"),
			code:     connect.CodeUnavailable,
			contains: "could not check /home/workspace/projects/adopt-test for git repositories",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, cleanup := db.SetupTestDB(t)
			defer cleanup()

			project := newAdoptTestProject(t, repo)
			router := &adoptTestDaemonRouter{discoverErr: tc.err}
			s := newWorktreeService(t, repo, router)

			_, err := s.listProjectRepos(context.Background(), project.UserID, project, placeOn("m"))
			require.Error(t, err)
			assert.Equal(t, tc.code, svcerr.Code(err))
			msg := svcerr.ToConnect(err).Message()
			assert.Contains(t, msg, tc.contains)
			assert.NotContains(t, msg, "no git repo")
		})
	}
}
