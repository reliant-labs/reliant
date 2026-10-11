// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// discoverRouter answers worktree.discover_repos and worktree.prune from
// canned data and records each payload and the daemon it went to.
type discoverRouter struct {
	worktreeTestDaemonRouter
	mu       sync.Mutex
	payloads map[string][]byte
	daemons  map[string]string
	entries  []map[string]any
	pruned   []map[string]any
}

func (r *discoverRouter) ResolveDaemonID(context.Context, string) (string, error) {
	return "daemon-default", nil
}

func (r *discoverRouter) SendDaemonCommand(ctx context.Context, userID, cmd string, payload []byte, t int32) ([]byte, error) {
	return r.SendDaemonCommandToDaemon(ctx, userID, "daemon-default", cmd, payload, t)
}

func (r *discoverRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, cmd string, payload []byte, _ int32) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.payloads == nil {
		r.payloads, r.daemons = map[string][]byte{}, map[string]string{}
	}
	r.payloads[cmd], r.daemons[cmd] = payload, daemonID
	switch cmd {
	case "worktree.discover_repos":
		return json.Marshal(map[string]any{"worktrees": r.entries, "worktrees_root": "/home/u/.reliant/worktrees"})
	case "worktree.prune":
		return json.Marshal(map[string]any{"pruned": r.pruned})
	}
	return json.Marshal(map[string]any{})
}

type discoverFixture struct {
	repo      *db.Repo
	router    *discoverRouter
	svc       *WorktreeService
	ctx       context.Context
	userID    string
	projectID string
	repoIDs   []string
}

func newDiscoverTestFixture(t *testing.T, relPaths ...string) *discoverFixture {
	t.Helper()
	repo := db.NewTestRepo(t)
	router := &discoverRouter{}
	f := &discoverFixture{repo: repo, router: router, svc: NewWorktreeService(repo, nil, router),
		userID: uuid.NewString(), projectID: uuid.NewString()}
	f.ctx = context.WithValue(context.Background(), auth.UserIDContextKey, f.userID)
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
		ID: f.projectID, UserID: f.userID, Name: "My Project", Path: "/proj",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	for i, rel := range relPaths {
		id := uuid.NewString()
		f.repoIDs = append(f.repoIDs, id)
		require.NoError(t, repo.CreateRepo(context.Background(), &core.Repo{
			ID: id, ProjectID: f.projectID, Name: "repo" + string(rune('a'+i)), RelativePath: rel,
			CreatedAt: now, UpdatedAt: now,
		}))
	}
	return f
}

func (f *discoverFixture) addRow(t *testing.T, path string, archived bool) {
	t.Helper()
	now := time.Now().UTC()
	wt := &db.Worktree{
		ID: uuid.NewString(), Name: "row-" + uuid.NewString()[:8], Path: path, Branch: "b",
		ProjectID: f.projectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, f.repo.CreateWorktree(context.Background(), wt))
	if archived {
		require.NoError(t, f.repo.ArchiveWorktree(context.Background(), wt.ID))
	}
}

func (f *discoverFixture) discover(t *testing.T) *reliantv1.DiscoverWorktreesResponse {
	t.Helper()
	resp, err := f.svc.DiscoverWorktrees(f.ctx, connect.NewRequest(&reliantv1.DiscoverWorktreesRequest{ProjectId: f.projectID}))
	require.NoError(t, err)
	return resp.Msg
}

func TestDiscoverWorktrees_SendsRepoPathsAndExcludesTrackedRows(t *testing.T) {
	f := newDiscoverTestFixture(t, "a", "b")
	f.addRow(t, "/ws/live", false)
	f.addRow(t, "/ws/archived", true)
	f.router.entries = []map[string]any{
		{"repo_id": f.repoIDs[0], "path": "/outside/wt-a", "name": "wt-a", "branch": "feat", "head": "abc", "locked": true},
	}
	resp := f.discover(t)

	var sent struct {
		Repos []struct {
			RepoID string `json:"repo_id"`
			Path   string `json:"path"`
		} `json:"repos"`
		Roots []string `json:"exclude_roots"`
	}
	require.NoError(t, json.Unmarshal(f.router.payloads["worktree.discover_repos"], &sent))
	require.Len(t, sent.Repos, 2)
	assert.Equal(t, filepath.Join("/proj", "a"), sent.Repos[0].Path)
	assert.Equal(t, f.repoIDs[0], sent.Repos[0].RepoID)
	assert.ElementsMatch(t, []string{"/ws/live", "/ws/archived"}, sent.Roots, "archived rows are excluded too")
	assert.Equal(t, "daemon-default", f.router.daemons["worktree.discover_repos"])

	require.Len(t, resp.Discovered, 1)
	d := resp.Discovered[0]
	assert.Equal(t, "/outside/wt-a", d.Path)
	assert.Equal(t, "repoa", d.RepoName)
	assert.Equal(t, "abc", d.Head)
	assert.True(t, d.Locked)
	assert.True(t, d.MovesOnImport, "multi-repo project")
	assert.Equal(t, "/home/u/.reliant/worktrees/my-project", resp.WorkspacesRoot)
}

