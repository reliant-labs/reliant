package chat

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// send_to_run reaches a running run through SignalRun, which is SendMessage.
// A message to a running thread is queued, and the queued row is what the
// drain attributes: queued as the human's, pr-fix's hand-off of 2026-11-05
// drained into history as the owner's own words. Under a MessageOrigin it is
// queued as the sending run's.
func TestSendMessage_QueuedUnderAnOriginIsTheSendersMessage(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, target := setupAbsorbFixture(t, repo, "test-user", db.Active())
	_, sender := setupAbsorbFixture(t, repo, "test-user", db.Active())

	ctx = db.WithMessageOrigin(ctx, db.MessageOrigin{
		ThreadID: sender.rootThreadID,
		Kind:     core.AgentMessageKindRunMessage,
		Subject:  "PR stuck",
	})
	resp, err := lateMessageService(repo, &closingRunTemporalClient{}).SendMessage(ctx,
		sendMessageRequest(t, target.chatID, "[pr-fix] Your PR is stuck"))
	require.NoError(t, err)
	require.True(t, resp.Msg.Queued)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, target.rootThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	assert.Equal(t, core.AgentMessageKindRunMessage, queued[0].Kind, "not a HUMAN_MESSAGE: the drain would show it as the user's")
	assert.Equal(t, sender.rootThreadID, queued[0].FromThreadID)
	assert.Equal(t, "PR stuck", queued[0].Subject)
	assert.Nil(t, queued[0].DisplayStyle)

	listed, err := lateMessageService(repo, &closingRunTemporalClient{}).ListQueuedAgentMessages(ctx, connect.NewRequest(&reliantv1.ListQueuedAgentMessagesRequest{
		ChatId: target.chatID, ThreadId: target.rootThreadID,
	}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.Messages, 1)
	assert.Equal(t, core.AgentMessageKindRunMessage, listed.Msg.Messages[0].SenderKind,
		"the pending strip shows only the human's own queued messages, so it leaves this one out")
}

// An async ask_user answer to a running thread queues LLM-only: it drains
// hidden (the question card shows it), and the pending strip, which lists
// what the user queued and can cancel, never shows it.
func TestSendMessage_QueuedLLMOnlyIsHiddenAndOffTheStrip(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())

	resp, err := lateMessageService(repo, &closingRunTemporalClient{}).SendMessage(db.WithLLMOnlyDelivery(ctx),
		sendMessageRequest(t, fx.chatID, "Answer to the question you asked without waiting"))
	require.NoError(t, err)
	require.True(t, resp.Msg.Queued)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.NotNil(t, queued[0].DisplayStyle)
	assert.Equal(t, int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN), *queued[0].DisplayStyle)

	listed, err := lateMessageService(repo, &closingRunTemporalClient{}).ListQueuedAgentMessages(ctx, connect.NewRequest(&reliantv1.ListQueuedAgentMessagesRequest{
		ChatId: fx.chatID, ThreadId: fx.rootThreadID,
	}))
	require.NoError(t, err)
	assert.Empty(t, listed.Msg.Messages)
}
