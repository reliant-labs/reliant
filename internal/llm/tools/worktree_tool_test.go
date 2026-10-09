// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// fakeRunMachine is the run's machine as the worktree tool sees it: it answers
// the daemon's worktree commands the way the daemon does for a workspace and
// records which machine each one reached.
type fakeRunMachine struct {
	runDaemon string
	home      string
	// failRepo, when set, is a project_path suffix whose create fails.
	failRepo string

	mu   sync.Mutex
	sent []sentWorktreeCommand
}

type sentWorktreeCommand struct {
	daemonID    string
	commandType string
	payload     map[string]any
}

func (m *fakeRunMachine) DaemonID(context.Context, string) (string, error) {
	return m.runDaemon, nil
}

func (m *fakeRunMachine) Send(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	var req map[string]any
	_ = json.Unmarshal(payload, &req)
	m.mu.Lock()
	m.sent = append(m.sent, sentWorktreeCommand{daemonID: daemonID, commandType: commandType, payload: req})
	m.mu.Unlock()

	switch commandType {
	case "worktree.create":
		if pp, _ := req["project_path"].(string); m.failRepo != "" && strings.HasSuffix(pp, m.failRepo) {
			return json.Marshal(map[string]any{"error": "fatal: invalid reference"})
		}
		path := filepath.Join(m.home, ".reliant", "worktrees", req["workspace_id"].(string))
		if sub, _ := req["sub_path"].(string); sub != "" {
			path = filepath.Join(path, sub)
		}
		return json.Marshal(map[string]any{"success": true, "worktree_path": path, "base_branch": "main"})
	}
	return json.Marshal(map[string]any{})
}

// to returns the commands of commandType, in send order.
func (m *fakeRunMachine) to(commandType string) []sentWorktreeCommand {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []sentWorktreeCommand
	for _, c := range m.sent {
		if c.commandType == commandType {
			out = append(out, c)
		}
	}
	return out
}

type worktreeToolFixture struct {
	repo    db.Repository
	machine *fakeRunMachine
	project *db.Project
	chatID  string
	repos   []*core.Repo
	rc      *rctx.ToolContext
}

const worktreeToolHome = "/home/machine-b"

func newWorktreeToolFixture(t *testing.T, repos ...*core.Repo) *worktreeToolFixture {
	t.Helper()
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	chatID := newCaller(t, repo)
	chat, err := repo.GetChat(context.Background(), chatID)
	require.NoError(t, err)
	project, err := repo.GetProject(context.Background(), chat.ProjectID)
	require.NoError(t, err)
	// The project lives on the machine; on the worker this path is nothing.
	project.Path = worktreeToolHome + "/project"
	if len(repos) == 0 {
		repos = []*core.Repo{{ID: uuid.NewString(), ProjectID: project.ID, Name: "project", RelativePath: ""}}
	}

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, runTestUser)
	rc := rctx.NewToolContext(ctx, chatID, chatID, project, nil)
	rc.Repos = repos
	return &worktreeToolFixture{
		repo:    repo,
		machine: &fakeRunMachine{runDaemon: "daemon-b", home: worktreeToolHome},
		project: project,
		chatID:  chatID,
		repos:   repos,
		rc:      rc,
	}
}

func (f *worktreeToolFixture) call(t *testing.T, params WorktreeParams) ToolResponse {
	t.Helper()
	return callRunTool(t, NewWorktreeTool(f.repo, f.machine), f.rc, WorktreeToolName, params)
}

func (f *worktreeToolFixture) row(t *testing.T, name string) *db.Worktree {
	t.Helper()
	rows, err := f.repo.ListWorktrees(context.Background(), db.WorktreeFilters{ProjectID: &f.project.ID, IncludeArchived: true})
	require.NoError(t, err)
	for _, row := range rows {
		if row.Name == name {
			return row
		}
	}
	return nil
}

// registerMain records the project's main checkout row.
func (f *worktreeToolFixture) registerMain(t *testing.T, name string) *db.Worktree {
	t.Helper()
	return f.insert(t, name, 1, true)
}

// register records a worktree row, for tests that need one to already exist.
func (f *worktreeToolFixture) register(t *testing.T, name string, status int32) *db.Worktree {
	t.Helper()
	return f.insert(t, name, status, false)
}

