// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spawnChildThread creates a thread parented to parentID, the shape the spawn
// path produces: a fresh thread of its own, linked to the thread that spawned
// it.
func spawnChildThread(t *testing.T, repo db.Repository, chatID, parentID string) string {
	t.Helper()
	childID := uuid.New().String()
	_, err := repo.CreateThread(context.Background(), &db.Thread{
		ID:             childID,
		ChatID:         chatID,
		ParentThreadID: &parentID,
		Origin:         core.ThreadOriginSpawn,
		Status:         core.ThreadStatusRunning,
		CreatedAt:      time.Now().UTC(),
	})
	require.NoError(t, err)
	return childID
}

// TestPlanReadThrough_SubAgentSeesParentBoard pins the delegation half of the
// plan tools.
//
// Plans are keyed by thread and a spawned sub-agent gets a fresh one, so the
// orchestrator's plan used to become invisible at exactly the moment it
// delegated — create_plan's description had to carry the warning "Any spawned
// workflows, agents, or sub threads will not see the plan or any associated
// tasks." Measured over one long run: sub-agents built 9 private plans of their
// own while the root thread that did all the delegating had none, so the task
// graph describing what could run concurrently never reached the agents doing
// the work.
func TestPlanReadThrough_SubAgentSeesParentBoard(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)
	parentCtx := createTestContext(t, chatID)

	// The parent plans and delegates: three tasks that share no dependency.
	_, err := (&CreatePlanTool{repo: repo}).Execute(parentCtx, CreatePlanParams{
		Title:       "Ship the feature",
		Description: "Deliver the feature end to end",
		Complexity:  "moderate",
		Tasks: []TaskInput{
			{Title: "Implement sim"},
			{Title: "Implement content"},
			{Title: "Implement UI"},
		},
	})
	require.NoError(t, err)

	childID := spawnChildThread(t, repo, chatID, chatID)
	childCtx := createTestContextForThread(t, chatID, childID)

	t.Run("list_tasks resolves to the parent's plan", func(t *testing.T) {
		resp, err := (&ListTasksTool{repo: repo}).Execute(childCtx, ListTasksParams{})
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "Implement sim",
			"a sub-agent must see the board it was spawned from")
		assert.Contains(t, resp.Content, "ancestor thread",
			"and must be told it is reading an inherited plan, not its own")
	})

	t.Run("get_plan resolves to the parent's plan", func(t *testing.T) {
		resp, err := (&GetPlanTool{repo: repo}).Execute(childCtx, GetPlanParams{})
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "Ship the feature")
	})

	// Writes stay with the owning thread: several sub-agents share one board,
	// so letting each mutate its shape is a concurrent-write problem nothing
	// here needs. The refusal must NOT advise create_plan — that is what
	// fragments the parent's board into private copies.
	t.Run("add_task refuses, and does not advise create_plan", func(t *testing.T) {
		resp, err := (&AddTaskTool{repo: repo}).Execute(childCtx, AddTaskParams{Title: "sneak in a task"})
		require.NoError(t, err)
		assert.Contains(t, resp.Content, "read-only")
		assert.Contains(t, resp.Content, chatID, "the refusal names the owning thread")
		assert.NotContains(t, resp.Content, "Use create_plan to create one",
			"advising create_plan here is what produced 9 fragmented per-sub-agent plans")
	})

	// The claim path: an agent spawned to do task 2 must be able to say
	// "2 is mine, and now it is done" against the board it can see.
	t.Run("update_task by ordinal resolves against the inherited plan", func(t *testing.T) {
		assignee := "sub-agent-content"
		resp, err := (&UpdateTaskTool{repo: repo}).Execute(childCtx, UpdateTaskParams{
			TaskID:   "2",
			Status:   "in_progress",
			Assignee: assignee,
		})
		require.NoError(t, err)
		assert.NotContains(t, resp.Content, "No plan found")

		// And the parent sees the claim, which is what stops it handing the
		// same task to a second agent.
		parentView, err := (&ListTasksTool{repo: repo}).Execute(parentCtx, ListTasksParams{})
		require.NoError(t, err)
		assert.Contains(t, parentView.Content, assignee,
			"assignee must be visible on the parent's board or the claim protocol is unobservable")
	})
}

