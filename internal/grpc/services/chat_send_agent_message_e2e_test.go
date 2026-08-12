// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// TestSendAgentMessage_EndToEndDelivery exercises the full path a real user
// action drives: SendAgentMessage enqueues into agent_messages, then the
// SAME drain activity the agent-loop step boundary calls
// (DrainAgentMessagesActivity, internal/workflow/runtime/agent_mailbox.go)
// folds it into the target thread's history. This is the equivalent of a
// live grpcurl call against the running server followed by the next agent
// loop iteration, without needing a live Temporal workflow to actually be
// mid-turn.
func TestSendAgentMessage_EndToEndDelivery(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}

	// A queued message must carry attachments exactly like an ordinary
	// SendMessage turn -- this is the screenshot the user tried to queue
	// and was refused.
	attachmentID := uuid.New().String()
	require.NoError(t, repo.CreateAttachment(ctx, &db.Attachment{
		ID:             attachmentID,
		Filename:       "screenshot.png",
		Size:           42,
		MimeType:       "image/png",
		AttachmentType: "image",
		Content:        []byte("fake-png-bytes"),
	}))

	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId:      fx.chatID,
		ThreadId:    fx.childThreadID,
		Message:     "stop and wait for a follow-up before touching the migration",
		Attachments: []string{attachmentID},
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)

	// Nothing delivered yet -- it is queued, not synchronous.
	msgsBefore, err := repo.ListMessages(context.Background(), fx.chatID, db.MessageListOptions{})
	require.NoError(t, err)
	for _, m := range msgsBefore {
		assert.NotEqual(t, fx.childThreadID, m.ThreadID, "message must not be delivered before the drain runs")
	}

	// Simulate the agent-loop step boundary: drain the child thread's
	// mailbox, via a real Temporal test activity environment (Execute reads
	// activity.GetLogger, which panics outside one).
	drainActivity := handlers.NewDrainAgentMessagesActivity(repo, threads.NewService(repo))
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(drainActivity.Execute)
	val, err := env.ExecuteActivity(drainActivity.Execute, handlers.DrainAgentMessagesInput{
		ChatID: fx.chatID,
		Thread: fx.childThreadID,
	})
	require.NoError(t, err)
	var output handlers.DrainAgentMessagesOutput
	require.NoError(t, val.Get(&output))
	assert.Equal(t, 1, output.Count)
	assert.True(t, output.HasMessages)

	// The message must now be in the CHILD thread's history. The drain writes
	// two rows: a HIDDEN <system> envelope carrying the attribution, then the
	// sender's text as its own visible message. The framing is kept out of
	// the body so the transcript renders the message the user actually wrote,
	// rather than the envelope markup around it.
	msgsAfter, err := repo.ListMessages(context.Background(), fx.chatID, db.MessageListOptions{})
	require.NoError(t, err)
	var childMsgs []*db.Message
	for _, m := range msgsAfter {
		if m.ThreadID == fx.childThreadID {
			childMsgs = append(childMsgs, m)
		}
	}
	require.Len(t, childMsgs, 2, "expected a hidden envelope followed by the delivered body")

	contentOf := func(m *db.Message) string {
		blocks, err := repo.ListContentBlocks(context.Background(), m.ID)
		require.NoError(t, err)
		require.Len(t, blocks, 1)
		require.NotNil(t, blocks[0].Content)
		return *blocks[0].Content
	}

	envelope, delivered := childMsgs[0], childMsgs[1]
	require.NotNil(t, envelope.DisplayStyle)
	assert.Equal(t, reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN, *envelope.DisplayStyle,
		"the envelope is LLM-only framing and must stay out of the transcript")
	assert.Contains(t, contentOf(envelope), `from="user"`,
		"a human-sent message must be labeled distinctly from an agent-to-agent spawn_send")

	// The delivered body must carry the same content blocks a normal
	// SendMessage would: the text block, followed by an image block
	// referencing the attachment ID. This is what makes the screenshot the
	// user tried to queue actually reach the LLM instead of being silently
	// dropped.
	deliveredBlocks, err := repo.ListContentBlocks(context.Background(), delivered.ID)
	require.NoError(t, err)
	require.Len(t, deliveredBlocks, 2, "expected a text block plus an image block for the attachment")
	require.NotNil(t, deliveredBlocks[0].Content)
	assert.Equal(t, "stop and wait for a follow-up before touching the migration", *deliveredBlocks[0].Content,
		"the delivered body must be the user's text alone")
	assert.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE, deliveredBlocks[1].BlockType,
		"the attachment must survive the drain fold as the same block type a direct send would produce")
	require.NotNil(t, deliveredBlocks[1].Content)
	assert.Equal(t, attachmentID, *deliveredBlocks[1].Content,
		"the image block must reference the attachment the user queued")
	assert.Nil(t, delivered.DisplayStyle, "the delivered body must be visible in the transcript")

	// The row must be marked delivered so a second drain does not redeliver it.
	queued, err := repo.ListQueuedAgentMessagesForThread(context.Background(), fx.childThreadID)
	require.NoError(t, err)
	assert.Empty(t, queued)
}
