// Copyright (c) 2025 Reliant Labs

// Package dbtest seeds the test database (db.SetupTestDB) with the shapes
// several handler services' tests start from, so each service's tests can
// move to their own package (research/forge-migration/05-p3a-handler-map.md).
// Only _test.go files import it.
//
//forge:exclude-contract: test fixtures that seed db.Repo, imported only by tests; nothing to fake behind them
package dbtest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// ToolCall is what SeedToolCall created: a chat with one thread, one
// assistant message, and on it one tool_call content block.
type ToolCall struct {
	Repo       *db.Repo
	ChatID     string
	ThreadID   string
	MessageID  string
	ToolCallID string
}

// SeedToolCall creates a chat owned by "test-user" in the seeded
// "test-project", with one assistant message carrying a "bash" tool_call
// block whose input is toolInput. It creates no tool_calls row.
func SeedToolCall(t *testing.T, ctx context.Context, repo *db.Repo, toolInput string) ToolCall {
	t.Helper()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	threadID := uuid.New().String()
	cwID := uuid.New().String()
	messageID := uuid.New().String()
	blockID := uuid.New().String()
	toolCallID := "toolu_" + uuid.New().String()
	toolName := "bash"

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		Title:      "durable status",
		ProjectID:  "test-project",
		UserID:     "test-user",
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	_, err := repo.CreateThread(ctx, &db.Thread{ID: threadID, ChatID: chatID, CreatedAt: now})
	require.NoError(t, err)

	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: cwID, ThreadID: threadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)

	require.NoError(t, repo.CreateMessage(ctx, &db.Message{
		ID:              messageID,
		ChatID:          chatID,
		Ordinal:         1,
		Seq:             1,
		ThreadID:        threadID,
		ContextWindowID: cwID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	require.NoError(t, repo.CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:         blockID,
		MessageID:  messageID,
		Position:   0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolCallID: &toolCallID,
		ToolName:   &toolName,
		ToolInput:  &toolInput,
		Version:    ptr.Of(1),
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	return ToolCall{
		Repo:       repo,
		ChatID:     chatID,
		ThreadID:   threadID,
		MessageID:  messageID,
		ToolCallID: toolCallID,
	}
}
