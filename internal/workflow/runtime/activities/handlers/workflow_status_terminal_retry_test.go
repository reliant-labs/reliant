// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// refuseTerminalWritesSQL makes the first n writes of a terminal status to
// workflow id fail, as a database too loaded to commit them would. The count
// lives in a sequence because nextval survives the rollback of the
// transaction it refused.
const refuseTerminalWritesSQL = `
CREATE SEQUENCE test_refused_terminal_writes;
CREATE FUNCTION test_refuse_terminal_writes() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.id = '%s' AND NEW.state = 3 AND NEW.stop_reason <> 3
	   AND nextval('test_refused_terminal_writes') <= %d THEN
		RAISE EXCEPTION 'injected: terminal status write refused';
	END IF;
	RETURN NEW;
END $$;
CREATE TRIGGER test_refuse_terminal_writes BEFORE UPDATE ON workflows
	FOR EACH ROW EXECUTE FUNCTION test_refuse_terminal_writes();
`

// A terminal status write that does not land fails the activity, so Temporal
// retries it (workflowStatusActivityOptions retries a terminal status until
// it lands), and the row ends COMPLETED once a write gets through. It used to
// be logged and the activity reported success over it: the run ended, nothing
// ever wrote its row again, and the chat read as running (e2e story 05).
func TestWorkflowStatus_RefusedTerminalWritesFailTheActivityUntilOneLands(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status string
		want   db.WorkflowStatus
	}{
		{"completed", db.Completed()},
		{"failed", db.Failed()},
		{"cancelled", db.Cancelled()},
	} {
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()

			userID, projectID, chatID := uuid.NewString(), uuid.NewString(), uuid.NewString()
			h.CreateTestProject(ctx, projectID, userID)
			h.CreateTestChat(ctx, chatID, projectID, userID)
			workflowID := uuid.NewString()
			require.NoError(t, h.Repo().CreateWorkflow(ctx, &db.Workflow{
				ID: workflowID, ChatID: chatID, WorkflowName: "builtin://chat", Thread: chatID, Status: db.Active(),
			}))

			const refused = 3
			_, err := h.DB().ExecContext(ctx, fmt.Sprintf(refuseTerminalWritesSQL, workflowID, refused))
			require.NoError(t, err)

			activity := NewWorkflowStatusActivity(h.Repo())
			input := WorkflowStatusInput{
				ChatID: chatID, WorkflowID: workflowID, WorkflowName: "builtin://chat",
				Status: tc.status, Thread: chatID,
			}
			for attempt := 1; attempt <= refused; attempt++ {
				var out WorkflowStatusOutput
				err := h.ExecuteActivity(activity.Execute, input, &out)
				require.Error(t, err, "attempt %d: a refused terminal write fails the activity so Temporal retries it", attempt)
				wf, getErr := h.Repo().GetWorkflow(ctx, workflowID)
				require.NoError(t, getErr)
				assert.Equal(t, db.Active(), wf.Status, "attempt %d wrote nothing", attempt)
			}

			var out WorkflowStatusOutput
			require.NoError(t, h.ExecuteActivity(activity.Execute, input, &out), "the attempt the database accepts succeeds")
			assert.True(t, out.Success)
			wf, err := h.Repo().GetWorkflow(ctx, workflowID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, wf.Status, "the terminal status landed")
		})
	}
}

// A terminal status for a workflow with no row cannot be written by any
// attempt, so it does not fail the activity: retrying it would only hold the
// run open until its deadline.
func TestWorkflowStatus_TerminalStatusOfAMissingRowDoesNotFailTheActivity(t *testing.T) {
	t.Parallel()
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID, projectID, chatID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	activity := NewWorkflowStatusActivity(h.Repo())
	var out WorkflowStatusOutput
	err := h.ExecuteActivity(activity.Execute, WorkflowStatusInput{
		ChatID: chatID, WorkflowID: uuid.NewString(), WorkflowName: "builtin://chat",
		Status: "failed", Thread: chatID,
	}, &out)
	require.NoError(t, err)
	assert.True(t, out.Success)
}
