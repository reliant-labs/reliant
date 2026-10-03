// Copyright (c) 2025 Reliant Labs
package services

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
)

// StartChat is the first send; SendMessage is every send after it. These tests
// pin the boundary from SendMessage's side and from the read side.
//
// The split exists so that "a chat starts exactly once" is a database
// invariant — the chat.start trigger event, unique on the chat id — rather than
// something each client has to get right. If SendMessage could still start a
// pending chat, a branch's first send would launch with no event row behind it
// and a retried send could start it twice.

// TestSendMessage_PendingChatIsRejected: a chat whose root run has never
// started (a branch awaiting its first send) must be started through
// StartChat. SendMessage refuses rather than quietly starting it, and starts
// nothing.
func TestSendMessage_PendingChatIsRejected(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Pending())
	temporal := &wakeTestTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{exists: false},
	}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, nil),
	}

	before := transcriptBodies(t, ctx, repo, fx.chatID)

	_, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "first message of a branch"))
	require.Error(t, err)

	connectErr := new(connect.Error)
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "StartChat",
		"the error must name the RPC the caller should use instead")

	assert.Equal(t, before, transcriptBodies(t, ctx, repo, fx.chatID),
		"a rejected send must not persist the message: StartChat will save it when it starts the chat")
	assert.Empty(t, temporal.signals, "a rejected send must not signal anything")

	root, err := repo.GetWorkflow(ctx, fx.chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Pending(), root.Status, "the chat must still be startable by StartChat")
}

// TestChatProto_CarriesRootWorkflowState: GetChat reports where the chat's
// root run is. The web routes a send on exactly this — PENDING goes to
// StartChat, everything else to SendMessage — and it reads paused from the
// same pair, so a chat whose fields were left unset looked neither pending nor
// paused, whatever its run was doing.
func TestChatProto_CarriesRootWorkflowState(t *testing.T) {
	cases := []struct {
		name       string
		status     db.WorkflowStatus
		wantState  reliantv1.WorkflowState
		wantReason reliantv1.WorkflowStopReason
	}{
		{"pending", db.Pending(), reliantv1.WorkflowState_WORKFLOW_STATE_PENDING, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_UNSPECIFIED},
		{"active", db.Active(), reliantv1.WorkflowState_WORKFLOW_STATE_ACTIVE, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_UNSPECIFIED},
		{"paused", db.Paused(), reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_PAUSED},
		{"completed", db.Completed(), reliantv1.WorkflowState_WORKFLOW_STATE_STOPPED, reliantv1.WorkflowStopReason_WORKFLOW_STOP_REASON_COMPLETED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := db.SetupTestDB(t)
			t.Cleanup(cleanup)

			ctx, fx := setupAbsorbFixture(t, repo, "test-user", tc.status)
			// GetChat probes Temporal only for a run the DB calls active; say
			// it is running so the read stays a pure projection.
			temporal := &absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING}
			service := &ChatService{
				database:   repo,
				tempClient: temporal,
				runs:       runs.NewService(repo, temporal, nil),
			}

			resp, err := service.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: fx.chatID}))
			require.NoError(t, err)
			assert.Equal(t, tc.wantState, resp.Msg.Chat.WorkflowState)
			assert.Equal(t, tc.wantReason, resp.Msg.Chat.WorkflowStopReason)
		})
	}
}
