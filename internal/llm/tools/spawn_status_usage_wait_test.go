package tools

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

func TestSpawnStatus_SingleAgent_UsageWaitWithoutAssistantMessage(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := uuid.NewString()
	createTestChat(t, repo, chatID)
	callerID, childID, _ := seedSpawnRelationship(t, repo, ctx, chatID, db.ThreadStatusRunning)
	now := time.Now().UTC()
	require.NoError(t, repo.RecordUsageLimitWait(ctx, chatID, childID, "codex", false, now.Add(time.Hour), now))
	resp, meta := runSpawnStatus(t, NewSpawnStatusTool(repo), rctx.NewToolContext(ctx, chatID, callerID, nil, nil), SpawnStatusParams{AgentID: childID})
	require.False(t, resp.IsError)
	require.Equal(t, "waiting for usage limit", meta.Status)
	require.Contains(t, resp.Content, "codex")
	require.Contains(t, resp.Content, "Waiting:")
	require.NotContains(t, resp.Content, "no activity yet")
}
