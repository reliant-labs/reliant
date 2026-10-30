// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// TestResolveRunExecutorContext_PinsWorktreeDaemon verifies that a chat bound to
// a worktree whose owning daemon is recorded resolves a DaemonSelector pinned to
// that daemon — this is what routes a branch chat's tool execution to the
// machine that actually holds its checkout on disk.
func TestResolveRunExecutorContext_PinsWorktreeDaemon(t *testing.T) {
	t.Parallel()

	repo := setupTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	userID := "test-user"
	projectID := uuid.New().String()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, Name: "P", Path: "/tmp/project-main", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	daemonID := "daemon-owning-worktree"
	worktreeID := uuid.New().String()
	require.NoError(t, repo.CreateWorktree(ctx, &core.Worktree{
		ID: worktreeID, Name: "feature", Path: "/home/u/.reliant/worktrees/feature",
		Branch: "feature", BaseBranch: "main", ProjectID: projectID,
		DaemonID: &daemonID, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, ProjectID: projectID, UserID: userID, WorktreeID: &worktreeID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	execCtx, err := resolveRunExecutorContext(ctx, repo, chatID)
	require.NoError(t, err)

	assert.Equal(t, worktreeID, execCtx.WorktreeID)
	assert.Equal(t, "/home/u/.reliant/worktrees/feature", execCtx.WorktreePath)
	require.NotNil(t, execCtx.DaemonSelector, "worktree-bound chat must pin a daemon selector")
	assert.Equal(t, daemonID, execCtx.DaemonSelector.ID)

	// Same precedence as ExecuteTools. A chat pin on the workspace's own
	// machine is the route. A pin naming ANOTHER machine is stale (StartChat,
	// SetChatDaemon and UpdateChat refuse it; worktree_owner_backfill moves
	// it): the branch checkout exists on its owner alone, so the owner wins.
	require.NoError(t, repo.UpdateChatActiveDaemon(ctx, chatID, &daemonID))
	execCtx, err = resolveRunExecutorContext(ctx, repo, chatID)
	require.NoError(t, err)
	require.NotNil(t, execCtx.DaemonSelector)
	assert.Equal(t, daemonID, execCtx.DaemonSelector.ID)

	stale := "chat-pin"
	require.NoError(t, repo.UpdateChatActiveDaemon(ctx, chatID, &stale))
	execCtx, err = resolveRunExecutorContext(ctx, repo, chatID)
	require.NoError(t, err)
	require.NotNil(t, execCtx.DaemonSelector)
	assert.Equal(t, daemonID, execCtx.DaemonSelector.ID, "a pin to a machine without the checkout yields to its owner")
	assert.Equal(t, worktreeID, execCtx.WorktreeID, "machine selection must not discard worktree binding")
}

// TestResolveRunExecutorContext_NoWorktreeNoDaemonSelector verifies a plain chat
// (no worktree) resolves no selector, leaving default daemon resolution intact.
func TestResolveRunExecutorContext_NoWorktreeNoDaemonSelector(t *testing.T) {
	t.Parallel()

	repo := setupTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	userID := "test-user"
	projectID := uuid.New().String()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, Name: "P", Path: "/tmp/project-main", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, ProjectID: projectID, UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	execCtx, err := resolveRunExecutorContext(ctx, repo, chatID)
	require.NoError(t, err)

	assert.Empty(t, execCtx.WorktreeID)
	assert.Nil(t, execCtx.DaemonSelector, "plain chat must not pin a daemon selector")
	assert.Equal(t, "/tmp/project-main", execCtx.ProjectPath)
}

// TestResolveRunExecutorContext_WorktreeWithoutDaemon verifies a legacy worktree
// row with no recorded daemon leaves the selector nil (falls back to default
// resolution) rather than pinning an empty daemon id.
func TestResolveRunExecutorContext_WorktreeWithoutDaemon(t *testing.T) {
	t.Parallel()

	repo := setupTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	userID := "test-user"
	projectID := uuid.New().String()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, Name: "P", Path: "/tmp/project-main", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	worktreeID := uuid.New().String()
	require.NoError(t, repo.CreateWorktree(ctx, &core.Worktree{
		ID: worktreeID, Name: "legacy", Path: "/home/u/.reliant/worktrees/legacy",
		Branch: "legacy", BaseBranch: "main", ProjectID: projectID,
		DaemonID: nil, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, ProjectID: projectID, UserID: userID, WorktreeID: &worktreeID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	execCtx, err := resolveRunExecutorContext(ctx, repo, chatID)
	require.NoError(t, err)

	assert.Equal(t, worktreeID, execCtx.WorktreeID)
	assert.Nil(t, execCtx.DaemonSelector, "worktree without recorded daemon must not pin a selector")
}

// selectorRecordingExecutor records the machine each command was sent to.
type selectorRecordingExecutor struct {
	mu   sync.Mutex
	sent []string
}

func (e *selectorRecordingExecutor) ExecuteTool(_ context.Context, req *toolexec.ToolRequest) (*toolexec.ToolResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := ""
	if req.DaemonSelector != nil {
		id = req.DaemonSelector.ID
	}
	e.sent = append(e.sent, id)
	return &toolexec.ToolResult{Success: true, Content: `{"stdout":"ok","exit_code":0}`}, nil
}

func (e *selectorRecordingExecutor) Close() error { return nil }

// A run node's dispatch: the daemon_selector startRun puts in the activity
// input (StepExecutor.runDaemonSelector), decoded as Temporal decodes it, to
// the machine ExecuteRunStep sends the command to. A run launched before its
// chat moved to the workspace's machine carries the old pin as a session
// selector for its whole life; its commands run on the workspace's owner, as
// ExecuteTools' do. A selector the workflow or node configures still routes
// where it says.
func TestExecuteRunStep_AStaleSessionPinRunsOnTheWorkspaceOwner(t *testing.T) {
	t.Parallel()

	repo := setupTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	userID := "test-user"
	projectID := uuid.New().String()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, Name: "P", Path: "/tmp/project-main", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	owner := "workspace-owner"
	worktreeID := uuid.New().String()
	require.NoError(t, repo.CreateWorktree(ctx, &core.Worktree{
		ID: worktreeID, Name: "feature", Path: "/home/u/.reliant/worktrees/feature",
		Branch: "feature", BaseBranch: "main", ProjectID: projectID,
		DaemonID: &owner, Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, ProjectID: projectID, UserID: userID, WorktreeID: &worktreeID,
		ActiveDaemonID: &owner, // moved to the owner after the run started
		CreatedAt:      now, UpdatedAt: now, LastActive: now,
	}))

	for _, tc := range []struct {
		name     string
		selector map[string]interface{}
		want     string
	}{
		{"the run's stale session pin", map[string]interface{}{"id": "former-pin", "name": "", "type": "", "labels": nil, "session": true}, owner},
		{"a configured id", map[string]interface{}{"id": "cicd", "name": "", "type": "", "labels": nil}, "cicd"},
		{"a session pin on the owner", map[string]interface{}{"id": owner, "name": "", "type": "", "labels": nil, "session": true}, owner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]interface{}{
				"workflow_id": "wf", "chat_id": chatID, "step_id": "run", "command": "pwd", "timeout": 5000,
				"daemon_selector": tc.selector,
			})
			require.NoError(t, err)
			var input ExecuteRunStepInput
			require.NoError(t, json.Unmarshal(raw, &input))

			executor := &selectorRecordingExecutor{}
			act := NewExecuteRunStepActivity(repo, executor, nil)
			env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
			env.RegisterActivity(act.Execute)
			_, err = env.ExecuteActivity(act.Execute, input)
			require.NoError(t, err)
			assert.Equal(t, []string{tc.want}, executor.sent)
		})
	}
}