func (f *worktreeToolFixture) insert(t *testing.T, name string, status int32, isMain bool) *db.Worktree {
	t.Helper()
	now := time.Now().UTC()
	owner := "daemon-b"
	wt := &db.Worktree{
		ID: uuid.NewString(), Name: name, Path: worktreeToolHome + "/.reliant/worktrees/proj/" + name + "-abc12345", Branch: "b-" + name,
		ProjectID: f.project.ID, DaemonID: &owner, Status: status, IsMain: isMain, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, f.repo.CreateWorktree(context.Background(), wt))
	return wt
}

// A create makes the UI's workspace: one checkout per repository under a
// <project>/<name>-<id> directory, a row that owns the path, recorded against
// the run's machine and chat, with the sidebar told to refetch.
func TestWorktreeTool_Create_MakesAWorkspaceOnTheRunsMachine(t *testing.T) {
	f := newWorktreeToolFixture(t,
		&core.Repo{ID: uuid.NewString(), Name: "api", RelativePath: "api"},
		&core.Repo{ID: uuid.NewString(), Name: "web", RelativePath: "web"},
	)

	resp := f.call(t, WorktreeParams{Action: "create", Name: "feature-auth", BaseBranch: "main", CopyFiles: []string{".env"}})
	require.False(t, resp.IsError, resp.Content)

	for _, cmd := range f.machine.sent {
		assert.Equal(t, "daemon-b", cmd.daemonID, "%s ran on the wrong machine", cmd.commandType)
	}
	creates := f.machine.to("worktree.create")
	require.Len(t, creates, 2, "one checkout per repository")
	workspaceID, _ := creates[0].payload["workspace_id"].(string)
	assert.Regexp(t, `^[a-z0-9-]+/feature-auth-[0-9a-f]{8}$`, workspaceID, "the UI's <project>/<name>-<id> layout")
	assert.Equal(t, workspaceID, creates[1].payload["workspace_id"], "both repos land in one workspace")
	assert.NotContains(t, workspaceID, "repo", "never the legacy <repo_id>/<name> layout")
	for _, c := range creates {
		assert.Equal(t, f.project.Path+"/"+c.payload["sub_path"].(string), c.payload["project_path"], "branched from the project's main checkout")
		assert.NotEmpty(t, c.payload["worktree_id"], "the reclaim lock names the row")
	}

	root := filepath.Join(worktreeToolHome, ".reliant", "worktrees", workspaceID)
	copies := f.machine.to("worktree.copy_paths")
	require.Len(t, copies, 1)
	assert.Equal(t, f.project.Path, copies[0].payload["source_root"])
	assert.Equal(t, root, copies[0].payload["dest_root"])

	row := f.row(t, "feature-auth")
	require.NotNil(t, row)
	assert.Equal(t, root, row.Path, "the row's path is the workspace root")
	assert.Equal(t, int32(1), row.Status, "settled ACTIVE")
	require.NotNil(t, row.DaemonID)
	assert.Equal(t, "daemon-b", *row.DaemonID)
	require.NotNil(t, row.ChatID)
	assert.Equal(t, f.chatID, *row.ChatID)
	assert.Contains(t, row.Branch, "worktree/feature-auth-")

	var meta WorktreeResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	assert.Equal(t, row.ID, meta.WorktreeID)
	assert.Equal(t, root, meta.Path)
	for _, key := range []string{"id", "name", "path", "branch", "base_branch"} {
		assert.NotEmpty(t, meta.StoredInCEL[key], "workflows read worktree_data.%s", key)
	}
	assert.Contains(t, resp.Content, filepath.Join(root, "api"))
	assert.Contains(t, resp.Content, filepath.Join(root, "web"))
	assert.Contains(t, resp.Content, `spawn(worktree="feature-auth")`)
}

// A single repo at the project root puts its checkout at the workspace root.
func TestWorktreeTool_Create_SingleRepoCheckoutIsTheRoot(t *testing.T) {
	f := newWorktreeToolFixture(t)

	resp := f.call(t, WorktreeParams{Action: "create", Name: "solo"})
	require.False(t, resp.IsError, resp.Content)

	row := f.row(t, "solo")
	require.NotNil(t, row)
	assert.Contains(t, resp.Content, "project: "+row.Path)
}

