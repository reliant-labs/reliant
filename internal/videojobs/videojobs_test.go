// Copyright (c) 2025 Reliant Labs
package videojobs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

func TestSQLStore_RoundTripAndResumeLookup(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	store := NewSQLStore(repo.ORM())

	missing, err := store.GetByToolCall(ctx, "tc-none")
	require.NoError(t, err)
	assert.Nil(t, missing)

	job := &Job{ToolCallID: "tc-1", UserID: "u1", ChatID: "c1", Driver: "gemini", ModelID: "veo-3.1-generate",
		APIModel: "veo-3.1-generate-preview", ProviderJob: "models/veo/operations/abc"}
	require.NoError(t, store.Create(ctx, job))
	assert.Error(t, store.Create(ctx, job), "one record per tool call: a duplicate Create is a bug")

	got, err := store.GetByToolCall(ctx, "tc-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, StateSubmitted, got.State)
	assert.Equal(t, "models/veo/operations/abc", got.ProviderJob)
	assert.Empty(t, got.AttachmentID)

	byAtt, err := store.GetByAttachment(ctx, "att-1")
	require.NoError(t, err)
	assert.Nil(t, byAtt)

	require.NoError(t, store.Complete(ctx, "tc-1", "att-1"))
	byAtt, err = store.GetByAttachment(ctx, "att-1")
	require.NoError(t, err)
	require.NotNil(t, byAtt)
	assert.Equal(t, StateCompleted, byAtt.State)
	assert.Equal(t, "tc-1", byAtt.ToolCallID)
	assert.Equal(t, "u1", byAtt.UserID)

	require.NoError(t, store.Create(ctx, &Job{ToolCallID: "tc-2", UserID: "u1", Driver: "gemini", ModelID: "m", APIModel: "m", ProviderJob: "j2"}))
	require.NoError(t, store.Finish(ctx, "tc-2", StateFailed, "content filter"))
	failed, err := store.GetByToolCall(ctx, "tc-2")
	require.NoError(t, err)
	assert.Equal(t, StateFailed, failed.State)
	assert.Equal(t, "content filter", failed.ErrorMessage)

	// Marking a job that is not there is not an error.
	require.NoError(t, store.Complete(ctx, "tc-none", "att-x"))
	require.NoError(t, store.Finish(ctx, "tc-none", StateCancelled, "gone"))
}

// The port onto the generated store reads and writes the rows the hand-SQL
// store it replaced did: a row written by that store's INSERT reads back the
// same, and a row written by the port reads back the same through that
// store's SELECT.
func TestSQLStore_RowsMatchTheHandSQLStore(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	store := NewSQLStore(repo.ORM())
	now := time.Now().UTC().Truncate(time.Microsecond)

	_, err := rawDB.ExecContext(ctx, `
		INSERT INTO video_generation_jobs
			(tool_call_id, user_id, chat_id, driver, model_id, api_model, provider_job, state, error_message, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
		"tc-hand", "u1", "c1", "gemini", "veo", "veo-preview", "ops/1", string(StateSubmitted), "", now)
	require.NoError(t, err)
	got, err := store.GetByToolCall(ctx, "tc-hand")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.CreatedAt.Equal(now) && got.UpdatedAt.Equal(now), "timestamps %v / %v, want %v", got.CreatedAt, got.UpdatedAt, now)
	got.CreatedAt, got.UpdatedAt = time.Time{}, time.Time{}
	assert.Equal(t, &Job{ToolCallID: "tc-hand", UserID: "u1", ChatID: "c1", Driver: "gemini", ModelID: "veo", APIModel: "veo-preview",
		ProviderJob: "ops/1", State: StateSubmitted}, got)

	job := &Job{ToolCallID: "tc-port", UserID: "u2", Driver: "omni", ModelID: "m", APIModel: "m-api", ProviderJob: "int/2"}
	require.NoError(t, store.Create(ctx, job))
	require.NoError(t, store.Complete(ctx, "tc-port", "att-2"))
	var (
		chatID, state, attachmentID, errMsg string
		createdAt, updatedAt                time.Time
	)
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT chat_id, state, COALESCE(attachment_id, ''), error_message, created_at, updated_at
		FROM video_generation_jobs WHERE tool_call_id = $1`, "tc-port").Scan(&chatID, &state, &attachmentID, &errMsg, &createdAt, &updatedAt))
	assert.Equal(t, "", chatID)
	assert.Equal(t, string(StateCompleted), state)
	assert.Equal(t, "att-2", attachmentID)
	assert.Equal(t, "", errMsg)
	assert.WithinDuration(t, job.CreatedAt, createdAt, time.Microsecond)
	assert.False(t, updatedAt.Before(createdAt), "Complete stamps updated_at")
}
