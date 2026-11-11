// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

// A pass backfills every incomplete chat, across batches, and a second pass
// finds nothing; a chat the previous release writes to again afterwards (a
// rollback) is incomplete again and the next pass repairs it.
func TestBackfillIncompleteChatUpdateHeads(t *testing.T) {
	t.Parallel()

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()

	// The previous release's counter: last_assigned_seq advanced, the heads
	// watermark untouched.
	behind := func(chatID string) {
		t.Helper()
		_, err := raw.ExecContext(ctx,
			`UPDATE update_stream_counters SET heads_through_seq = 0 WHERE stream_kind = 'chat' AND stream_id = $1`, chatID)
		require.NoError(t, err)
		_, err = raw.ExecContext(ctx, `DELETE FROM chat_update_heads WHERE chat_id = $1`, chatID)
		require.NoError(t, err)
	}
	var chats []string
	for i := 0; i < 5; i++ {
		chatID := fmt.Sprintf("chat-backfill-pass-%d", i)
		chats = append(chats, chatID)
		require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "ap-1", `{"v":1}`))
		require.NoError(t, repo.CreateChatUpdate(ctx, chatID, UpdateTypeApproval, "ap-1", `{"v":2}`))
		behind(chatID)
	}

	report, err := repo.BackfillIncompleteChatUpdateHeads(ctx, 2, 0)
	require.NoError(t, err)
	require.Equal(t, ChatUpdateHeadsBackfillReport{Backfilled: 5}, report)
	for _, chatID := range chats {
		got, source, err := store.SnapshotUpdateHeads(ctx, repo.ORM(), chatID)
		require.NoError(t, err)
		require.Equal(t, store.HeadsSourceTable, source, chatID)
		require.Len(t, got, 1)
		require.JSONEq(t, `{"v":2}`, string(got[0].Data))
	}

	report, err = repo.BackfillIncompleteChatUpdateHeads(ctx, 2, 0)
	require.NoError(t, err)
	require.Equal(t, ChatUpdateHeadsBackfillReport{}, report)

	behind(chats[3])
	report, err = repo.BackfillIncompleteChatUpdateHeads(ctx, 2, 0)
	require.NoError(t, err)
	require.Equal(t, ChatUpdateHeadsBackfillReport{Backfilled: 1}, report)
}
