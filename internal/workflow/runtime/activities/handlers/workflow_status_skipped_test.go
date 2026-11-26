// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A run that completed into a declared outcome: skipped chose not to do its
// work this time. It is not a failure anywhere: its firing records run_status
// completed with run_outcome skipped (trigger health reads the pair as a
// skip), the chat is not marked unread, no failure message is written, and
// the run is archived like any finished automation run.
func TestWorkflowStatus_DeclaredSkipIsNotAFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	f := newStreakFixture(t, false)
	chatID := f.launch(core.TriggerEventKindSchedule, db.Active())
	in := WorkflowStatusInput{
		ChatID: chatID, WorkflowID: chatID, WorkflowName: "builtin://agent", Status: "completed", Thread: chatID,
		Outcome: model.OutcomeSkipped, OutcomeNode: "unhealthy", LaunchRun: true,
	}
	var out WorkflowStatusOutput
	require.NoError(t, f.h.ExecuteActivity(NewWorkflowStatusActivity(workflowStatusDeps(f.h.Repo())).Execute, in, &out))

	var runStatus, eventOutcome, rowOutcome sql.NullString
	require.NoError(t, f.h.DB().QueryRowContext(ctx,
		`SELECT run_status, run_outcome FROM trigger_events WHERE chat_id = $1`, chatID).Scan(&runStatus, &eventOutcome))
	assert.Equal(t, core.TriggerRunCompleted, runStatus.String, "a skip is a completed run, never a failed one")
	assert.Equal(t, core.RunOutcomeSkipped, eventOutcome.String)
	require.NoError(t, f.h.DB().QueryRowContext(ctx,
		`SELECT run_outcome FROM workflows WHERE id = $1`, chatID).Scan(&rowOutcome))
	assert.Equal(t, core.RunOutcomeSkipped, rowOutcome.String)

	chat, err := store.GetChat(ctx, f.h.Repo().ORM(), chatID)
	require.NoError(t, err)
	assert.False(t, chat.Unread, "a skip notifies nobody")
	assert.Equal(t, db.ChatStateArchived, chat.State, "a skipped automation run is archived like a success")

	messages, err := f.h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	assert.Empty(t, messages, "a skip writes no failure message")
}
