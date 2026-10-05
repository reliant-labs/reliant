// Copyright (c) 2025 Reliant Labs
package videojobs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

func TestSQLStore_RoundTripAndResumeLookup(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	store := NewSQLStore(rawDB)

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
}
