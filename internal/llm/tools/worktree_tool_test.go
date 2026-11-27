// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/chatupdates"
	"github.com/reliant-labs/reliant/internal/store"

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
			// What the daemon answers for a base branch that does not
			// exist: git's text for the logs, and the diagnosis.
			base, _ := req["base_branch"].(string)
			return json.Marshal(map[string]any{
				"error":        "fatal: invalid reference: " + base,
				"error_code":   "base_not_found",
				"error_detail": base,
			})
		}
		path := filepath.Join(m.home, ".reliant", "worktrees", req["workspace_id"].(string))
		if sub, _ := req["sub_path"].(string); sub != "" {
			path = filepath.Join(path, sub)
		}
		// The daemon answers the base it used: the requested one, else the
		// repository's default branch.
		base, _ := req["base_branch"].(string)
		if base == "" {
			base = "main"
		}
		return json.Marshal(map[string]any{"success": true, "worktree_path": path, "base_branch": base})
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
	repo    *db.Repo
	machine *fakeRunMachine
	settler *fakeSettler
	project *db.Project
	chatID  string
	repos   []*core.Repo
	rc      *rctx.ToolContext
}

const worktreeToolHome = "/home/machine-b"

// fakeSettler records the candidates handed to Settle.
type fakeSettler struct {
	settled chan *core.ReclaimCandidate
}

func (s *fakeSettler) Settle(_ context.Context, _ string, cand *core.ReclaimCandidate) {
	s.settled <- cand
}

func newWorktreeToolFixture(t *testing.T, repos ...*core.Repo) *worktreeToolFixture {
	t.Helper()
	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	chatID := newCaller(t, repo)
	chat, err := store.GetChat(context.Background(), repo.ORM(), chatID)
	require.NoError(t, err)
	project, err := repo.GetProject(context.Background(), chat.ProjectID)
	require.NoError(t, err)
	// The project lives on the machine; on the worker this path is nothing.
	// The tool knows it only as the run machine's checkout (rc.Project).
	project.Path = worktreeToolHome + "/project"
	if len(repos) == 0 {
		repos = []*core.Repo{{ID: uuid.NewString(), ProjectID: project.ID, Name: "project", RelativePath: ""}}
	}

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, runTestUser)
	// The dispatcher resolved the run machine's checkout into the request.
	rc := rctx.NewToolContext(ctx, chatID, chatID, &rctx.Project{ID: project.ID, Name: project.Name, Checkout: project.Path}, nil)
	rc.Repos = repos
	return &worktreeToolFixture{
		repo:    repo,
		machine: &fakeRunMachine{runDaemon: "daemon-b", home: worktreeToolHome},
		settler: &fakeSettler{settled: make(chan *core.ReclaimCandidate, 4)},
		project: project,
		chatID:  chatID,
		repos:   repos,
		rc:      rc,
	}
}

func (f *worktreeToolFixture) call(t *testing.T, params WorktreeParams) ToolResponse {
	t.Helper()
	return callRunTool(t, NewWorktreeTool(f.repo, f.repo, store.NewChatExtStore(f.repo.ORM()), f.machine, f.settler, chatupdates.NewFromORM(f.repo.ORM(), f.repo, nil)), f.rc, WorktreeToolName, params)
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
	// The agent is told what to change, so it can retry with a real branch.
	assert.Contains(t, resp.Content, "base branch 'nope' doesn't exist in web", "the reason names the bad base branch")
	assert.Contains(t, resp.Content, "failed worktree named 'doomed'")
	assert.Contains(t, resp.Content, "pass base_branch_repo", "a multi-repo project is told how to scope the base")

	row := f.row(t, "doomed")
	require.NotNil(t, row)
	assert.Equal(t, int32(6), row.Status, "FAILED, visible in the UI")
	assert.Empty(t, row.Path, "a failed row carries no path")
	assert.NotEmpty(t, f.machine.to("worktree.delete_directory"), "the api checkout that succeeded is torn down")
}

