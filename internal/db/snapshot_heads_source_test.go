// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

// The snapshot's chat_snapshot.update_heads span says where the heads came
// from: the materialized table, or the log for a chat whose heads the stream
// counter cannot vouch for. That is how the rollout is watched (how many opens
// still pay the log read), so the store must name the right source.
func TestSnapshotUpdateHeads_ReportsItsSource(t *testing.T) {
	t.Parallel()

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	sourceOf := func(chatID string) string {
		t.Helper()
		_, source, err := store.SnapshotUpdateHeads(ctx, repo.ORM(), chatID)
		require.NoError(t, err)
		return source
	}

	chatID := seedChatForUpdates(t, repo, ctx)
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "ap-1", `{}`))
	require.Equal(t, store.HeadsSourceTable, sourceOf(chatID))

	// A counter the previous release advanced: the heads are not vouched for.
	_, err := raw.ExecContext(ctx,
		`UPDATE update_stream_counters SET last_assigned_seq = last_assigned_seq + 1 WHERE stream_kind = 'chat' AND stream_id = $1`, chatID)
	require.NoError(t, err)
	require.Equal(t, store.HeadsSourceLogFallback, sourceOf(chatID))
}
