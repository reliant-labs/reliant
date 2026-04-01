// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// TestDualWrite_Simple validates the dual-write pattern without Temporal infrastructure
func TestDualWrite_Simple(t *testing.T) {
	ctx := context.Background()

	// Create temporary database
	tmpDir := t.TempDir()

	repo, err := db.NewRepoFromDir(tmpDir)
	require.NoError(t, err)
	defer repo.Close()

	t.Run("UserMessageDualWrite", func(t *testing.T) {
		chatID := uuid.New().String()
		messageID := uuid.New().String()
		projectID := uuid.New().String()
		worktreeID := uuid.New().String()
		now := time.Now().UTC()

		// Create project first
		project := &db.Project{
			ID:        projectID,
			Name:      "test-project-1",
			Path:      "/tmp/test-" + projectID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		err := repo.CreateProject(ctx, project)
		require.NoError(t, err)

		// Create worktree
		worktree := &db.Worktree{
			ID:         worktreeID,
			ProjectID:  projectID,
			Name:       "main",
			Path:       "/tmp/test",
			Branch:     "main",
			Status:     int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
			IsMain:     true,
			CreatedAt:  now,
			UpdatedAt:  now,
			LastActive: now,
		}
		err = repo.CreateWorktree(ctx, worktree)
		require.NoError(t, err)

		// Create chat
		// Note: Agent is now a workflow param, not stored on chat
		chat := &db.Chat{
			ID:         chatID,
			ProjectID:  projectID,
			WorktreeID: &worktreeID,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		err = repo.CreateChat(ctx, chat)
		require.NoError(t, err)

		// Create thread and context window for the message
		threadID := "0"
		_, err = repo.CreateThread(ctx, &db.Thread{
			ID:             threadID,
			ConversationID: chatID,
			CreatedAt:      now,
		})
		require.NoError(t, err)

		contextWindowID := fmt.Sprintf("%s:%s:0", chatID, threadID)
		_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
			ID:        contextWindowID,
			ThreadID:  threadID,
			Sequence:  0,
			CreatedAt: now,
		})
		require.NoError(t, err)

		// Create user message with dual-write
		err = repo.RunTx(ctx, func(txCtx context.Context) error {
			// Create message
			msg := &db.Message{
				ID:              messageID,
				ChatID:          chatID,
				Ordinal:         0,
				ThreadID:        threadID,
				ContextWindowID: contextWindowID,
				Role:            reliantv1.MessageRole_MESSAGE_ROLE_USER,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			if err := repo.CreateMessage(txCtx, msg); err != nil {
				return err
			}

			// Create content block
			blockID := uuid.New().String()
			content := "hello world"
			block := &db.MessageContentBlock{
				ID:        blockID,
				MessageID: messageID,
				Position:  0,
				BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
				Content:   &content,
				Version:   ptr.Of(1),
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := repo.CreateContentBlock(txCtx, block); err != nil {
				return err
			}

			// DUAL-WRITE to chat_updates
			blocks, err := repo.ListContentBlocks(txCtx, messageID)
			if err != nil {
				return err
			}

			contentBlocks := []map[string]interface{}{}
			for _, b := range blocks {
				blockData := map[string]interface{}{
					"id":    b.ID,
					"type":  b.BlockType,
					"index": b.Position,
				}
				if b.Content != nil {
					blockData["content"] = *b.Content
				}
				contentBlocks = append(contentBlocks, blockData)
			}

			updateData := map[string]interface{}{
				"update_type":      "message",
				"id":               messageID,
				"role":             "user",
				"ordinal":          0,
				"thread":           "0",
				"context_sequence": 0,
				"created_at":       now.Format(time.RFC3339Nano),
				"updated_at":       now.Format(time.RFC3339Nano),
				"content_blocks":   contentBlocks,
			}

			updateDataJSON, err := json.Marshal(updateData)
			if err != nil {
				return err
			}

			return repo.CreateChatUpdate(txCtx, chatID, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, messageID, string(updateDataJSON))
		})
		require.NoError(t, err)

		// Verify message exists
		msg, err := repo.GetMessage(ctx, messageID)
		require.NoError(t, err)
		require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, msg.Role)
		require.Equal(t, chatID, msg.ChatID)

		// Verify content blocks exist
		blocks, err := repo.ListContentBlocks(ctx, messageID)
		require.NoError(t, err)
		require.Len(t, blocks, 1)
		require.Equal(t, reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, blocks[0].BlockType)
		require.Equal(t, "hello world", *blocks[0].Content)

		// Verify chat_update exists
		updates, err := repo.GetUpdatesSince(ctx, chatID, 0, 10)
		require.NoError(t, err)
		require.Len(t, updates, 1)
		require.Equal(t, reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, updates[0].UpdateType)
		require.Equal(t, messageID, updates[0].EntityID)

		// Parse and validate JSON data
		var updateData map[string]interface{}
		err = json.Unmarshal([]byte(updates[0].Data), &updateData)
		require.NoError(t, err)
		require.Equal(t, "message", updateData["update_type"])
		require.Equal(t, "user", updateData["role"])
		require.Equal(t, messageID, updateData["id"])

		contentBlocks, ok := updateData["content_blocks"].([]interface{})
		require.True(t, ok)
		require.Len(t, contentBlocks, 1)

		firstBlock := contentBlocks[0].(map[string]interface{})
		require.Equal(t, float64(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT), firstBlock["type"])
		require.Equal(t, "hello world", firstBlock["content"])

		t.Log("✓ User message dual-write validated")
	})

	// NOTE: StreamingDeltaDualWrite subtest removed - streaming deltas are now ephemeral
	// and handled via StreamingHub rather than persisted to database.
}
