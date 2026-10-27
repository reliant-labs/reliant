package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

// Exercise the actual public tool, Finish and registered daemon handler, not
// fakeRunMachine's success-only response. Only git setup/assertions are test
// code; the branch reset itself must happen inside worktree.create.
func TestWorktreeToolForceResetsStaleBranchesAfterValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("real git repositories and tool/daemon round trip; full scoped test")
	}
	fixture := newBaseRepos(t)
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	project := &db.Project{ID: uuid.NewString(), UserID: "test-user", Name: "force-proof", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateProject(ctx, project))
	chat := &db.Chat{ID: uuid.NewString(), ProjectID: project.ID, UserID: "test-user", CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateChat(ctx, chat))
	rc := rctx.NewToolContext(ctx, chat.ID, chat.ID, project, nil)
	machine := &forceToolMachine{}
	oldHeads := map[string]string{}
	for _, name := range []string{"api", "web"} {
		dir := filepath.Join(project.Path, name)
		fixture.run(project.Path, "clone", fixture.origin, dir)
		fixture.run(dir, "branch", "feat/again", "refs/remotes/origin/feat/x")
		oldHeads[dir] = fixture.run(dir, "rev-parse", "refs/heads/feat/again")
		rc.Repos = append(rc.Repos, &core.Repo{ID: uuid.NewString(), ProjectID: project.ID, Name: name, RelativePath: name})
	}
	tool := tools.NewWorktreeTool(repo, machine, nil)
	call := func(name, base string) tools.ToolResponse {
		input, err := json.Marshal(tools.WorktreeParams{Action: "create", Name: name, Branch: "feat/again", BaseBranch: base, Force: true})
		require.NoError(t, err)
		result, err := tool.Run(rc, tools.ToolCall{ID: uuid.NewString(), Name: tools.WorktreeToolName, Input: string(input)})
		require.NoError(t, err)
		return result
	}
	// A forced request with invalid base must leave stale branch data alone.
	refused := call("invalid-base", "missing-base")
	require.True(t, refused.IsError, refused.Content)
	for dir, old := range oldHeads {
		require.Equal(t, old, fixture.run(dir, "rev-parse", "refs/heads/feat/again"))
	}

	made := call("again", "main")
	require.False(t, made.IsError, made.Content)
	rows, err := repo.ListWorktrees(ctx, db.WorktreeFilters{ProjectID: &project.ID, IncludeArchived: true})
	require.NoError(t, err)
	var created *db.Worktree
	for _, row := range rows {
		if row.Name == "again" {
			created = row
		}
	}
	require.NotNil(t, created)
	require.NotEmpty(t, created.Path)
	for _, name := range []string{"api", "web"} {
		dir := filepath.Join(project.Path, name)
		expected := fixture.run(dir, "rev-parse", "refs/remotes/origin/main")
		require.NotEqual(t, oldHeads[dir], expected, "precondition: a stale branch really moved")
		require.Equal(t, expected, fixture.run(dir, "rev-parse", "refs/heads/feat/again"))
		require.Equal(t, expected, fixture.run(filepath.Join(created.Path, name), "rev-parse", "HEAD"))
	}
	machine.mu.Lock()
	defer machine.mu.Unlock()
	require.Len(t, machine.commands, 4, "two refused creates, two successful creates; no external cleanup")
	for _, command := range machine.commands {
		require.Equal(t, "worktree.create", command)
	}
}

type forceToolMachine struct {
	mu       sync.Mutex
	commands []string
}

func (*forceToolMachine) DaemonID(context.Context, string) (string, error) {
	return "force-test-daemon", nil
}
func (m *forceToolMachine) Send(ctx context.Context, _, _, command string, payload []byte, _ int32) ([]byte, error) {
	m.mu.Lock()
	m.commands = append(m.commands, command)
	m.mu.Unlock()
	if command != "worktree.create" {
		return nil, fmt.Errorf("unexpected command %s", command)
	}
	return DefaultRegistry().Handle(ctx, command, payload)
}

// Force must not reset an already checked-out dirty branch even when the
// requested base is valid: Git refuses it instead of detaching/deleting work.
func TestWorktreeForcePreservesDirtyCheckedOutBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("real git worktree with uncommitted content; full scoped test")
	}
	r := newBaseRepos(t)
	expected := r.run(r.clone, "rev-parse", "refs/remotes/origin/feat/x")
	existing := filepath.Join(t.TempDir(), "existing")
	r.run(r.clone, "worktree", "add", "-b", "feat/again", existing, expected)
	sentinel := filepath.Join(existing, "uncommitted.txt")
	require.NoError(t, os.WriteFile(sentinel, []byte("keep me"), 0600))
	for _, base := range []string{"missing-base", "main"} {
		payload, err := json.Marshal(worktreeCreateRequest{ProjectPath: r.clone, WorkspaceID: uniqueWorkspaceID(t, "force-dirty-"+base), Branch: "feat/again", BaseBranch: base, Force: true})
		require.NoError(t, err)
		raw, err := handleWorktreeCreate(context.Background(), payload)
		require.NoError(t, err)
		var out worktreeCreateResponse
		require.NoError(t, json.Unmarshal(raw, &out))
		require.False(t, out.Success, "base=%s", base)
		require.Equal(t, expected, r.run(existing, "rev-parse", "HEAD"))
		require.Equal(t, expected, r.run(r.clone, "rev-parse", "refs/heads/feat/again"))
		data, err := os.ReadFile(sentinel)
		require.NoError(t, err)
		require.Equal(t, "keep me", string(data))
	}
}
