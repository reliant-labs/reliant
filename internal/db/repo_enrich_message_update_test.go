package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

func TestEnrichMessageUpdate_IncludesFileReferenceAttachments(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	chatID := uuid.New().String()
	threadID := uuid.New().String()
	cwID := uuid.New().String()
	messageID := uuid.New().String()
	attachmentID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID:         chatID,
		Title:      "Attachment chat",
		ProjectID:  "test-project",
		UserID:     "test-user",
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	_, err := repo.CreateThread(ctx, &Thread{
		ID:             threadID,
		ConversationID: chatID,
		CreatedAt:      now,
	})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cwID,
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: now,
	})
	require.NoError(t, err)

	require.NoError(t, repo.CreateMessage(ctx, &Message{
		ID:              messageID,
		ChatID:          chatID,
		Ordinal:         1,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	require.NoError(t, repo.CreateAttachment(ctx, &Attachment{
		ID:             attachmentID,
		UserID:         "test-user",
		Filename:       "payload.json",
		Size:           128,
		MimeType:       "application/json",
		FilePath:       "test-user/payload.json",
		AttachmentType: "file_reference",
		CreatedAt:      now,
		UpdatedAt:      now,
	}))

	require.NoError(t, repo.CreateContentBlock(ctx, &MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: messageID,
		Position:  0,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE,
		Content:   &attachmentID,
		Version:   ptr.Of(1),
		CreatedAt: now,
		UpdatedAt: now,
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

	attachments, ok := payload["attachments"].([]any)
	require.True(t, ok, "attachments should be present")
	require.Len(t, attachments, 1)

	attachmentMap, ok := attachments[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, attachmentID, attachmentMap["id"])
	require.Equal(t, "payload.json", attachmentMap["filename"])
	require.Equal(t, "application/json", attachmentMap["mime_type"])
}

func TestEnrichMessageUpdate_UsesCanonicalToolCallInputField(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now()

	chatID := uuid.New().String()
	threadID := uuid.New().String()
	cwID := uuid.New().String()
	messageID := uuid.New().String()
	inputJSON := `{"edits":[{"file_path":"foo.txt","old_string":"a","new_string":"b"}]}`

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID:         chatID,
		Title:      "Tool input chat",
		ProjectID:  "test-project",
		UserID:     "test-user",
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	_, err := repo.CreateThread(ctx, &Thread{
		ID:             threadID,
		ConversationID: chatID,
		CreatedAt:      now,
	})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &ContextWindow{
		ID:        cwID,
		ThreadID:  threadID,
		Sequence:  0,
		CreatedAt: now,
	})
	require.NoError(t, err)

	require.NoError(t, repo.CreateMessage(ctx, &Message{
		ID:              messageID,
		ChatID:          chatID,
		Ordinal:         1,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	toolName := "edit"
	toolCallID := "tc-enrich"
	require.NoError(t, repo.CreateContentBlock(ctx, &MessageContentBlock{
		ID:         uuid.New().String(),
		MessageID:  messageID,
		Position:   0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:   &toolName,
		ToolCallID: &toolCallID,
		ToolInput:  &inputJSON,
		Version:    ptr.Of(1),
		CreatedAt:  now,
		UpdatedAt:  now,
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

	blocksAny, ok := payload["content_blocks"].([]any)
	require.True(t, ok, "content_blocks should be present")
	require.Len(t, blocksAny, 1)

	block, ok := blocksAny[0].(map[string]any)
	require.True(t, ok)

	input, hasInput := block["input"]
	require.True(t, hasInput, "expected canonical input field in enriched content block")
	require.Equal(t, inputJSON, input)

	_, hasLegacy := block["tool_input"]
	require.False(t, hasLegacy, "legacy tool_input should not be present")
}
