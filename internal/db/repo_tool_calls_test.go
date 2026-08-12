// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// toolCallFixture is the minimum set of parent rows a tool call needs. The
// foreign keys added in 20260801000000 mean a message cannot exist without a
// chat, a thread, and a context window, and a tool call cannot exist without
// the chat (and, if set, the thread and message) it points at.
type toolCallFixture struct {
	chatID    string
	threadID  string
	messageID string
}

func setupToolCallFixture(t *testing.T, repo *Repo) toolCallFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	chatID := uuid.New().String()
	threadID := uuid.New().String()
	cwID := uuid.New().String()
	messageID := uuid.New().String()

	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID:         chatID,
		Title:      "tool call chat",
		ProjectID:  "test-project",
		UserID:     "test-user",
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	_, err := repo.CreateThread(ctx, &Thread{
		ID:        threadID,
		ChatID:    chatID,
		CreatedAt: now,
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

	return toolCallFixture{chatID: chatID, threadID: threadID, messageID: messageID}
}

func newTestToolCall(f toolCallFixture, id string) *ToolCall {
	now := time.Now().UTC()
	return &ToolCall{
		ID:          id,
		ChatID:      f.chatID,
		ThreadID:    &f.threadID,
		MessageID:   &f.messageID,
		ToolName:    "bash",
		Input:       []byte(`{"command":"ls"}`),
		Status:      core.ToolCallStatusPending,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestUpsertAndGetToolCall(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	call := newTestToolCall(f, "toolu_"+uuid.New().String())

	require.NoError(t, repo.UpsertToolCall(ctx, call))

	got, err := repo.GetToolCall(ctx, call.ID)
	require.NoError(t, err)
	require.Equal(t, call.ID, got.ID)
	require.Equal(t, f.chatID, got.ChatID)
	require.Equal(t, f.threadID, *got.ThreadID)
	require.Equal(t, f.messageID, *got.MessageID)
	require.Equal(t, "bash", got.ToolName)
	require.JSONEq(t, `{"command":"ls"}`, string(got.Input))
	require.Equal(t, core.ToolCallStatusPending, got.Status)
	require.Nil(t, got.CompletedAt)
}

// The write path is driven by Temporal activities, which retry. A retry
// re-sending the same call must converge on one row rather than fail on the
// primary key -- that is the whole reason these are upserts.
func TestUpsertToolCallIsIdempotent(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	call := newTestToolCall(f, "toolu_"+uuid.New().String())

	require.NoError(t, repo.UpsertToolCall(ctx, call))

	completedAt := time.Now().UTC()
	call.Status = core.ToolCallStatusCompleted
	call.CompletedAt = &completedAt
	call.StartedAt = &completedAt
	require.NoError(t, repo.UpsertToolCall(ctx, call))

	got, err := repo.GetToolCall(ctx, call.ID)
	require.NoError(t, err)
	require.Equal(t, core.ToolCallStatusCompleted, got.Status)
	require.NotNil(t, got.CompletedAt)

	// Still exactly one row for the chat, not two.
	all, err := repo.ListToolCallsByChat(ctx, f.chatID)
	require.NoError(t, err)
	require.Len(t, all, 1)
}

// The CHECK constraint exists so a row cannot claim to be COMPLETED without a
// completion time. The repository rejects it earlier with a named error, but
// both layers must hold.
func TestUpsertToolCallRejectsCompletedWithoutCompletedAt(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	call := newTestToolCall(f, "toolu_"+uuid.New().String())
	call.Status = core.ToolCallStatusCompleted
	call.CompletedAt = nil

	err := repo.UpsertToolCall(ctx, call)
	require.Error(t, err)
	require.Contains(t, err.Error(), "completed_at")
}

func TestUpsertToolCallResultIsIdempotent(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	call := newTestToolCall(f, "toolu_"+uuid.New().String())
	require.NoError(t, repo.UpsertToolCall(ctx, call))

	now := time.Now().UTC()
	result := &ToolCallResult{
		ToolCallID: call.ID,
		MessageID:  &f.messageID,
		Content:    "first",
		IsError:    false,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	require.NoError(t, repo.UpsertToolCallResult(ctx, result))

	result.Content = "second"
	result.IsError = true
	result.UpdatedAt = time.Now().UTC()
	require.NoError(t, repo.UpsertToolCallResult(ctx, result))

	results, err := repo.ListToolCallResultsByMessageIDs(ctx, []string{f.messageID})
	require.NoError(t, err)
	require.Len(t, results, 1, "a second result for the same call must replace, not duplicate")
	require.Equal(t, "second", results[0].Content)
	require.True(t, results[0].IsError)
}

// The primary key + foreign key on tool_call_results is what makes an orphan
// result unrepresentable. Without it, the product's hard invariant (never send
// the LLM a tool_use block without a matching tool_result) relies on repair
// code noticing after the fact.
func TestToolCallResultRequiresExistingCall(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	now := time.Now().UTC()

	err := repo.UpsertToolCallResult(ctx, &ToolCallResult{
		ToolCallID: "toolu_does_not_exist",
		MessageID:  &f.messageID,
		Content:    "orphan",
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	require.Error(t, err, "a result for a nonexistent call must be rejected by the foreign key")
}

// Deleting the call must take its result with it; a result that outlives its
// call is the orphan case in the other direction.
func TestDeletingToolCallCascadesToResult(t *testing.T) {
	repo, db, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	call := newTestToolCall(f, "toolu_"+uuid.New().String())
	require.NoError(t, repo.UpsertToolCall(ctx, call))

	now := time.Now().UTC()
	require.NoError(t, repo.UpsertToolCallResult(ctx, &ToolCallResult{
		ToolCallID: call.ID,
		MessageID:  &f.messageID,
		Content:    "result",
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	_, err := db.ExecContext(ctx, "DELETE FROM tool_calls WHERE id = $1", call.ID)
	require.NoError(t, err)

	var count int
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT count(*) FROM tool_call_results WHERE tool_call_id = $1", call.ID,
	).Scan(&count))
	require.Zero(t, count)
}

// The batch reads are the read path's entry point, and sqlc's Postgres
// slice codegen is broken (it emits IN ($1) and only ever matches the first
// id), so these stores hand-build the IN clause. This test is what catches a
// regression back to the generated version.
func TestListToolCallsByMessageIDsMatchesEveryID(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f1 := setupToolCallFixture(t, repo)
	f2 := setupToolCallFixture(t, repo)
	f3 := setupToolCallFixture(t, repo)

	for _, f := range []toolCallFixture{f1, f2, f3} {
		call := newTestToolCall(f, "toolu_"+uuid.New().String())
		require.NoError(t, repo.UpsertToolCall(ctx, call))

		now := time.Now().UTC()
		require.NoError(t, repo.UpsertToolCallResult(ctx, &ToolCallResult{
			ToolCallID: call.ID,
			MessageID:  &f.messageID,
			Content:    "ok",
			CreatedAt:  now,
			UpdatedAt:  now,
		}))
	}

	ids := []string{f1.messageID, f2.messageID, f3.messageID}

	calls, err := repo.ListToolCallsByMessageIDs(ctx, ids)
	require.NoError(t, err)
	require.Len(t, calls, 3, "every message id in the batch must match, not just the first")

	results, err := repo.ListToolCallResultsByMessageIDs(ctx, ids)
	require.NoError(t, err)
	require.Len(t, results, 3)
}

func TestListToolCallsByMessageIDsEmptyInput(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	calls, err := repo.ListToolCallsByMessageIDs(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, calls)

	results, err := repo.ListToolCallResultsByMessageIDs(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestListToolCallsByChatOrdersByRequestedAt(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	base := time.Now().UTC()

	for i, offset := range []time.Duration{2 * time.Minute, 0, time.Minute} {
		call := newTestToolCall(f, "toolu_order_"+uuid.New().String())
		call.RequestedAt = base.Add(offset)
		call.ToolName = []string{"third", "first", "second"}[i]
		require.NoError(t, repo.UpsertToolCall(ctx, call))
	}

	calls, err := repo.ListToolCallsByChat(ctx, f.chatID)
	require.NoError(t, err)
	require.Len(t, calls, 3)
	require.Equal(t, "first", calls[0].ToolName)
	require.Equal(t, "second", calls[1].ToolName)
	require.Equal(t, "third", calls[2].ToolName)
}

// Input is nullable because tool arguments have not finished arriving while
// the assistant message is still streaming.
func TestToolCallWithNullInput(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	call := newTestToolCall(f, "toolu_"+uuid.New().String())
	call.Input = nil

	require.NoError(t, repo.UpsertToolCall(ctx, call))

	got, err := repo.GetToolCall(ctx, call.ID)
	require.NoError(t, err)
	require.Nil(t, got.Input)
}

func TestGetToolCallNotFound(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	_, err := repo.GetToolCall(ctx, "toolu_missing")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}
