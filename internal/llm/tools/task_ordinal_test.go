// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdateTask_OrdinalIsOneIndexed pins update_task's ordinal against the
// numbering the agent actually sees.
//
// list_tasks prints tasks as "1. …, 2. …, 3. …" and create_plan documents
// task_position as "1-indexed", converting with `dep.TaskPosition - 1` before
// use. update_task's ordinal path passed the number straight to
// GetTaskByPosition, which slices `tasks[position]` — 0-indexed. So
// `update_task(task_id: "1")` marked the SECOND task in the list, silently, and
// the highest-numbered task in the list was unreachable ("position N out of
// range").
//
// Silent is what makes this worth a test: the call succeeded and reported a
// task title, so the only way to notice was to read the response closely and
// realise it named the wrong one.
func TestUpdateTask_OrdinalIsOneIndexed(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := uuid.New().String()
	createTestChat(t, repo, chatID)
	ctx := createTestContext(t, chatID)

	_, err := (&CreatePlanTool{repo: repo}).Execute(ctx, CreatePlanParams{
		Title:       "Ordinal check",
		Description: "Three tasks with distinct titles",
		Complexity:  "simple",
		Tasks: []TaskInput{
			{Title: "FIRST task"},
			{Title: "SECOND task"},
			{Title: "THIRD task"},
		},
	})
	require.NoError(t, err)

	// "1" must mean the task list's item 1.
	resp, err := (&UpdateTaskTool{repo: repo}).Execute(ctx, UpdateTaskParams{
		TaskID: "1",
		Status: "completed",
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "FIRST task",
		`update_task(task_id: "1") must update the task list's item 1`)
	assert.NotContains(t, resp.Content, "SECOND task",
		"0-indexing here silently updates the task after the one the agent named")

	// The last item must be reachable at its displayed number.
	last, err := (&UpdateTaskTool{repo: repo}).Execute(ctx, UpdateTaskParams{
		TaskID: "3",
		Status: "completed",
	})
	require.NoError(t, err)
	assert.Contains(t, last.Content, "THIRD task",
		"the highest displayed number must not be out of range")

	// Position 0 is not a label any surface shows, so it is a mistake worth
	// naming rather than silently treating as the first task.
	zero, err := (&UpdateTaskTool{repo: repo}).Execute(ctx, UpdateTaskParams{
		TaskID: "0",
		Status: "completed",
	})
	require.NoError(t, err)
	assert.Contains(t, zero.Content, "out of range",
		"task numbering starts at 1, so 0 should be rejected, not aliased to task 1")
}
