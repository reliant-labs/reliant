// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/rowreads"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/planningtest"
	"github.com/reliant-labs/reliant/internal/store"
)

// recordedRefetch is one announcement a tool made through the factory's Refetcher.
type recordedRefetch struct {
	scope string // "chat" or "user"
	id    string // chat id or user id
	typ   core.RefetchType
	opts  core.RefetchOpts
}

// recordingRefetcher is a Refetcher that keeps what it was asked to announce.
type recordingRefetcher struct {
	mu   sync.Mutex
	seen []recordedRefetch
}

func (r *recordingRefetcher) EmitChatRefetch(_ context.Context, chatID string, t core.RefetchType) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, recordedRefetch{scope: "chat", id: chatID, typ: t})
	return nil
}

func (r *recordingRefetcher) EmitUserRefetch(_ context.Context, userID string, t core.RefetchType, opts core.RefetchOpts) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, recordedRefetch{scope: "user", id: userID, typ: t, opts: opts})
	return nil
}

// drain returns what was announced since the last drain.
func (r *recordingRefetcher) drain() []recordedRefetch {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.seen
	r.seen = nil
	return out
}

// The planning, task, dependency and worktree tools announce their writes
// through ToolsOptions.Refetch, each exactly once, to the scope and with the
// type clients refetch on. This runs the tools the factory builds, so a
// factory that stops passing f.refetch() to a constructor (or a worker that
// stops injecting the Refetcher) fails here instead of silently degrading to
// the clients' poll.
func TestFactoryTools_AnnounceThroughTheInjectedRefetcher(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.NewString()
	createTestChat(t, repo, chatID)
	ctx := createTestContext(t, chatID)
	rec := &recordingRefetcher{}
	factory := NewToolsFactory(&ToolsOptions{
		Repo:     repo,
		Planning: planningtest.New(t, repo),
		Refetch:  rec,
	})

	wantPlanTasks := recordedRefetch{scope: "chat", id: chatID, typ: core.RefetchPlanTasks}

	step := func(t *testing.T, name string, tool Tool, params any, want ...recordedRefetch) {
		t.Helper()
		rec.drain()
		resp := callRunTool(t, tool, ctx, name, params)
		require.False(t, resp.IsError, "%s: %s", name, resp.Content)
		assert.Equal(t, want, rec.drain(), "%s announces exactly once, to the chat, as plan_tasks", name)
	}

	step(t, "create_plan", factory.CreatePlan(), CreatePlanParams{
		Title: "Announced", Description: "routing", Complexity: "simple",
		Tasks: []TaskInput{{Title: "A"}, {Title: "B"}},
	}, wantPlanTasks)
	step(t, "update_plan", factory.UpdatePlan(), UpdatePlanParams{Title: "Announced, renamed"}, wantPlanTasks)
	step(t, "add_task", factory.AddTask(), AddTaskParams{Title: "C"}, wantPlanTasks)
	step(t, "update_task", factory.UpdateTask(), UpdateTaskParams{TaskID: "1", Status: "in_progress"}, wantPlanTasks)
	step(t, "add_dependency", factory.AddDependency(), AddDependencyParams{FromTask: "1", ToTask: "2", Type: "blocks"}, wantPlanTasks)
	step(t, "remove_dependency", factory.RemoveDependency(), RemoveDependencyParams{FromTask: "1", ToTask: "2", Type: "blocks"}, wantPlanTasks)

	plan, err := planningtest.New(t, repo).GetPlanByThreadID(ctx.Context, chatID)
	require.NoError(t, err)
	tasks, err := repo.ListTasksByPlan(ctx.Context, plan.ID)
	require.NoError(t, err)
	require.NotEmpty(t, tasks)
	step(t, "create_subtask", factory.CreateSubtask(), CreateSubtaskParams{ParentTaskID: tasks[0].ID, Title: "sub"}, wantPlanTasks)
}

// The worktree tool announces to the user, with the project and worktree the
// sidebar refetches, through the same injected Refetcher.
func TestFactoryWorktreeTool_AnnouncesToTheUserThroughTheInjectedRefetcher(t *testing.T) {
	f := newWorktreeToolFixture(t)
	rec := &recordingRefetcher{}
	factory := NewToolsFactory(&ToolsOptions{
		Repo:            f.repo,
		Placement:       rowreads.WithRepo(f.repo),
		WorktreeChats:   store.NewChatExtStore(f.repo.ORM()),
		RunMachine:      f.machine,
		WorktreeSettler: f.settler,
		Refetch:         rec,
	})

	resp := callRunTool(t, factory.Worktree(), f.rc, WorktreeToolName, WorktreeParams{Action: "create", Name: "announced"})
	require.False(t, resp.IsError, resp.Content)

	row := f.row(t, "announced")
	require.NotNil(t, row)
	got := rec.drain()
	require.Len(t, got, 1, "one announcement per create")
	assert.Equal(t, "user", got[0].scope)
	assert.Equal(t, runTestUser, got[0].id)
	assert.Equal(t, core.RefetchWorktreeChanges, got[0].typ)
	require.NotNil(t, got[0].opts.ProjectID)
	assert.Equal(t, f.project.ID, *got[0].opts.ProjectID)
	require.NotNil(t, got[0].opts.WorktreeID)
	assert.Equal(t, row.ID, *got[0].opts.WorktreeID)
}

// With no Refetcher the same tools complete and announce nothing: the row is
// the source of truth and clients keep their fallback poll.
func TestFactoryTools_WithoutARefetcherStillWrite(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.NewString()
	createTestChat(t, repo, chatID)
	ctx := createTestContext(t, chatID)
	factory := NewToolsFactory(&ToolsOptions{Repo: repo, Planning: planningtest.New(t, repo)})

	resp := callRunTool(t, factory.CreatePlan(), ctx, "create_plan", CreatePlanParams{
		Title: "Quiet", Description: "no refetcher", Complexity: "simple", Tasks: []TaskInput{{Title: "A"}},
	})
	require.False(t, resp.IsError, resp.Content)
	resp = callRunTool(t, factory.AddTask(), ctx, "add_task", AddTaskParams{Title: "B"})
	require.False(t, resp.IsError, resp.Content)
	assert.Nil(t, factory.refetch(), "an absent Refetcher is a nil interface, not a typed nil")
}