func TestDiscoverWorktrees_MovesOnImport(t *testing.T) {
	cases := []struct {
		name  string
		rels  []string
		moves bool
	}{
		{"single repo at project root", []string{""}, false},
		{"single repo dot root", []string{"."}, false},
		{"single repo below root", []string{"app"}, true},
		{"multi repo", []string{"a", "b"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDiscoverTestFixture(t, c.rels...)
			f.router.entries = []map[string]any{{"repo_id": f.repoIDs[0], "path": "/x/wt"}}
			resp := f.discover(t)
			require.Len(t, resp.Discovered, 1)
			assert.Equal(t, c.moves, resp.Discovered[0].MovesOnImport)
		})
	}
	t.Run("single repo at root sends project path", func(t *testing.T) {
		f := newDiscoverTestFixture(t, "")
		f.discover(t)
		var sent struct {
			Repos []struct{ Path string } `json:"repos"`
		}
		require.NoError(t, json.Unmarshal(f.router.payloads["worktree.discover_repos"], &sent))
		assert.Equal(t, "/proj", sent.Repos[0].Path)
	})
}

func TestDiscoverWorktrees_SplitsStale(t *testing.T) {
	f := newDiscoverTestFixture(t, "a", "b")
	f.router.entries = []map[string]any{
		{"repo_id": f.repoIDs[0], "path": "/live", "name": "live"},
		{"repo_id": f.repoIDs[1], "path": "/gone", "name": "gone", "prunable": true, "prunable_reason": "gitdir file points to non-existent location"},
	}
	resp := f.discover(t)
	require.Len(t, resp.Discovered, 1)
	assert.Equal(t, "/live", resp.Discovered[0].Path)
	require.Len(t, resp.Stale, 1)
	assert.Equal(t, "/gone", resp.Stale[0].Path)
	assert.Equal(t, f.repoIDs[1], resp.Stale[0].RepoId)
	assert.Equal(t, "repob", resp.Stale[0].RepoName)
	assert.Contains(t, resp.Stale[0].Reason, "non-existent")
}

func TestDiscoverWorktrees_RejectsAnotherUsersProject(t *testing.T) {
	f := newDiscoverTestFixture(t, "")
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, uuid.NewString())
	_, err := f.svc.DiscoverWorktrees(ctx, connect.NewRequest(&reliantv1.DiscoverWorktreesRequest{ProjectId: f.projectID}))
	require.Error(t, err)
	assert.Nil(t, f.router.payloads)
}