// base_branch_repo starts one repository from base_branch (a branch only it
// has, such as a pull request's) and every other from its default branch.
func TestWorktreeTool_Create_BaseBranchRepoScopesTheBase(t *testing.T) {
	api := &core.Repo{ID: uuid.NewString(), Name: "api", RelativePath: "api"}
	web := &core.Repo{ID: uuid.NewString(), Name: "web", RelativePath: "services/web"}
	for _, named := range []string{"web", "services/web"} {
		t.Run(named, func(t *testing.T) {
			f := newWorktreeToolFixture(t, api, web)
			resp := f.call(t, WorktreeParams{Action: "create", Name: "pr-head", BaseBranch: "fix/pr", BaseBranchRepo: named})
			require.False(t, resp.IsError, resp.Content)

			bases := map[string]any{}
			for _, c := range f.machine.to("worktree.create") {
				bases[c.payload["sub_path"].(string)] = c.payload["base_branch"]
			}
			assert.Equal(t, map[string]any{"api": "", "services/web": "fix/pr"}, bases,
				"only the named repository is sent the base; the other auto-detects its default")

			row := f.row(t, "pr-head")
			require.NotNil(t, row)
			assert.Equal(t, int32(1), row.Status, "settled ACTIVE")
			assert.Equal(t, "fix/pr", row.BaseBranch, "the displayed base is the one asked for, not a sibling's default")
			assert.Equal(t, map[string]string{api.ID: "main", web.ID: "fix/pr"}, row.BaseBranches)
		})
	}
}

// A base_branch_repo that names no repository, or comes without base_branch,
// is refused before anything is recorded or reaches the machine.
func TestWorktreeTool_Create_BaseBranchRepoIsValidated(t *testing.T) {
	f := newWorktreeToolFixture(t,
		&core.Repo{ID: uuid.NewString(), Name: "api", RelativePath: "api"},
		&core.Repo{ID: uuid.NewString(), Name: "web", RelativePath: "web"},
	)
	for name, tc := range map[string]struct {
		params WorktreeParams
		want   string
	}{
		"unknown": {WorktreeParams{Action: "create", Name: "x1", BaseBranch: "b", BaseBranchRepo: "nope"}, `"nope" is not a repository of this project (its repositories: api, web)`},
		"no-base": {WorktreeParams{Action: "create", Name: "x2", BaseBranchRepo: "web"}, "base_branch_repo needs base_branch"},
	} {
		resp := f.call(t, tc.params)
		require.True(t, resp.IsError, name)
		assert.Contains(t, resp.Content, tc.want, name)
		assert.Nil(t, f.row(t, tc.params.Name), "%s: no row is recorded", name)
	}
	assert.Empty(t, f.machine.sent, "nothing may reach the machine")
}

// The tool forwards force into each create, not a separate cleanup that could
// run before base/head validation. The real tool -> Finish -> daemon -> git
// stale-branch reset and refusal behavior is pinned by
// daemonruntime.TestWorktreeToolForceResetsStaleBranchesAfterValidation and
// TestWorktreeFinishDriftPreservesExistingWorktreeAndBranch.
func TestWorktreeTool_CreateForce_DelegatesResetInsideEachCreate(t *testing.T) {
	f := newWorktreeToolFixture(t,
		&core.Repo{ID: uuid.NewString(), Name: "api", RelativePath: "api"},
		&core.Repo{ID: uuid.NewString(), Name: "web", RelativePath: "web"},
	)

	resp := f.call(t, WorktreeParams{Action: "create", Name: "again", Branch: "feat/again", BaseBranch: "main", Force: true})
	require.False(t, resp.IsError, resp.Content)

	creates := f.machine.to("worktree.create")
	require.Len(t, creates, 2)
	for _, create := range creates {
		assert.Equal(t, true, create.payload["force"], "daemon owns reset after validation")
		reuse, _ := create.payload["reuse_branch"].(bool) // false is omitted by the JSON protocol
		assert.False(t, reuse, "adoption must not bypass the requested reset")
		assert.Equal(t, "feat/again", create.payload["branch"])
		assert.Equal(t, "main", create.payload["base_branch"])
	}
	assert.Empty(t, f.machine.to("worktree.force_cleanup"), "must not destructively clean before create validates its base")
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
	assert.Contains(t, resp.Content, "pushed or already merged")
}

