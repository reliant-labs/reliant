// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// fakeRunMachine is the run's machine as the worktree tool sees it: it answers
// the daemon's worktree commands and records which machine each one reached.
type fakeRunMachine struct {
	runDaemon string
	home      string

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
	case "worktree.generate_repo_id":
		return json.Marshal(map[string]string{"repo_id": "repo123"})
	case "skills.get_home_dir":
		return json.Marshal(map[string]string{"home_dir": m.home})
	case "worktree.create":
		return json.Marshal(map[string]any{
			"success":       true,
			"worktree_path": filepath.Join(m.home, ".reliant", "worktrees", req["repo_id"].(string), req["name"].(string)),
			"base_branch":   "main",
		})
	case "worktree.delete_directory":
		return json.Marshal(map[string]any{"deleted": true})
	}
	return json.Marshal(map[string]any{"success": true})
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
	rc      *rctx.ToolContext
}

const worktreeToolHome = "/home/machine-b"

func newWorktreeToolFixture(t *testing.T) *worktreeToolFixture {
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

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, runTestUser)
	return &worktreeToolFixture{
		repo:    repo,
		machine: &fakeRunMachine{runDaemon: "daemon-b", home: worktreeToolHome},
		project: project,
		chatID:  chatID,
		rc:      rctx.NewToolContext(ctx, chatID, chatID, project, nil),
	}
}

func (f *worktreeToolFixture) call(t *testing.T, params WorktreeParams) ToolResponse {
	t.Helper()
	return callRunTool(t, NewWorktreeTool(f.repo, f.machine), f.rc, WorktreeToolName, params)
}

// legacyPath is where the daemon puts a worktree the tool creates.
func legacyPath(name string) string {
	return filepath.Join(worktreeToolHome, ".reliant", "worktrees", "repo123", name)
}

// register records a worktree at the tool's path for name, owned by owner.
func (f *worktreeToolFixture) register(t *testing.T, name, owner string) *db.Worktree {
	t.Helper()
	now := time.Now().UTC()
	wt := &db.Worktree{
		ID: uuid.NewString(), Name: name, Path: legacyPath(name), Branch: "b-" + name,
		ProjectID: f.project.ID, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	if owner != "" {
		wt.DaemonID = &owner
	}
	require.NoError(t, f.repo.CreateWorktree(context.Background(), wt))
	return wt
}

// liveAt returns the project's live worktree rows at path.
func (f *worktreeToolFixture) liveAt(t *testing.T, path string) []*db.Worktree {
	t.Helper()
	rows, err := f.repo.ListWorktrees(context.Background(), db.WorktreeFilters{ProjectID: &f.project.ID})
	require.NoError(t, err)
	var out []*db.Worktree
	for _, row := range rows {
		if row.Path == path {
			out = append(out, row)
		}
	}
	return out
}

// Every step of a create reaches the run's machine, and the row records that
// machine as the owner, so the chat that later works in the worktree, and
// every operation on it, routes to where the checkout is.
func TestWorktreeTool_Create_OnTheRunsMachine(t *testing.T) {
	f := newWorktreeToolFixture(t)

	resp := f.call(t, WorktreeParams{Action: "create", Name: "feature-auth", BaseBranch: "main", CopyFiles: []string{".env"}})
	require.False(t, resp.IsError, resp.Content)

	require.NotEmpty(t, f.machine.sent)
	for _, cmd := range f.machine.sent {
		assert.Equal(t, "daemon-b", cmd.daemonID, "%s ran on the wrong machine", cmd.commandType)
	}
	creates := f.machine.to("worktree.create")
	require.Len(t, creates, 1)
	assert.Equal(t, f.project.Path, creates[0].payload["project_path"])
	assert.Equal(t, "repo123", creates[0].payload["repo_id"])
	assert.Equal(t, "feature-auth", creates[0].payload["name"])
	assert.Contains(t, creates[0].payload["branch"], "worktree/feature-auth-", "an unnamed branch is generated as before")
	copies := f.machine.to("worktree.copy_paths")
	require.Len(t, copies, 1)
	assert.Equal(t, []any{".env"}, copies[0].payload["paths"])
	assert.Equal(t, legacyPath("feature-auth"), copies[0].payload["dest_root"])

	rows := f.liveAt(t, legacyPath("feature-auth"))
	require.Len(t, rows, 1, "the worktree must be registered")
	require.NotNil(t, rows[0].DaemonID, "the worktree must record its owning machine")
	assert.Equal(t, "daemon-b", *rows[0].DaemonID)
	require.NotNil(t, rows[0].ChatID)
	assert.Equal(t, f.chatID, *rows[0].ChatID)

	var meta WorktreeResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	assert.Equal(t, "repo123/feature-auth", meta.WorktreeID)
	assert.Equal(t, legacyPath("feature-auth"), meta.Path)
	assert.Equal(t, "main", meta.StoredInCEL["base_branch"])
}

// The registry still refuses a second worktree with the same name.
func TestWorktreeTool_Create_RefusesARegisteredName(t *testing.T) {
	f := newWorktreeToolFixture(t)
	f.register(t, "feature-auth", "daemon-b")

	resp := f.call(t, WorktreeParams{Action: "create", Name: "feature-auth"})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "already exists in Reliant registry (use force=true to override)")
	assert.Empty(t, f.machine.to("worktree.create"), "nothing may be checked out")
}

