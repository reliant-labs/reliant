// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

type pausedForMachineFixture struct {
	h          *IdempotencyTestHelper
	activity   *WorkflowStatusActivity
	chatID     string
	workflowID string
}

func newPausedForMachineFixture(t *testing.T) pausedForMachineFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	ctx := context.Background()
	userID, projectID := uuid.NewString(), uuid.NewString()
	chatID, workflowID := uuid.NewString(), uuid.NewString()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
		ID: workflowID, ChatID: chatID, WorkflowName: "builtin://chat", Thread: chatID, Status: db.Active(),
	}))
	return pausedForMachineFixture{h: h, activity: NewWorkflowStatusActivity(h.Repo()), chatID: chatID, workflowID: workflowID}
}

func (f pausedForMachineFixture) report(t *testing.T, status string, opts func(*WorkflowStatusInput)) {
	t.Helper()
	input := WorkflowStatusInput{ChatID: f.chatID, WorkflowID: f.workflowID, WorkflowName: "builtin://chat", Status: status, Thread: f.chatID}
	if opts != nil {
		opts(&input)
	}
	var out WorkflowStatusOutput
	require.NoError(t, f.h.ExecuteActivity(f.activity.Execute, input, &out))
	require.True(t, out.Success)
}

func (f pausedForMachineFixture) marked(t *testing.T) bool {
	t.Helper()
	var marked bool
	require.NoError(t, f.h.sqlDB.QueryRow(
		`SELECT paused_for_machine_at IS NOT NULL FROM chats WHERE id = $1`, f.chatID).Scan(&marked))
	return marked
}

// The breaker's pause marks the chat, and the row it lists reads paused: the
// pair queueddelivery resumes on a machine connect.
func TestWorkflowStatus_BreakerPauseMarksChatPausedForMachine(t *testing.T) {
	t.Parallel()

	f := newPausedForMachineFixture(t)

	f.report(t, "paused", func(in *WorkflowStatusInput) { in.PausedForMachine = true })

	assert.True(t, f.marked(t))
	wf, err := f.h.Repo().GetWorkflow(context.Background(), f.workflowID)
	require.NoError(t, err)
	assert.Equal(t, db.Paused(), wf.Status)
}

// Any resume clears the mark — whatever resumed it — so a user who retried
// before the machine came back is not resumed a second time.
func TestWorkflowStatus_ResumeClearsPausedForMachine(t *testing.T) {
	t.Parallel()

	f := newPausedForMachineFixture(t)
	f.report(t, "paused", func(in *WorkflowStatusInput) { in.PausedForMachine = true })
	require.True(t, f.marked(t))

	f.report(t, "started", func(in *WorkflowStatusInput) { in.Resumed = true })

	assert.False(t, f.marked(t))
}

// A pause that is not the breaker's (the user's, a rate limit) carries no mark
// and clears a stale one: it must not be undone by a machine connecting.
func TestWorkflowStatus_OtherPauseIsNotPausedForMachine(t *testing.T) {
	t.Parallel()

	f := newPausedForMachineFixture(t)
	f.report(t, "paused", func(in *WorkflowStatusInput) { in.PausedForMachine = true })
	require.True(t, f.marked(t))

	f.report(t, "paused", nil)

	assert.False(t, f.marked(t))
}

// A nested workflow's report says nothing about the root run: it neither
// sets nor clears the chat's mark.
func TestWorkflowStatus_ChildReportDoesNotClearPausedForMachine(t *testing.T) {
	t.Parallel()

	f := newPausedForMachineFixture(t)
	f.report(t, "paused", func(in *WorkflowStatusInput) { in.PausedForMachine = true })
	childID := uuid.NewString()
	require.NoError(t, f.h.Repo().CreateWorkflow(context.Background(), &db.Workflow{
		ID: childID, ParentID: &f.workflowID, ChatID: f.chatID, WorkflowName: "builtin://agent", Thread: childID, Status: db.Active(),
	}))

	var out WorkflowStatusOutput
	require.NoError(t, f.h.ExecuteActivity(f.activity.Execute, WorkflowStatusInput{
		ChatID: f.chatID, WorkflowID: childID, WorkflowName: "builtin://agent", Status: "started",
		ParentWorkflowID: f.workflowID, Thread: childID,
	}, &out))

	assert.True(t, f.marked(t))
}
