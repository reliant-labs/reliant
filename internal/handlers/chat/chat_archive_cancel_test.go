// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
)

// cancelTestTemporalClient answers CancelWorkflow with err and records the
// runs it was asked to cancel.
type cancelTestTemporalClient struct {
	client.Client
	err      error
	canceled []string
}

func (c *cancelTestTemporalClient) CancelWorkflow(_ context.Context, workflowID, runID string) error {
	c.canceled = append(c.canceled, workflowID+"/"+runID)
	return c.err
}

func archivedChat() *db.Chat {
	workflowID, runID := "wf-1", "run-1"
	return &db.Chat{ID: "chat-1", WorkflowID: &workflowID, RunID: &runID}
}

// Prod (GO-M, 7 events in one evening): archiving a chat whose run had
// already finished logged "Failed to cancel workflow for archived chat" at
// ERROR with Temporal's NotFound. Archiving wants the run gone; a run that is
// already gone is the archive succeeding.
func TestCancelArchivedChatWorkflow_RunAlreadyGoneIsSuccess(t *testing.T) {
	for name, err := range map[string]error{
		"run finished":   serviceerror.NewNotFound("workflow execution already completed"),
		"run not found":  serviceerror.NewNotFound(`workflow execution not found for workflow ID "wf-1" and run ID "run-1"`),
		"cancel request": nil,
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLogs(t)
			temporal := &cancelTestTemporalClient{err: err}
			svc := &Service{deps: Deps{Temporal: temporal}}

			svc.cancelArchivedChatWorkflow(context.Background(), archivedChat())

			require.Equal(t, []string{"wf-1/run-1"}, temporal.canceled, "the archived chat's run is the one cancelled")
			require.Empty(t, logRecords(logs, "Failed to cancel workflow for archived chat"),
				"a run that is already gone is not a failure to cancel")
		})
	}
}

// Any other failure leaves a run going for a chat the user put away.
func TestCancelArchivedChatWorkflow_RealFailureIsStillAnError(t *testing.T) {
	logs := captureLogs(t)
	svc := &Service{deps: Deps{Temporal: &cancelTestTemporalClient{err: serviceerror.NewUnavailable("frontend unreachable")}}}

	svc.cancelArchivedChatWorkflow(context.Background(), archivedChat())

	records := logRecords(logs, "Failed to cancel workflow for archived chat")
	require.Len(t, records, 1)
	require.Equal(t, "ERROR", records[0]["level"])
	require.Equal(t, "chat-1", records[0]["chatID"])
	require.Equal(t, "wf-1", records[0]["workflowID"])
}

// A chat that never started has no run, and nothing is asked of Temporal.
func TestCancelArchivedChatWorkflow_NoRunNoCall(t *testing.T) {
	t.Parallel()

	temporal := &cancelTestTemporalClient{err: errors.New("must not be called")}
	svc := &Service{deps: Deps{Temporal: temporal}}

	svc.cancelArchivedChatWorkflow(context.Background(), &db.Chat{ID: "chat-1"})

	require.Empty(t, temporal.canceled)
}
