// Copyright (c) 2026 Reliant Labs

package dbtest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// TranscriptBodies is what SeedTranscriptBodies created: a chat whose
// transcript holds every kind of body a TOOL_BODY_MODE_STUB read treats
// differently (dev-docs/design/chat-lazy-tool-bodies.md).
type TranscriptBodies struct {
	ChatID, ThreadID, ContextWindowID string
	// The assistant message and the tool message after it.
	AssistantMessageID, ToolMessageID string

	// Block ids, and the bodies stored in them.
	TextBlockID        string
	ThinkingBlockID    string
	DoneCallBlockID    string // a finished edit with a large input
	RunningCallBlockID string // a shell call still running, large input
	SmallCallBlockID   string // a finished view under the threshold
	PlanCallBlockID    string // a finished create_plan (always full), large input
	ResultBlockID      string // the edit's large result

	DoneCallID, RunningCallID, SmallCallID, PlanCallID string

	Thinking, DoneInput, RunningInput, SmallInput, PlanInput, Result string
}

// SeedTranscriptBodies creates a chat for userID in "test-project" with one
// thread and context window and two messages:
//
//   - assistant: a text block, a large thinking block, and four tool_call
//     blocks — a finished edit with a large input, a running shell call with a
//     large input, a finished small view, and a finished create_plan with a
//     large input — each with its durable tool_calls row;
//   - tool: the edit's large tool_result.
func SeedTranscriptBodies(t *testing.T, ctx context.Context, repo *db.Repo, userID string) TranscriptBodies {
	t.Helper()
	now := time.Now().UTC()
	id := func() string { return uuid.New().String() }
	chatID := id()
	f := TranscriptBodies{
		// The thread is the chat's main thread (a chat with no workflow reads
		// its own id as one), so a running call is this thread's own.
		ChatID: chatID, ThreadID: chatID, ContextWindowID: id(),
		AssistantMessageID: id(), ToolMessageID: id(),
		TextBlockID: id(), ThinkingBlockID: id(), DoneCallBlockID: id(), RunningCallBlockID: id(),
		SmallCallBlockID: id(), PlanCallBlockID: id(), ResultBlockID: id(),
		DoneCallID: "toolu_" + id(), RunningCallID: "toolu_" + id(), SmallCallID: "toolu_" + id(), PlanCallID: "toolu_" + id(),
	}
	mustJSON := func(v any) string {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		return string(b)
	}
	f.Thinking = strings.Repeat("Considering which file to change. ", 100)
	f.DoneInput = mustJSON(map[string]any{
		"file_path":  "internal/messageproto/stub.go",
		"old_string": strings.Repeat("old line\n", 400),
		"new_string": strings.Repeat("new line\n", 400),
	})
	f.RunningInput = mustJSON(map[string]any{
		"command":     "cat <<'EOF' > /tmp/x\n" + strings.Repeat("heredoc body\n", 500) + "EOF",
		"description": "Write the fixture",
	})
	f.SmallInput = mustJSON(map[string]any{"file_path": "README.md"})
	f.PlanInput = mustJSON(map[string]any{
		"title": "Plan", "description": strings.Repeat("a step of the plan. ", 200),
	})
	f.Result = strings.Repeat("edit applied; diff line\n", 2000)

	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: f.ChatID, Title: "transcript bodies", ProjectID: "test-project", UserID: userID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: f.ThreadID, ChatID: f.ChatID, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID: f.ContextWindowID, ThreadID: f.ThreadID, CreatedAt: now,
	})
	require.NoError(t, err)

	message := func(msgID string, ordinal int64, role reliantv1.MessageRole) {
		require.NoError(t, repo.CreateMessage(ctx, &db.Message{
			ID: msgID, ChatID: f.ChatID, Ordinal: ordinal, Seq: ordinal, ThreadID: f.ThreadID,
			ContextWindowID: f.ContextWindowID, Role: role, CreatedAt: now, UpdatedAt: now,
		}))
	}
	block := func(b *db.MessageContentBlock) {
		b.Version = ptr.Of(1)
		b.CreatedAt, b.UpdatedAt = now, now
		require.NoError(t, repo.CreateContentBlock(ctx, b))
	}
	call := func(blockID, msgID string, pos int, callID, tool, input string, status core.ToolCallStatus) {
		block(&db.MessageContentBlock{
			ID: blockID, MessageID: msgID, Position: pos,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: ptr.Of(callID), ToolName: ptr.Of(tool), ToolInput: ptr.Of(input),
		})
		row := &db.ToolCall{
			ID: callID, ChatID: f.ChatID, ThreadID: ptr.Of(f.ThreadID), MessageID: ptr.Of(msgID),
			ToolName: tool, Status: status, RequestedAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
		}
		if status.IsTerminal() {
			row.CompletedAt = &now
		}
		require.NoError(t, repo.UpsertToolCall(ctx, row))
	}

	message(f.AssistantMessageID, 1, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)
	block(&db.MessageContentBlock{
		ID: f.TextBlockID, MessageID: f.AssistantMessageID, Position: 0,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: ptr.Of("Editing the stub."),
	})
	block(&db.MessageContentBlock{
		ID: f.ThinkingBlockID, MessageID: f.AssistantMessageID, Position: 1,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING, Content: ptr.Of(f.Thinking),
	})
	call(f.DoneCallBlockID, f.AssistantMessageID, 2, f.DoneCallID, "edit", f.DoneInput, core.ToolCallStatusCompleted)
	call(f.RunningCallBlockID, f.AssistantMessageID, 3, f.RunningCallID, "shell", f.RunningInput, core.ToolCallStatusExecuting)
	call(f.SmallCallBlockID, f.AssistantMessageID, 4, f.SmallCallID, "view", f.SmallInput, core.ToolCallStatusCompleted)
	call(f.PlanCallBlockID, f.AssistantMessageID, 5, f.PlanCallID, "create_plan", f.PlanInput, core.ToolCallStatusCompleted)

	message(f.ToolMessageID, 2, reliantv1.MessageRole_MESSAGE_ROLE_TOOL)
	block(&db.MessageContentBlock{
		ID: f.ResultBlockID, MessageID: f.ToolMessageID, Position: 0,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
		ToolCallID: ptr.Of(f.DoneCallID), ToolName: ptr.Of("edit"), Content: ptr.Of(f.Result), IsError: ptr.Of(false),
	})
	return f
}
