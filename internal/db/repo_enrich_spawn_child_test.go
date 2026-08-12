package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

// TestEnrichMessageUpdate_CarriesSpawnChildWorkflowID covers the LIVE path.
//
// A spawn's preview renders the child thread's messages, and it finds that
// thread through child_workflow_id on the tool-call block. This enrichment
// builds the block payload by hand, field by field, and it did not include
// that column -- so while a spawn was actually running, the block streamed to
// the client with no thread named, the preview had nothing to read, and it sat
// at "Starting..." for the entire run. Reloading the chat fixed it, because
// the reload path assembles blocks differently and did carry the id.
//
// That is the worst shape for a bug: correct on the path you check afterwards,
// wrong on the path you watch while it happens.
func TestEnrichMessageUpdate_CarriesSpawnChildWorkflowID(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	chatID := uuid.New().String()
	threadID := uuid.New().String()
	cwID := uuid.New().String()
	messageID := uuid.New().String()
	childThreadID := uuid.New().String()
	toolCallID := "toolu_" + uuid.New().String()[:8]

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "spawn live", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &Thread{ID: threadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateThread(ctx, &Thread{
		ID: childThreadID, ChatID: chatID, ParentThreadID: &threadID,
		Origin: core.ThreadOriginSpawn, CreatedAt: now,
	})
	require.NoError(t, err)

	require.NoError(t, repo.CreateMessage(ctx, &Message{
		ID: messageID, ChatID: chatID, Ordinal: 1, ThreadID: threadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &MessageContentBlock{
		ID: uuid.New().String(), MessageID: messageID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: ptr.Of(toolCallID),
		ToolName:   ptr.Of("spawn"),
		ToolInput:  ptr.Of(`{"title":"child agent"}`),
		Version:    ptr.Of(1),
		CreatedAt:  now, UpdatedAt: now,
	}))

	// Still executing -- the state a spawn is in for the whole time its
	// preview matters.
	require.NoError(t, repo.UpsertToolCall(ctx, &ToolCall{
		ID: toolCallID, ChatID: chatID, ThreadID: &threadID, MessageID: &messageID,
		ToolName: "spawn", Status: core.ToolCallStatusExecuting,
		ChildWorkflowID: &childThreadID,
		RequestedAt:     now, CreatedAt: now, UpdatedAt: now,
	}))

	enriched, err := repo.EnrichMessageUpdate(ctx, ChatUpdate{
		ID:         uuid.New().String(),
		ChatID:     chatID,
		UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
		EntityID:   messageID,
		Data:       json.RawMessage([]byte(`{"id":"` + messageID + `"}`)),
		CreatedAt:  now,
	})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(enriched, &payload))

	blocks, ok := payload["content_blocks"].([]any)
	require.True(t, ok, "content_blocks should be present")
	require.Len(t, blocks, 1)

	block, ok := blocks[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, toolCallID, block["tool_call_id"])
	require.Equal(t, childThreadID, block["child_workflow_id"],
		"a live spawn update must name the thread it owns, or its preview has nothing to render")
	require.EqualValues(t, core.ToolCallStatusExecuting, block["tool_call_status"])
}

// TestEnrichMessageUpdate_NonSpawnToolCallHasNoChildWorkflowID keeps the field
// meaningful: it is present when a call really started a child, absent
// otherwise. An empty-string default would make "has a child thread" untestable
// at the consumer.
func TestEnrichMessageUpdate_NonSpawnToolCallHasNoChildWorkflowID(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	chatID := uuid.New().String()
	threadID := uuid.New().String()
	cwID := uuid.New().String()
	messageID := uuid.New().String()
	toolCallID := "toolu_" + uuid.New().String()[:8]

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID: chatID, Title: "plain tool", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &Thread{ID: threadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)

	require.NoError(t, repo.CreateMessage(ctx, &Message{
		ID: messageID, ChatID: chatID, Ordinal: 1, ThreadID: threadID,
		ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.CreateContentBlock(ctx, &MessageContentBlock{
		ID: uuid.New().String(), MessageID: messageID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: ptr.Of(toolCallID),
		ToolName:   ptr.Of("bash"),
		Version:    ptr.Of(1),
		CreatedAt:  now, UpdatedAt: now,
	}))
	require.NoError(t, repo.UpsertToolCall(ctx, &ToolCall{
		ID: toolCallID, ChatID: chatID, ThreadID: &threadID, MessageID: &messageID,
		ToolName: "bash", Status: core.ToolCallStatusExecuting,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))

	enriched, err := repo.EnrichMessageUpdate(ctx, ChatUpdate{
		ID: uuid.New().String(), ChatID: chatID,
		UpdateType: reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE,
		EntityID:   messageID,
		Data:       json.RawMessage([]byte(`{"id":"` + messageID + `"}`)),
		CreatedAt:  now,
	})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(enriched, &payload))
	blocks := payload["content_blocks"].([]any)
	block := blocks[0].(map[string]any)

	_, hasChild := block["child_workflow_id"]
	require.False(t, hasChild, "a tool call that started no child must not claim one")
}