// While the chat is inside a worktree, the project path is still the main
// checkout and copy_files come from the chat's worktree.
func TestWorktreeTool_Create_FromInsideAWorktree(t *testing.T) {
	f := newWorktreeToolFixture(t)
	inside := f.register(t, "current", 1)
	f.rc.Worktree = &rctx.WorktreeInfo{ID: inside.ID, Path: inside.Path, DaemonID: "daemon-b"}

	resp := f.call(t, WorktreeParams{Action: "create", Name: "next", CopyFiles: []string{".env"}})
	require.False(t, resp.IsError, resp.Content)

	creates := f.machine.to("worktree.create")
	require.Len(t, creates, 1)
	assert.Equal(t, f.project.Path, creates[0].payload["project_path"], "never the chat's own worktree")
	copies := f.machine.to("worktree.copy_paths")
	require.Len(t, copies, 1)
	assert.Equal(t, inside.Path, copies[0].payload["source_root"], "this workspace's .env travels")
}

// A name held by a live or creating worktree is refused with a pointer, and
// nothing reaches the machine. An archived name is refused too (the database
// keeps one row per name), but says so.
func TestWorktreeTool_Create_RefusesAHeldName(t *testing.T) {
	f := newWorktreeToolFixture(t)
	f.register(t, "live", 1)
	f.register(t, "pending", 5)

	for name, want := range map[string]string{
		"live":    "already exists",
		"pending": "still being created",
	} {
		resp := f.call(t, WorktreeParams{Action: "create", Name: name})
		require.True(t, resp.IsError, name)
		assert.Contains(t, resp.Content, want, name)
		assert.Contains(t, resp.Content, "pick another name", name)
	}
	assert.Empty(t, f.machine.sent, "nothing may reach the machine")
}

// An archived worktree releases its name: the same name creates again, the
// archived row is left as it was, and name lookups resolve the live one.
func TestWorktreeTool_Create_ReusesAnArchivedName(t *testing.T) {
	f := newWorktreeToolFixture(t)
	old := f.register(t, "feat", 1)
	require.NoError(t, f.repo.ArchiveWorktree(context.Background(), old.ID))

	resp := f.call(t, WorktreeParams{Action: "create", Name: "feat"})
	require.False(t, resp.IsError, resp.Content)

	live, err := f.repo.GetLiveWorktreeByName(context.Background(), f.project.ID, "feat")
	require.NoError(t, err)
	require.NotNil(t, live)
	assert.NotEqual(t, old.ID, live.ID)
	assert.Equal(t, int32(1), live.Status)

	kept, err := f.repo.GetWorktree(context.Background(), old.ID)
	require.NoError(t, err)
	assert.NotNil(t, kept.DeletedAt, "the archived row stays archived")

	got := f.call(t, WorktreeParams{Action: "get", Name: "feat"})
	require.False(t, got.IsError, got.Content)
	assert.Contains(t, got.Content, live.ID)

	del := f.call(t, WorktreeParams{Action: "delete", Name: "feat"})
	require.False(t, del.IsError, del.Content)
	gone, err := f.repo.GetWorktree(context.Background(), live.ID)
	require.NoError(t, err)
	assert.NotNil(t, gone.DeletedAt)
	assert.Empty(t, f.machine.to("worktree.force_cleanup"))
}

// A FAILED attempt left nothing behind, so creating again replaces it.
func TestWorktreeTool_Create_RetriesAFailedName(t *testing.T) {
	f := newWorktreeToolFixture(t)
	failed := f.register(t, "retry", 6)

	resp := f.call(t, WorktreeParams{Action: "create", Name: "retry", Branch: failed.Branch})
	require.False(t, resp.IsError, resp.Content)

	old, err := f.repo.GetWorktree(context.Background(), failed.ID)
	require.NoError(t, err)
	assert.NotNil(t, old.DeletedAt, "the failed row is archived")
	live, err := f.repo.GetLiveWorktreeByName(context.Background(), f.project.ID, "retry")
	require.NoError(t, err)
	require.NotNil(t, live)
	assert.NotEqual(t, failed.ID, live.ID)
}

