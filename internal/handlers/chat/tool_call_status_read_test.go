package chat

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/db/dbtest"
	"github.com/reliant-labs/reliant/internal/handlers/message"
	"github.com/reliant-labs/reliant/internal/store"
	typedconfig "github.com/reliant-labs/reliant/pkg/config"
)

// Status is durable in tool_calls, but before this the read path never
// projected it onto the content block, so a reload had nothing but workflow
// activity to infer status from. These tests read the RPC response after a
// separate write to the tool_calls table -- the reload's view -- rather than
// reading back a value through the same code path that wrote it.

func TestGetMessage_PopulatesDurableToolCallStatus(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := dbtest.SeedToolCall(t, ctx, repo, `{"command":"sleep 600"}`)

	require.NoError(t, repo.UpsertToolCall(ctx, &core.ToolCall{
		ID:          f.ToolCallID,
		ChatID:      f.ChatID,
		MessageID:   &f.MessageID,
		ThreadID:    &f.ThreadID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusExecuting,
		RequestedAt: time.Now().UTC(),
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}))

	svc, err := message.New(message.Deps{
		Logger:  slog.Default(),
		Config:  &typedconfig.Config{},
		Repo:    repo,
		Chats:   store.NewChatExtStore(repo.ORM()),
		Senders: store.NewThreadExtStore(repo.ORM()),
	})
	require.NoError(t, err)
	resp, err := svc.GetMessage(ctx, connect.NewRequest(&reliantv1.GetMessageRequest{
		MessageId: f.MessageID,
	}))
	require.NoError(t, err)

	require.Len(t, resp.Msg.Message.ContentBlocks, 1)
	block := resp.Msg.Message.ContentBlocks[0]
	require.NotNil(t, block.ToolCallStatus, "durable status must be projected onto the tool_call block")
	assert.Equal(t, reliantv1.ToolCallStatus_TOOL_CALL_STATUS_EXECUTING, *block.ToolCallStatus)
}

func TestListMessages_PopulatesDurableToolCallStatus(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := dbtest.SeedToolCall(t, ctx, repo, `{"command":"npm run dev"}`)

	completedAt := time.Now().UTC()
	require.NoError(t, repo.UpsertToolCall(ctx, &core.ToolCall{
		ID:          f.ToolCallID,
		ChatID:      f.ChatID,
		MessageID:   &f.MessageID,
		ThreadID:    &f.ThreadID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusCompleted,
		CompletedAt: &completedAt,
		RequestedAt: time.Now().UTC(),
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}))

	svc := &Service{deps: Deps{Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}}
	resp, err := svc.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{
		ChatId: f.ChatID,
	}))
	require.NoError(t, err)

	var block *reliantv1.ContentBlock
	for _, msg := range resp.Msg.Messages {
		for _, b := range msg.ContentBlocks {
			if b.ToolCallId != nil && *b.ToolCallId == f.ToolCallID {
				block = b
			}
		}
	}
	require.NotNil(t, block, "expected the tool_call block to be present in the message list")
	require.NotNil(t, block.ToolCallStatus)
	assert.Equal(t, reliantv1.ToolCallStatus_TOOL_CALL_STATUS_COMPLETED, *block.ToolCallStatus)
	require.NotNil(t, block.CompletedAt)
}
