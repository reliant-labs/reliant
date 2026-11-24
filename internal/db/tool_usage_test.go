// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llmusage"
	"github.com/reliant-labs/reliant/internal/llmusage/llmusagetest"
	"github.com/reliant-labs/reliant/internal/store"
)

func toolRec(id string, attempt int32, at time.Time) store.ToolCallUsageRecord {
	return store.ToolCallUsageRecord{
		ID: id, Attempt: attempt, UserID: "u1", ChatID: "chat-1", ProjectID: "proj-1",
		WorkflowID: "wf-1", AIRunID: "run-1", AIRootRunID: "run-1",
		ToolName: "shell", ToolKind: store.ToolKindBuiltin, Location: "daemon",
		Outcome: store.ToolOutcomeSuccess, DurationMs: 420, CreatedAt: at,
	}
}

// A tool call's attempts are each recorded, a repeated write of one is not,
// and the run's columns hold NULL when the run scope did not name them.
func TestToolCallUsage_EachAttemptIsRecordedOnce(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	ledger := llmusagetest.New(t, repo)
	at := day(2026, 11, 24)

	first := toolRec("wf-1/run/act/toolu_1", 1, at)
	written, err := ledger.RecordToolCallUsage(ctx, first)
	require.NoError(t, err)
	assert.True(t, written)
	written, err = ledger.RecordToolCallUsage(ctx, first)
	require.NoError(t, err)
	assert.False(t, written, "a repeat of the same attempt must not write")

	// The retry that answered "interrupted" is its own row.
	retry := toolRec("wf-1/run/act/toolu_1", 2, at)
	retry.Outcome, retry.ErrorClass = store.ToolOutcomeError, store.ToolErrorInterrupted
	retry.WorkflowID, retry.AIRunID, retry.AIRootRunID = "", "", ""
	written, err = ledger.RecordToolCallUsage(ctx, retry)
	require.NoError(t, err)
	assert.True(t, written)

	assert.Equal(t, 2, rowCount(t, raw, `SELECT count(*) FROM tool_call_usage WHERE id = $1`, first.ID))
	assert.Equal(t, 1, rowCount(t, raw, `SELECT count(*) FROM tool_call_usage
		WHERE attempt = 2 AND workflow_id IS NULL AND ai_run_id IS NULL AND error_class = 'interrupted'`))
	assert.Equal(t, 420, rowCount(t, raw, `SELECT duration_ms FROM tool_call_usage WHERE attempt = 1`))
}

// A class handed in with a success is dropped rather than failing the write
// on the schema's check: the outcome is what the row says.
func TestToolCallUsage_ASuccessCarriesNoErrorClass(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	rec := toolRec("wf/run/act/toolu_ok", 1, day(2026, 11, 24))
	rec.ErrorClass = store.ToolErrorTool
	written, err := llmusagetest.New(t, repo).RecordToolCallUsage(context.Background(), rec)
	require.NoError(t, err)
	assert.True(t, written)
	assert.Equal(t, 1, rowCount(t, raw, `SELECT count(*) FROM tool_call_usage WHERE error_class = '' AND outcome = 'success'`))
}

// Rows past the retention period are pruned with the model calls' rows.
func TestToolCallUsage_PrunedAfterRetention(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	ledger := llmusagetest.New(t, repo)
	now := day(2026, 11, 24)

	_, err := ledger.RecordToolCallUsage(ctx, toolRec("old", 1, now.Add(-llmusage.Retention-time.Hour)))
	require.NoError(t, err)
	_, err = ledger.RecordToolCallUsage(ctx, toolRec("new", 1, now.Add(-time.Hour)))
	require.NoError(t, err)

	n, err := ledger.PruneToolCallUsage(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	assert.Equal(t, 1, rowCount(t, raw, `SELECT count(*) FROM tool_call_usage WHERE id = 'new'`))
	assert.Equal(t, 0, rowCount(t, raw, `SELECT count(*) FROM tool_call_usage WHERE id = 'old'`))
}