// Reusing a name must never reset the branch of an archived worktree whose
// work may be unmerged, not even under force (which would delete it).
func TestWorktreeTool_Create_NeverTakesAnArchivedWorktreesBranch(t *testing.T) {
	f := newWorktreeToolFixture(t)
	old := f.register(t, "feat", 1)
	require.NoError(t, f.repo.ArchiveWorktree(context.Background(), old.ID))

	for _, force := range []bool{false, true} {
		resp := f.call(t, WorktreeParams{Action: "create", Name: "feat", Branch: old.Branch, Force: force})
		require.True(t, resp.IsError, "force=%v", force)
		assert.Contains(t, resp.Content, "pass a different branch")
		assert.Contains(t, resp.Content, old.Branch)
	}
	assert.Empty(t, f.machine.sent, "nothing may reach the machine")

	resp := f.call(t, WorktreeParams{Action: "create", Name: "feat", Branch: "other"})
	require.False(t, resp.IsError, resp.Content)
}

// When a repo fails, the whole workspace is rolled back and the row is kept as
// FAILED with the reason in the tool error.
func TestWorktreeTool_Create_FailureIsAllOrNothing(t *testing.T) {
	f := newWorktreeToolFixture(t,
		&core.Repo{ID: uuid.NewString(), Name: "api", RelativePath: "api"},
		&core.Repo{ID: uuid.NewString(), Name: "web", RelativePath: "web"},
	)
	f.machine.failRepo = "/web"

	resp := f.call(t, WorktreeParams{Action: "create", Name: "doomed", BaseBranch: "nope"})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "nope", "the reason names the bad base branch")
	assert.Contains(t, resp.Content, "failed worktree named 'doomed'")

	row := f.row(t, "doomed")
	require.NotNil(t, row)
	assert.Equal(t, int32(6), row.Status, "FAILED, visible in the UI")
	assert.Empty(t, row.Path, "a failed row carries no path")
	assert.NotEmpty(t, f.machine.to("worktree.delete_directory"), "the api checkout that succeeded is torn down")
}

func TestWorktreeTool_CreateForce_CleansTheStaleBranchPerRepo(t *testing.T) {
	f := newWorktreeToolFixture(t)

	resp := f.call(t, WorktreeParams{Action: "create", Name: "again", Branch: "feat/again", Force: true})
	require.False(t, resp.IsError, resp.Content)

	cleanups := f.machine.to("worktree.force_cleanup")
	require.Len(t, cleanups, 1)
	assert.Equal(t, "feat/again", cleanups[0].payload["branch"])
	assert.Equal(t, "", cleanups[0].payload["worktree_path"], "the workspace dir is fresh; only the branch is stale")
}

// force keeps its permission prompt, as does delete.
func TestWorktreeTool_RequiresPermissionForForceAndDelete(t *testing.T) {
	tool := &worktreeTool{}
	for _, tc := range []struct {
		p    WorktreeParams
		want bool
	}{
		{WorktreeParams{Action: "create"}, false},
		{WorktreeParams{Action: "create", Force: true}, true},
		{WorktreeParams{Action: "delete"}, true},
		{WorktreeParams{Action: "list"}, false},
	} {
		got, err := tool.RequiresPermission(tc.p)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got, "%+v", tc.p)
	}
}

// list and get report every status, including the in-flight and failed ones,
// with the workspace's path, branch and owner.
func TestWorktreeTool_ListAndGet_ShowEveryStatus(t *testing.T) {
	f := newWorktreeToolFixture(t)
	f.register(t, "live", 1)
	f.register(t, "pending", 5)
	failed := f.register(t, "broken", 6)
	failed.Path = ""
	require.NoError(t, f.repo.UpdateWorktree(context.Background(), failed))
	archived := f.register(t, "gone", 1)
	require.NoError(t, f.repo.ArchiveWorktree(context.Background(), archived.ID))

	list := f.call(t, WorktreeParams{Action: "list"})
	require.False(t, list.IsError, list.Content)
	var meta WorktreeResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(list.Metadata), &meta))
	got := map[string]WorktreeStatus{}
	for _, wt := range meta.Worktrees {
		got[wt.Name] = wt.Status
	}
	assert.Equal(t, map[string]WorktreeStatus{
		"live": WorktreeStatusActive, "pending": WorktreeStatusCreating, "broken": WorktreeStatusFailed,
	}, got, "archived rows are not listed")
	assert.Contains(t, list.Content, "Owner machine (daemon): daemon-b")

	one := f.call(t, WorktreeParams{Action: "get", Name: "live"})
	require.False(t, one.IsError, one.Content)
	assert.Contains(t, one.Content, "Status: active")
	assert.Contains(t, one.Content, "Branch: b-live")
	assert.Contains(t, one.Content, "project: "+worktreeToolHome+"/.reliant/worktrees/proj/live-abc12345")
	require.NoError(t, json.Unmarshal([]byte(one.Metadata), &meta))
	assert.Equal(t, worktreeToolHome+"/.reliant/worktrees/proj/live-abc12345", meta.StoredInCEL["path"])

	missing := f.call(t, WorktreeParams{Action: "get", Name: "nope"})
	require.True(t, missing.IsError)
}

