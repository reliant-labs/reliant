// Copyright (c) 2026 Reliant Labs
package streaming

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/dbtest"
	"github.com/reliant-labs/reliant/internal/store"
)

// StreamChatUpdatesRequest.skip_total: the snapshot of a stream that sets it
// runs no count of the chat's messages (on a chat of a million it took 11.3s
// in prod, obs 16122d5e) and says total 0; one that leaves it unset, as every
// client older than the field does, still gets the exact count.

// countSpyRepo is the real repository, counting CountMessagesInChat calls.
type countSpyRepo struct {
	Repository
	counts atomic.Int32
}

func (r *countSpyRepo) CountMessagesInChat(ctx context.Context, chatID string) (int, error) {
	r.counts.Add(1)
	return r.Repository.CountMessagesInChat(ctx, chatID)
}

func TestChatSnapshotSkipTotal(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, raceTestUserID)
	f := dbtest.SeedTranscriptBodies(t, ctx, repo, raceTestUserID)
	exact, err := repo.CountMessagesInChat(ctx, f.ChatID)
	require.NoError(t, err)
	require.Positive(t, exact)

	open := func(t *testing.T, skipTotal bool) (*reliantv1.ChatSyncSnapshot, int32) {
		t.Helper()
		spy := &countSpyRepo{Repository: repo}
		svc := NewTestStreaming(t, WithStreamingDeps(withRoles(Deps{TriggerReads: &MockTriggerReads{}, Repo: spy, ChatRows: store.NewChatExtStore(repo.ORM())})))
		events := chatStreamEvents(t, svc, &reliantv1.StreamChatUpdatesRequest{ChatId: f.ChatID, SkipTotal: skipTotal})
		snapshot := nextChatEvent(t, events).GetSnapshot()
		require.NotNil(t, snapshot)
		require.NotNil(t, nextChatEvent(t, events).GetCaughtUp())
		return snapshot, spy.counts.Load()
	}

	counted, counts := open(t, false)
	t.Run("unset: the exact count, as before", func(t *testing.T) {
		assert.Equal(t, int32(1), counts)
		assert.Equal(t, int32(exact), counted.GetTotal())
	})

	t.Run("skip_total: no count query, total 0, the snapshot otherwise unchanged", func(t *testing.T) {
		skipped, counts := open(t, true)
		assert.Zero(t, counts, "CountMessagesInChat ran for a stream that set skip_total")
		assert.Zero(t, skipped.GetTotal())
		require.NotEmpty(t, skipped.GetMessages())
		assert.Len(t, skipped.GetMessages(), len(counted.GetMessages()))
		assert.Equal(t, counted.GetHasMore(), skipped.GetHasMore())
		assert.Equal(t, counted.GetOldestSeq(), skipped.GetOldestSeq())
		assert.Equal(t, counted.GetLatestSequence(), skipped.GetLatestSequence())
	})
}
