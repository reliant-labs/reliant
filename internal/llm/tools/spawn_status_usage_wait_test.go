package tools

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/providerbackoff/providerbackofftest"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/store"
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
	require.NoError(t, providerbackofftest.New(t, repo).RecordUsageLimitWait(ctx, chatID, childID, "codex", false, now.Add(time.Hour), now))
	resp, meta := runSpawnStatus(t, NewSpawnStatusTool(SpawnStatusDeps{Store: repo, Backoff: providerbackofftest.New(t, repo), Questions: store.NewQuestionExtStore(repo.ORM()), Tree: store.NewToolCallExtStore(repo.ORM())}), rctx.NewToolContext(ctx, chatID, callerID, nil, nil), SpawnStatusParams{AgentID: childID})
	require.False(t, resp.IsError)
	require.Equal(t, "waiting for usage limit", meta.Status)
	require.Contains(t, resp.Content, "codex")
	require.Contains(t, resp.Content, "Waiting:")
	require.NotContains(t, resp.Content, "no activity yet")
}