func TestPruneWorktrees_SendsReposAndReturnsPruned(t *testing.T) {
	f := newDiscoverTestFixture(t, "a", "b")
	f.router.pruned = []map[string]any{
		{"repo_id": f.repoIDs[1], "path": "/gone", "prunable": true, "prunable_reason": "missing"},
	}
	resp, err := f.svc.PruneWorktrees(f.ctx, connect.NewRequest(&reliantv1.PruneWorktreesRequest{ProjectId: f.projectID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Pruned, 1)
	assert.Equal(t, "/gone", resp.Msg.Pruned[0].Path)
	assert.Equal(t, "repob", resp.Msg.Pruned[0].RepoName)
	assert.Equal(t, "missing", resp.Msg.Pruned[0].Reason)

	var sent struct {
		Repos []struct {
			RepoID string `json:"repo_id"`
			Path   string `json:"path"`
		} `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(f.router.payloads["worktree.prune"], &sent))
	require.Len(t, sent.Repos, 2)
	assert.Equal(t, filepath.Join("/proj", "b"), sent.Repos[1].Path)

	_, err = f.svc.PruneWorktrees(f.ctx, connect.NewRequest(&reliantv1.PruneWorktreesRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestDiscoverWorktrees_BackgroundNeverWakesAnAsleepMachine(t *testing.T) {
	for _, background := range []bool{true, false} {
		name := "foreground wakes"
		if background {
			name = "background does not wake"
		}
		t.Run(name, func(t *testing.T) {
			f := newAsleepFixture(t, placementDefaultDaemon)
			_, err := f.wt.DiscoverWorktrees(f.ctx, connect.NewRequest(&reliantv1.DiscoverWorktreesRequest{
				ProjectId: f.projectID, Background: background,
			}))
			require.Error(t, err)
			assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
			if background {
				assert.Empty(t, f.router.wakeCalls(), "a background poll must not start the machine")
				assert.False(t, isMachineWaking(err))
			} else {
				require.NotEmpty(t, f.router.wakeCalls())
				assert.True(t, isMachineWaking(err))
			}
		})
	}
}

// ---- import ----

func TestImportWorktree_MultiRepoNeedsConfirmMove(t *testing.T) {
	f := newDiscoverTestFixture(t, "a", "b")
	_, err := f.svc.ImportWorktree(f.ctx, connect.NewRequest(&reliantv1.ImportWorktreeRequest{
		ProjectId: f.projectID, RepoId: f.repoIDs[0], Path: "/x/wt",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "moves")
	assert.Nil(t, f.router.payloads, "nothing reaches the daemon before the user confirms")
}

func TestImportWorktree_RefusesPathsTheDaemonDoesNotReport(t *testing.T) {
	f := newDiscoverTestFixture(t, "")
	f.router.entries = []map[string]any{{"repo_id": f.repoIDs[0], "path": "/x/real", "branch": "b"}}
	_, err := f.svc.ImportWorktree(f.ctx, connect.NewRequest(&reliantv1.ImportWorktreeRequest{
		ProjectId: f.projectID, Path: "/etc",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	rows, _ := f.repo.ListWorktrees(context.Background(), db.WorktreeFilters{ProjectID: &f.projectID, IncludeArchived: true})
	assert.Empty(t, rows)
}

func TestImportWorktree_DetachedHeadIsRefused(t *testing.T) {
	f := newDiscoverTestFixture(t, "")
	f.router.entries = []map[string]any{{"repo_id": f.repoIDs[0], "path": "/x/det", "head": "abc", "detached": true}}
	_, err := f.svc.ImportWorktree(f.ctx, connect.NewRequest(&reliantv1.ImportWorktreeRequest{
		ProjectId: f.projectID, Path: "/x/det",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "detached")
}

func TestImportWorktree_SingleRepoRegistersInPlace(t *testing.T) {
	f := newDiscoverTestFixture(t, "")
	f.router.entries = []map[string]any{{"repo_id": f.repoIDs[0], "path": "/x/wt", "name": "wt", "branch": "feat/y"}}
	resp, err := f.svc.ImportWorktree(f.ctx, connect.NewRequest(&reliantv1.ImportWorktreeRequest{
		ProjectId: f.projectID, Path: "/x/wt",
	}))
	require.NoError(t, err)
	got, err := f.repo.GetWorktree(context.Background(), resp.Msg.Worktree.Id)
	require.NoError(t, err)
	assert.Equal(t, "/x/wt", got.Path)
	assert.Equal(t, "feat/y", got.Branch)
	assert.Equal(t, "wt", got.Name)
	assert.Equal(t, int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), got.Status)
	assert.Empty(t, f.router.payloads["worktree.adopt_move"])

	// Same name again: AlreadyExists, not Internal.
	f.router.entries = []map[string]any{{"repo_id": f.repoIDs[0], "path": "/x/other", "name": "wt", "branch": "feat/z"}}
	_, err = f.svc.ImportWorktree(f.ctx, connect.NewRequest(&reliantv1.ImportWorktreeRequest{
		ProjectId: f.projectID, Path: "/x/other",
	}))
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

	// A branch held by another row: FailedPrecondition.
	f.router.entries = []map[string]any{{"repo_id": f.repoIDs[0], "path": "/x/third", "name": "third", "branch": "feat/y"}}
	_, err = f.svc.ImportWorktree(f.ctx, connect.NewRequest(&reliantv1.ImportWorktreeRequest{
		ProjectId: f.projectID, Path: "/x/third",
	}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