// force=true clears the old checkout on the machine, and the row is brought
// up to date in place, so chats bound to it stay bound.
func TestWorktreeTool_CreateForce_ClearsTheOldCheckoutOnTheMachine(t *testing.T) {
	f := newWorktreeToolFixture(t)
	old := f.register(t, "feature-auth", "daemon-b")

	resp := f.call(t, WorktreeParams{Action: "create", Name: "feature-auth", Branch: "feat/auth", Force: true})
	require.False(t, resp.IsError, resp.Content)

	cleanups := f.machine.to("worktree.force_cleanup")
	require.Len(t, cleanups, 1)
	assert.Equal(t, "daemon-b", cleanups[0].daemonID)
	assert.Equal(t, legacyPath("feature-auth"), cleanups[0].payload["worktree_path"])
	assert.Equal(t, "feat/auth", cleanups[0].payload["branch"])
	require.Len(t, f.machine.to("worktree.create"), 1)

	rows := f.liveAt(t, legacyPath("feature-auth"))
	require.Len(t, rows, 1)
	assert.Equal(t, old.ID, rows[0].ID, "the row is updated in place, not duplicated")
	assert.Equal(t, "feat/auth", rows[0].Branch)
}

// Deleting and then creating the same name again brings the archived row
// back: the project holds one row per name, so a second insert would fail
// and leave the new checkout unrecorded.
func TestWorktreeTool_RecreateAfterDelete_ReusesTheRow(t *testing.T) {
	f := newWorktreeToolFixture(t)
	created := f.call(t, WorktreeParams{Action: "create", Name: "feature-auth"})
	require.False(t, created.IsError, created.Content)
	first := f.liveAt(t, legacyPath("feature-auth"))
	require.Len(t, first, 1)

	deleted := f.call(t, WorktreeParams{Action: "delete", Name: "feature-auth"})
	require.False(t, deleted.IsError, deleted.Content)
	require.Empty(t, f.liveAt(t, legacyPath("feature-auth")))

	again := f.call(t, WorktreeParams{Action: "create", Name: "feature-auth"})
	require.False(t, again.IsError, again.Content)
	rows := f.liveAt(t, legacyPath("feature-auth"))
	require.Len(t, rows, 1, "the recreated worktree must be registered")
	assert.Equal(t, first[0].ID, rows[0].ID)
}

// force=true wipes the tool's own path, so it is never applied to a workspace
// made some other way that holds the name: that one lives elsewhere and may be
// in use. Nothing is cleared or checked out.
func TestWorktreeTool_Create_LeavesAnotherWorkspaceWithTheName(t *testing.T) {
	f := newWorktreeToolFixture(t)
	now := time.Now().UTC()
	require.NoError(t, f.repo.CreateWorktree(context.Background(), &db.Worktree{
		ID: uuid.NewString(), Name: "feature-auth", Path: worktreeToolHome + "/.reliant/worktrees/" + uuid.NewString(),
		ProjectID: f.project.ID, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	resp := f.call(t, WorktreeParams{Action: "create", Name: "feature-auth", Force: true})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "already exists in Reliant registry as another workspace")
	assert.Empty(t, f.machine.to("worktree.force_cleanup"))
	assert.Empty(t, f.machine.to("worktree.create"))
}

// A worktree the tool made on another machine is that machine's checkout;
// this run cannot replace it, and must not record a second owner for it.
func TestWorktreeTool_Create_RefusesANameOwnedByAnotherMachine(t *testing.T) {
	f := newWorktreeToolFixture(t)
	f.register(t, "feature-auth", "daemon-c")

	resp := f.call(t, WorktreeParams{Action: "create", Name: "feature-auth", Force: true})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "on another machine")
	assert.Empty(t, f.machine.to("worktree.force_cleanup"))
	assert.Empty(t, f.machine.to("worktree.create"))
}

// A delete goes to the machine that owns the checkout, which need not be the
// machine this run executes on.
func TestWorktreeTool_Delete_RoutesToTheOwningMachine(t *testing.T) {
	f := newWorktreeToolFixture(t)
	wt := f.register(t, "feature-auth", "daemon-c")

	resp := f.call(t, WorktreeParams{Action: "delete", Name: "feature-auth"})
	require.False(t, resp.IsError, resp.Content)

	deletes := f.machine.to("worktree.delete_directory")
	require.Len(t, deletes, 1)
	assert.Equal(t, "daemon-c", deletes[0].daemonID, "the checkout exists only on its owner")
	assert.Equal(t, wt.Path, deletes[0].payload["worktree_path"])
	assert.Empty(t, f.liveAt(t, wt.Path), "a deleted worktree is archived")
}

// Only a worktree the tool registered under the name is deleted: a workspace
// made elsewhere that shares the name is at another path and is left alone.
func TestWorktreeTool_Delete_LeavesOtherWorkspacesWithTheSameName(t *testing.T) {
	f := newWorktreeToolFixture(t)
	now := time.Now().UTC()
	other := &db.Worktree{
		ID: uuid.NewString(), Name: "feature-auth", Path: worktreeToolHome + "/.reliant/worktrees/" + uuid.NewString(),
		ProjectID: f.project.ID, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, f.repo.CreateWorktree(context.Background(), other))

	resp := f.call(t, WorktreeParams{Action: "delete", Name: "feature-auth"})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "worktree 'feature-auth' not found")
	assert.Empty(t, f.machine.to("worktree.delete_directory"))
	assert.Len(t, f.liveAt(t, other.Path), 1)
}

// The name becomes a directory force=true wipes and delete removes on the
// user's machine, so it may not climb out of ~/.reliant/worktrees.
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
// local disk.
func TestWorktreeTool_WithoutAMachine_DoesNothingLocally(t *testing.T) {
	f := newWorktreeToolFixture(t)

	resp := callRunTool(t, NewWorktreeTool(f.repo, nil), f.rc, WorktreeToolName, WorktreeParams{Action: "create", Name: "feature-auth"})
	require.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "no machine can be reached")
	assert.Empty(t, f.liveAt(t, legacyPath("feature-auth")))
}
