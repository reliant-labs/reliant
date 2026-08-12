// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// The three writers of tool status each know only part of a call. This is the
// property that lets them interleave: a later writer that omits a field must
// not blank what an earlier one recorded.
func TestUpsertToolCallStatusPreservesFieldsAcrossWriters(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	callID := "toolu_" + uuid.New().String()
	startedAt := time.Now().UTC()

	// Writer 1 (the execute_tools activity) knows the input, thread, message
	// and start time.
	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:          callID,
		ChatID:      f.chatID,
		ThreadID:    &f.threadID,
		MessageID:   &f.messageID,
		ToolName:    "bash",
		Input:       []byte(`{"command":"sleep 60"}`),
		Status:      core.ToolCallStatusExecuting,
		StartedAt:   &startedAt,
		RequestedAt: startedAt,
		CreatedAt:   startedAt,
		UpdatedAt:   startedAt,
	}))

	// Writer 2 (the CancelToolCall handler) knows only that it was cancelled.
	completedAt := time.Now().UTC()
	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:          callID,
		ChatID:      f.chatID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusCancelled,
		CompletedAt: &completedAt,
		RequestedAt: completedAt,
		CreatedAt:   completedAt,
		UpdatedAt:   completedAt,
	}))

	got, err := repo.GetToolCall(ctx, callID)
	require.NoError(t, err)

	// The second writer's status wins.
	require.Equal(t, core.ToolCallStatusCancelled, got.Status)
	require.NotNil(t, got.CompletedAt)

	// Everything it did not know is still there.
	require.NotNil(t, got.StartedAt, "started_at must survive a writer that doesn't know it")
	require.Equal(t, f.threadID, *got.ThreadID)
	require.Equal(t, f.messageID, *got.MessageID)
	require.JSONEq(t, `{"command":"sleep 60"}`, string(got.Input))
	require.Equal(t, startedAt.Unix(), got.RequestedAt.Unix(),
		"requested_at is set by the first write and never moves")
}

// A caller that does supply a field overrides the persisted one -- inheritance
// fills gaps, it does not freeze values.
func TestUpsertToolCallStatusCallerValuesWin(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	callID := "toolu_" + uuid.New().String()
	now := time.Now().UTC()

	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:          callID,
		ChatID:      f.chatID,
		ToolName:    "bash",
		Input:       []byte(`{"command":"first"}`),
		Status:      core.ToolCallStatusPending,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	errMsg := "boom"
	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:           callID,
		ChatID:       f.chatID,
		ToolName:     "bash",
		Input:        []byte(`{"command":"second"}`),
		Status:       core.ToolCallStatusFailed,
		ErrorMessage: &errMsg,
		RequestedAt:  now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}))

	got, err := repo.GetToolCall(ctx, callID)
	require.NoError(t, err)
	require.JSONEq(t, `{"command":"second"}`, string(got.Input))
	require.Equal(t, core.ToolCallStatusFailed, got.Status)
	require.Equal(t, "boom", *got.ErrorMessage)
}

// The first write for a call has no existing row to read. That is the normal
// case, not an error.
// A call that reached a terminal status has its outcome. A later writer that
// still thinks it is running must not resurrect it: that is what leaves the UI
// spinning on a call that already finished.
func TestUpsertToolCallStatusTerminalIsOneWay(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	callID := "toolu_" + uuid.New().String()
	now := time.Now().UTC()

	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:          callID,
		ChatID:      f.chatID,
		ToolName:    "spawn",
		Status:      core.ToolCallStatusCompleted,
		CompletedAt: &now,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	// A racing writer that still believes the call is executing.
	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:          callID,
		ChatID:      f.chatID,
		ToolName:    "spawn",
		Status:      core.ToolCallStatusExecuting,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	got, err := repo.GetToolCall(ctx, callID)
	require.NoError(t, err)
	require.Equal(t, core.ToolCallStatusCompleted, got.Status,
		"a non-terminal write must not move a terminal call back to running")
	require.NotNil(t, got.CompletedAt)

	// One terminal status may still correct another: a cancel that lands after
	// a FAILURE is a real transition, not a stale one. (The one pairing that is
	// not a correction -- completed downgraded to cancelled -- has its own test
	// below; a finished tool's result must not be erased by a cancel aimed at
	// one of its siblings.)
	failedCallID := "toolu_" + uuid.New().String()
	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:          failedCallID,
		ChatID:      f.chatID,
		ToolName:    "spawn",
		Status:      core.ToolCallStatusFailed,
		CompletedAt: &now,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:          failedCallID,
		ChatID:      f.chatID,
		ToolName:    "spawn",
		Status:      core.ToolCallStatusCancelled,
		CompletedAt: &now,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	got, err = repo.GetToolCall(ctx, failedCallID)
	require.NoError(t, err)
	require.Equal(t, core.ToolCallStatusCancelled, got.Status,
		"terminal-to-terminal transitions must still be recorded")
}

// Cancelling one tool must not erase a sibling that already finished.
//
// All of a turn's tool calls execute as parallel goroutines under one shared
// context, so a cancellation aimed at one of them used to arrive for all of
// them -- and because CANCELLED is itself terminal, the one-way-door guard
// above let it overwrite a COMPLETED row. The tool's real output was then gone
// from the durable record, and a reload showed a finished tool as cancelled.
func TestUpsertToolCallStatusCompletedIsNotDowngradedToCancelled(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	callID := "toolu_" + uuid.New().String()
	now := time.Now().UTC()

	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:          callID,
		ChatID:      f.chatID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusCompleted,
		CompletedAt: &now,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	// A cancel aimed at a sibling tool, arriving after this one completed.
	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:          callID,
		ChatID:      f.chatID,
		ToolName:    "bash",
		Status:      core.ToolCallStatusCancelled,
		CompletedAt: &now,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	got, err := repo.GetToolCall(ctx, callID)
	require.NoError(t, err)
	require.Equal(t, core.ToolCallStatusCompleted, got.Status,
		"a completed tool call must not be relabelled cancelled")
}

func TestUpsertToolCallStatusFirstWriteHasNoExistingRow(t *testing.T) {
	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	f := setupToolCallFixture(t, repo)
	callID := "toolu_" + uuid.New().String()
	now := time.Now().UTC()

	require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
		ID:          callID,
		ChatID:      f.chatID,
		ToolName:    "grep",
		Status:      core.ToolCallStatusPending,
		RequestedAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))

	got, err := repo.GetToolCall(ctx, callID)
	require.NoError(t, err)
	require.Equal(t, core.ToolCallStatusPending, got.Status)
	require.Equal(t, "grep", got.ToolName)
}