// Delete archives the row and never touches the directory: removing it is the
// reclaim sweep's call, made only when it is clean, pushed and unused.
func TestWorktreeTool_Delete_ArchivesAndLeavesTheDirectoryToReclaim(t *testing.T) {
	f := newWorktreeToolFixture(t)
	wt := f.register(t, "feature", 1)

	resp := f.call(t, WorktreeParams{Action: "delete", Name: "feature"})
	require.False(t, resp.IsError, resp.Content)

	got, err := f.repo.GetWorktree(context.Background(), wt.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.DeletedAt, "archived")
	assert.Empty(t, f.machine.sent, "no directory is removed directly")
	assert.Contains(t, resp.Content, "clean, pushed and unused")
}

func TestWorktreeTool_Delete_Refusals(t *testing.T) {
	f := newWorktreeToolFixture(t)

	t.Run("the chat's own worktree", func(t *testing.T) {
		own := f.register(t, "own", 1)
		f.rc.Worktree = &rctx.WorktreeInfo{ID: own.ID, Path: own.Path}
		defer func() { f.rc.Worktree = nil }()
		resp := f.call(t, WorktreeParams{Action: "delete", Name: "own"})
		require.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "this chat is working in")
		got, err := f.repo.GetWorktree(context.Background(), own.ID)
		require.NoError(t, err)
		assert.Nil(t, got.DeletedAt)
	})

	t.Run("the main checkout", func(t *testing.T) {
		// is_main is written by the insert only, so the row is created with it.
		main := f.registerMain(t, "main-ish")
		got, err := f.repo.GetWorktree(context.Background(), main.ID)
		require.NoError(t, err)
		require.True(t, got.IsMain)
		resp := f.call(t, WorktreeParams{Action: "delete", Name: "main-ish"})
		require.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "main checkout")
	})

	t.Run("a worktree that does not exist", func(t *testing.T) {
		resp := f.call(t, WorktreeParams{Action: "delete", Name: "ghost"})
		require.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "not found")
	})
}

// The name becomes a directory prefix on the user's machine, so it may not
// climb out of ~/.reliant/worktrees.
func TestWorktreeTool_RefusesANameThatIsAPath(t *testing.T) {
	f := newWorktreeToolFixture(t)

	for _, name := range []string{"..", "../..", "a/b", `a\b`} {
		for _, action := range []string{"create", "delete"} {
			resp := f.call(t, WorktreeParams{Action: action, Name: name, Force: true})
			require.True(t, resp.IsError, "%s %q", action, name)
			assert.Contains(t, resp.Content, "invalid worktree name")
		}
	}
	assert.Empty(t, f.machine.sent, "nothing may reach the machine")
}

// With no way to reach a machine the tool says so; it never falls back to the
// local disk, and records nothing.
func TestWorktreeTool_WithoutAMachine_DoesNothingLocally(t *testing.T) {
	f := newWorktreeToolFixture(t)

	resp := callRunTool(t, NewWorktreeTool(f.repo, nil), f.rc, WorktreeToolName, WorktreeParams{Action: "create", Name: "feature-auth"})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "no machine can be reached")
	assert.Nil(t, f.row(t, "feature-auth"))
}

// A chat with no project cannot create a worktree, and the tool does not guess
// a path from the working directory.
func TestWorktreeTool_WithoutAProject_Refuses(t *testing.T) {
	f := newWorktreeToolFixture(t)
	f.rc.Project = nil

	resp := f.call(t, WorktreeParams{Action: "create", Name: "x"})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "no project")
	assert.Empty(t, f.machine.sent)
}

func TestWorktreeTool_DescriptionTeachesTheWorkspaceWorkflow(t *testing.T) {
	desc := (&worktreeTool{}).Description()
	for _, want := range []string{"ONLY way", "git worktree add", "spawn(worktree=", "~/.reliant/worktrees/", "Reliant sidebar"} {
		assert.Contains(t, desc, want)
	}
	assert.NotContains(t, desc, "repo_id")
}