// Delete asks for one immediate settle of the archived row, as the UI's archive
// does, on the machine's project path, and not for a refused delete.
func TestWorktreeTool_Delete_AsksForAnImmediateSettle(t *testing.T) {
	f := newWorktreeToolFixture(t)
	wt := f.register(t, "feature", 1)

	resp := f.call(t, WorktreeParams{Action: "delete", Name: "feature"})
	require.False(t, resp.IsError, resp.Content)

	select {
	case cand := <-f.settler.settled:
		assert.Equal(t, wt.ID, cand.Worktree.ID)
		assert.NotNil(t, cand.Worktree.DeletedAt, "it settles the row as archived, so the daemon is given its fence")
		assert.Equal(t, runTestUser, cand.OwnerUserID)
		assert.Equal(t, f.project.Path, cand.ProjectPath)
	case <-time.After(30 * time.Second):
		t.Fatal("delete never asked for a settle")
	}

	own := f.register(t, "own", 1)
	f.rc.Worktree = &rctx.WorktreeInfo{ID: own.ID, Path: own.Path}
	defer func() { f.rc.Worktree = nil }()
	require.True(t, f.call(t, WorktreeParams{Action: "delete", Name: "own"}).IsError)
	select {
	case cand := <-f.settler.settled:
		t.Fatalf("a refused delete must not settle anything: %s", cand.Worktree.ID)
	case <-time.After(200 * time.Millisecond):
	}
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

// bindSubAgent spawns a sub-agent thread into wt, its runs created in order
// and the thread bound to each new run, as spawn(worktree=...) binds it.
func (f *worktreeToolFixture) bindSubAgent(t *testing.T, wt *db.Worktree, runs ...db.WorkflowStatus) (threadID string, runIDs []string) {
	t.Helper()
	ctx := context.Background()
	threadID = uuid.NewString()
	parent := f.chatID
	at := time.Now().Add(-time.Hour)
	_, err := f.repo.CreateThread(ctx, &db.Thread{
		ID: threadID, ChatID: f.chatID, ParentThreadID: &parent, WorktreeID: &wt.ID,
		Origin: db.ThreadOriginSpawn, Status: db.ThreadStatusRunning, CreatedAt: at,
	})
	require.NoError(t, err)
	for i, status := range runs {
		id := uuid.NewString()
		require.NoError(t, f.repo.CreateWorkflow(ctx, &db.Workflow{
			ID: id, ChatID: f.chatID, WorkflowName: "builtin://agent",
			Thread: threadID, Status: status, CreatedAt: at.Add(time.Duration(i) * time.Minute),
		}))
		_, err := store.UpdateThreadWorkflow(ctx, f.repo.ORM(), threadID, id)
		require.NoError(t, err)
		runIDs = append(runIDs, id)
	}
	return threadID, runIDs
}

// Owner decision, 2026-11-27 (obs 0cea454a): delete refuses a worktree a
// sub-agent thread is bound to while that thread's run is live (pending,
// active or paused), naming the thread and run and how to proceed. A
// sub-agent whose run finished does not block, and only its newest run
// counts: an agent resumed as a new run leaves its old row behind as history.
func TestWorktreeTool_Delete_RefusesWhileABoundSubAgentRuns(t *testing.T) {
	f := newWorktreeToolFixture(t)
	ctx := context.Background()

	for name, status := range map[string]db.WorkflowStatus{
		"active": db.Active(), "paused": db.Paused(), "pending": db.Pending(),
	} {
		t.Run("refused while "+name, func(t *testing.T) {
			wt := f.register(t, "busy-"+name, 1)
			thread, runs := f.bindSubAgent(t, wt, status)
			resp := f.call(t, WorktreeParams{Action: "delete", Name: "busy-" + name})
			require.True(t, resp.IsError, resp.Content)
			assert.Contains(t, resp.Content, "sub-agent "+thread+" is "+status.Label())
			assert.Contains(t, resp.Content, "run "+runs[0])
			assert.Contains(t, resp.Content, fmt.Sprintf("spawn_stop(agent_id=%q)", thread))
			got, err := f.repo.GetWorktree(ctx, wt.ID)
			require.NoError(t, err)
			assert.Nil(t, got.DeletedAt, "nothing is archived")
			select {
			case cand := <-f.settler.settled:
				t.Fatalf("a refused delete must not settle anything: %s", cand.Worktree.ID)
			default:
			}
		})
	}

	for name, runs := range map[string][]db.WorkflowStatus{
		"completed":                  {db.Completed()},
		"failed":                     {db.Failed()},
		"cancelled":                  {db.Cancelled()},
		"resumed run since finished": {db.Active(), db.Completed()},
	} {
		t.Run("allowed once "+name, func(t *testing.T) {
			wt := f.register(t, "done-"+strings.ReplaceAll(name, " ", "-"), 1)
			f.bindSubAgent(t, wt, runs...)
			resp := f.call(t, WorktreeParams{Action: "delete", Name: wt.Name})
			require.False(t, resp.IsError, resp.Content)
			got, err := f.repo.GetWorktree(ctx, wt.ID)
			require.NoError(t, err)
			assert.NotNil(t, got.DeletedAt, "archived")
		})
	}
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

	resp := callRunTool(t, NewWorktreeTool(f.repo, f.repo, store.NewChatExtStore(f.repo.ORM()), nil, nil, chatupdates.NewFromORM(f.repo.ORM(), f.repo, nil)), f.rc, WorktreeToolName, WorktreeParams{Action: "create", Name: "feature-auth"})
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
	for _, want := range []string{"Prefer this", "git worktree add", "spawn(worktree=", "~/.reliant/worktrees/", "Reliant sidebar"} {
		assert.Contains(t, desc, want)
	}
	assert.NotContains(t, desc, "repo_id")
	// It steers, it does not forbid, and the user's own rule wins.
	assert.NotContains(t, desc, "ONLY way")
	assert.NotContains(t, desc, "refuses")
	assert.Contains(t, desc, "memory (reliant.md) says where worktrees go, follow that instead")
}

// list narrows by name or branch, can include archived worktrees, and pages
// with a next_page_token; with no parameters it lists every live worktree.
func TestWorktreeTool_List_FiltersAndPages(t *testing.T) {
	f := newWorktreeToolFixture(t)
	for i := 0; i < 3; i++ {
		f.register(t, fmt.Sprintf("feat-%d", i), 1)
	}
	f.register(t, "fix-login", 1)
	gone := f.register(t, "feat-old", 1)
	require.NoError(t, f.repo.ArchiveWorktree(context.Background(), gone.ID))

	names := func(resp ToolResponse) ([]string, WorktreeResponseMetadata) {
		t.Helper()
		require.False(t, resp.IsError, resp.Content)
		var meta WorktreeResponseMetadata
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
		var out []string
		for _, wt := range meta.Worktrees {
			out = append(out, wt.Name)
		}
		sort.Strings(out)
		return out, meta
	}

	all, meta := names(f.call(t, WorktreeParams{Action: "list"}))
	assert.Equal(t, []string{"feat-0", "feat-1", "feat-2", "fix-login"}, all)
	assert.Empty(t, meta.NextPageToken)

	feats, _ := names(f.call(t, WorktreeParams{Action: "list", Filter: "FEAT"}))
	assert.Equal(t, []string{"feat-0", "feat-1", "feat-2"}, feats)
	byBranch, _ := names(f.call(t, WorktreeParams{Action: "list", Filter: "b-fix"}))
	assert.Equal(t, []string{"fix-login"}, byBranch, "the filter matches the branch too")

	withArchived := f.call(t, WorktreeParams{Action: "list", Filter: "feat", Archived: true})
	got, _ := names(withArchived)
	assert.Equal(t, []string{"feat-0", "feat-1", "feat-2", "feat-old"}, got)
	assert.Contains(t, withArchived.Content, "Archived: ")

	page1 := f.call(t, WorktreeParams{Action: "list", Limit: 3})
	first, meta := names(page1)
	require.Len(t, first, 3)
	assert.Equal(t, 4, meta.Total)
	require.NotEmpty(t, meta.NextPageToken)
	assert.Contains(t, page1.Content, "next_page_token: "+meta.NextPageToken)
	rest, meta := names(f.call(t, WorktreeParams{Action: "list", Limit: 3, PageToken: meta.NextPageToken}))
	require.Len(t, rest, 1)
	assert.Empty(t, meta.NextPageToken)
	assert.ElementsMatch(t, all, append(first, rest...))

	bad := f.call(t, WorktreeParams{Action: "list", PageToken: "x"})
	assert.True(t, bad.IsError)
	none := f.call(t, WorktreeParams{Action: "list", Filter: "nothing-like-this"})
	assert.Contains(t, none.Content, "No worktrees match")
}

// list without limit or page_token returns every live worktree, as it did
// before paging existed; an explicit limit pages, and the token continues.
func TestWorktreeTool_List_WithoutPagingListsEveryWorktree(t *testing.T) {
	f := newWorktreeToolFixture(t)
	var want []string
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("wt-%02d", i)
		f.register(t, name, 1)
		want = append(want, name)
	}
	listed := func(resp ToolResponse) ([]string, WorktreeResponseMetadata) {
		t.Helper()
		require.False(t, resp.IsError, resp.Content)
		var meta WorktreeResponseMetadata
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
		var out []string
		for _, wt := range meta.Worktrees {
			out = append(out, wt.Name)
		}
		return out, meta
	}

	resp := f.call(t, WorktreeParams{Action: "list"})
	all, meta := listed(resp)
	assert.ElementsMatch(t, want, all)
	assert.Equal(t, 60, meta.Total)
	assert.Empty(t, meta.NextPageToken)
	assert.Contains(t, resp.Content, "Found 60 worktrees:\n")
	assert.Contains(t, resp.Content, "\n60. ")
	assert.NotContains(t, resp.Content, "next_page_token")
	assert.Contains(t, resp.Content, "All 60 worktrees are listed")

	var paged []string
	token := ""
	for pages := 0; ; pages++ {
		require.Less(t, pages, 3, "60 worktrees at 25 a page is three pages")
		got, meta := listed(f.call(t, WorktreeParams{Action: "list", Limit: 25, PageToken: token}))
		paged = append(paged, got...)
		if meta.NextPageToken == "" {
			assert.Len(t, got, 10)
			break
		}
		assert.Len(t, got, 25)
		token = meta.NextPageToken
	}
	assert.Equal(t, all, paged, "the pages, in order, are the unpaged list")
}

// get names the caller's chats bound to the worktree, with their state.
func TestWorktreeTool_Get_ShowsBoundChats(t *testing.T) {
	f := newWorktreeToolFixture(t)
	wt := f.register(t, "branchy", 1)
	idle := f.register(t, "idle", 1)
	ctx := context.Background()
	chat, err := store.GetChat(ctx, f.repo.ORM(), f.chatID)
	require.NoError(t, err)
	chat.WorktreeID = &wt.ID
	require.NoError(t, f.repo.UpdateChat(ctx, chat))

	resp := f.call(t, WorktreeParams{Action: "get", Name: "branchy"})
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "Chats bound to it")
	assert.Contains(t, resp.Content, "(id "+f.chatID+"), active")
	var meta WorktreeResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Len(t, meta.Chats, 1)
	assert.Equal(t, f.chatID, meta.Chats[0].ID)
	assert.Equal(t, "active", meta.Chats[0].State)

	empty := f.call(t, WorktreeParams{Action: "get", Name: idle.Name})
	require.False(t, empty.IsError, empty.Content)
	assert.Contains(t, empty.Content, "Chats: none of yours is bound to it")
}