// TestPlanReadThrough_OwnPlanWins: a sub-agent that made its own plan is
// working to that one, not its parent's.
func TestPlanReadThrough_OwnPlanWins(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)
	parentCtx := createTestContext(t, chatID)

	_, err := (&CreatePlanTool{repo: repo}).Execute(parentCtx, CreatePlanParams{
		Title:       "Parent board",
		Description: "The orchestrator board",
		Complexity:  "simple",
		Tasks:       []TaskInput{{Title: "Parent task"}},
	})
	require.NoError(t, err)

	childID := spawnChildThread(t, repo, chatID, chatID)
	childCtx := createTestContextForThread(t, chatID, childID)

	_, err = (&CreatePlanTool{repo: repo}).Execute(childCtx, CreatePlanParams{
		Title:       "My own breakdown",
		Description: "Sub-agent local breakdown",
		Complexity:  "simple",
		Tasks:       []TaskInput{{Title: "My own task"}},
	})
	require.NoError(t, err)

	resp, err := (&ListTasksTool{repo: repo}).Execute(childCtx, ListTasksParams{})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "My own task")
	assert.NotContains(t, resp.Content, "Parent task",
		"a thread with its own plan must not read through to an ancestor's")
	assert.NotContains(t, resp.Content, "ancestor thread",
		"and must not be told it is reading an inherited board")
}

// TestPlanReadThrough_ReadySetIsStatedAsFanOut: the ready count was already
// computed and printed as a bare statistic ("Pending: 12 (5 ready)"). Ready
// tasks have no dependency between them, which is precisely the fan-out set —
// the number is only useful if it says so.
func TestPlanReadThrough_ReadySetIsStatedAsFanOut(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)
	ctx := createTestContext(t, chatID)

	// Fan-out: three tasks blocked by one, then all ready together.
	_, err := (&CreatePlanTool{repo: repo}).Execute(ctx, CreatePlanParams{
		Title:       "Fan out",
		Description: "Schema then three independent implementations",
		Complexity:  "moderate",
		Tasks: []TaskInput{
			{Title: "Design schema"},
			{Title: "Implement API", Dependencies: []TaskDependencyInput{{TaskPosition: 1, Type: "blocks"}}},
			{Title: "Implement worker", Dependencies: []TaskDependencyInput{{TaskPosition: 1, Type: "blocks"}}},
			{Title: "Implement frontend", Dependencies: []TaskDependencyInput{{TaskPosition: 1, Type: "blocks"}}},
		},
	})
	require.NoError(t, err)

	// Only the schema is ready while it blocks the rest, so there is no
	// fan-out to advertise yet.
	first, err := (&ListTasksTool{repo: repo}).Execute(ctx, ListTasksParams{})
	require.NoError(t, err)
	assert.NotContains(t, first.Content, "ready now",
		"one ready task is not a fan-out; advertising it would be noise")

	_, err = (&UpdateTaskTool{repo: repo}).Execute(ctx, UpdateTaskParams{TaskID: "1", Status: "completed"})
	require.NoError(t, err)

	after, err := (&ListTasksTool{repo: repo}).Execute(ctx, ListTasksParams{})
	require.NoError(t, err)
	assert.Contains(t, after.Content, "3 tasks are ready now",
		"once the blocker completes, the three independent tasks are the fan-out set")
	assert.Contains(t, after.Content, "ONE turn",
		"the ready set must be stated as delegable work, not left as a statistic")
}

func createTestContextForThread(t *testing.T, chatID, threadID string) *rctx.ToolContext {
	t.Helper()
	c := createTestContext(t, chatID)
	c.Thread = threadID
	return c
}
