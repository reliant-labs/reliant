// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdateTask_SubAgentsUpdateOnlyTheirOwnTask pins who may write a task on
// a shared board (north-star AW-35).
//
// update_task resolved a task id with no check of whose plan it was on, so a
// sub-agent could overwrite a sibling's task on the parent's board — or any
// task on any plan, given its id. Now: a sub-agent may update a task on the
// board it was spawned from, holds the task it updates, and is refused on a
// sibling's task, on a title change, and on a plan outside its chain.
func TestUpdateTask_SubAgentsUpdateOnlyTheirOwnTask(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)
	parentCtx := createTestContext(t, chatID)

	_, err := (&CreatePlanTool{repo: repo}).Execute(parentCtx, CreatePlanParams{
		Title:       "Shared board",
		Description: "Two tasks for two sub-agents",
		Complexity:  "simple",
		Tasks:       []TaskInput{{Title: "Task A"}, {Title: "Task B"}},
	})
	require.NoError(t, err)
	plan, err := repo.GetPlanByThreadID(context.Background(), chatID)
	require.NoError(t, err)
	tasks, err := repo.ListTasksByPlan(context.Background(), plan.ID)
	require.NoError(t, err)
	var taskA string
	for _, task := range tasks {
		if task.Title == "Task A" {
			taskA = task.ID
		}
	}
	require.NotEmpty(t, taskA)

	first := createTestContextForThread(t, chatID, spawnChildThread(t, repo, chatID, chatID))
	sibling := createTestContextForThread(t, chatID, spawnChildThread(t, repo, chatID, chatID))
	update := func(ctx *rctx.ToolContext, p UpdateTaskParams) string {
		t.Helper()
		resp, err := (&UpdateTaskTool{repo: repo}).Execute(ctx, p)
		require.NoError(t, err)
		return resp.Content
	}

	got := update(first, UpdateTaskParams{TaskID: taskA, Status: "in_progress", Assignee: "agent-a"})
	assert.Contains(t, got, "Task updated successfully", "a sub-agent may claim a task on the board it was spawned from")

	got = update(first, UpdateTaskParams{TaskID: taskA, Status: "completed"})
	assert.Contains(t, got, "Task updated successfully", "the holder keeps updating its own task")

	got = update(sibling, UpdateTaskParams{TaskID: taskA, Status: "failed"})
	assert.Contains(t, got, "held by another sub-agent (assignee: agent-a)",
		"a sibling must not overwrite a task another sub-agent holds")
	assert.NotContains(t, got, "Task updated successfully")

	got = update(sibling, UpdateTaskParams{TaskID: taskA, Metadata: map[string]interface{}{claimedByThreadKey: sibling.Thread}})
	assert.Contains(t, got, "held by", "the claim cannot be taken over through metadata")

	got = update(sibling, UpdateTaskParams{TaskID: "2", Title: "Renamed"})
	assert.Contains(t, got, "title is that thread's to change", "a title on an inherited plan is the owner's")

	got = update(sibling, UpdateTaskParams{TaskID: "2", Status: "in_progress"})
	assert.Contains(t, got, "Task updated successfully", "a free task is still claimable by any sub-agent")

	task, err := repo.GetTask(context.Background(), taskA)
	require.NoError(t, err)
	assert.Equal(t, "Task A", task.Title)
	assert.Equal(t, int32(db.TaskStatusCompleted), task.Status, "the holder's completed status survives the sibling's attempt")

	// The owner reassigns: the claim is released and the new agent may take it.
	got = update(parentCtx, UpdateTaskParams{TaskID: taskA, Assignee: "agent-b", Status: "pending"})
	assert.Contains(t, got, "Task updated successfully", "the plan's owner may always update its tasks")
	got = update(sibling, UpdateTaskParams{TaskID: taskA, Status: "in_progress"})
	assert.Contains(t, got, "Task updated successfully", "after reassignment the new agent may take the task")

	// A plan in another chat is outside every chain here.
	otherChat := uuid.New().String()
	createTestChat(t, repo, otherChat)
	_, err = (&CreatePlanTool{repo: repo}).Execute(createTestContext(t, otherChat), CreatePlanParams{
		Title: "Elsewhere", Description: "Another chat's plan", Complexity: "simple",
		Tasks: []TaskInput{{Title: "Not yours"}},
	})
	require.NoError(t, err)
	otherPlan, err := repo.GetPlanByThreadID(context.Background(), otherChat)
	require.NoError(t, err)
	otherTasks, err := repo.ListTasksByPlan(context.Background(), otherPlan.ID)
	require.NoError(t, err)
	require.Len(t, otherTasks, 1)
	for _, ctx := range []*rctx.ToolContext{parentCtx, first} {
		got = update(ctx, UpdateTaskParams{TaskID: otherTasks[0].ID, Status: "completed"})
		assert.Contains(t, got, "not on this thread's plan", "a task on a foreign plan is refused")
	}

	// create_subtask changes the plan's shape: the owner only.
	sub, err := (&CreateSubtaskTool{repo: repo}).Execute(first, CreateSubtaskParams{ParentTaskID: taskA, Title: "sneak"})
	require.NoError(t, err)
	assert.Contains(t, sub.Content, "read-only", "a sub-agent cannot add subtasks to its parent's plan")
	sub, err = (&CreateSubtaskTool{repo: repo}).Execute(parentCtx, CreateSubtaskParams{ParentTaskID: otherTasks[0].ID, Title: "sneak"})
	require.NoError(t, err)
	assert.Contains(t, sub.Content, "not on this thread's plan")
	sub, err = (&CreateSubtaskTool{repo: repo}).Execute(parentCtx, CreateSubtaskParams{ParentTaskID: taskA, Title: "owned"})
	require.NoError(t, err)
	assert.Contains(t, sub.Content, "Subtask created successfully")
}

// Two sibling sub-agents racing to take the same free task: exactly one holds
// it. The holder check and the claim it writes are one serializable
// transaction, so the loser is retried and then sees the claim (review B1 on
// north-star AW-35).
func TestUpdateTask_ConcurrentClaimsHaveOneWinner(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)
	_, err := (&CreatePlanTool{repo: repo}).Execute(createTestContext(t, chatID), CreatePlanParams{
		Title: "Race", Description: "One free task", Complexity: "simple", Tasks: []TaskInput{{Title: "Contested"}},
	})
	require.NoError(t, err)

	const racers = 6
	results := make(chan string, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		ctx := createTestContextForThread(t, chatID, spawnChildThread(t, repo, chatID, chatID))
		go func(i int) {
			<-start
			resp, err := (&UpdateTaskTool{repo: repo}).Execute(ctx, UpdateTaskParams{
				TaskID: "1", Status: "in_progress", Assignee: fmt.Sprintf("agent-%d", i),
			})
			if err != nil {
				results <- "error: " + err.Error()
				return
			}
			results <- resp.Content
		}(i)
	}
	close(start)
	won := 0
	for i := 0; i < racers; i++ {
		got := <-results
		switch {
		case strings.Contains(got, "Task updated successfully"):
			won++
		case strings.Contains(got, "held by another sub-agent"):
		default:
			t.Errorf("unexpected result: %s", got)
		}
	}
	assert.Equal(t, 1, won, "exactly one sibling holds the task")
}
