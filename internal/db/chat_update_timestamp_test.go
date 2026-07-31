package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

// TestCreateChatUpdateStampsUTC pins the wall clock every workflow event
// carries.
//
// chat_updates.created_at is TIMESTAMP WITHOUT TIME ZONE, so the driver
// discards the offset and keeps the wall-clock reading. A local time.Now()
// therefore round-trips as that local reading RELABELLED UTC, and the server
// serializes it with time.RFC3339 — correct code, already-wrong value — so the
// stream emits "23:50:11Z" for an event that happened at 03:50:11Z. Events
// stamped elsewhere (the follower's own synthetic terminal events, which use
// time.Now().UTC()) carry true UTC, so a single stream mixes two clocks hours
// apart, both claiming Zulu. Any timeline rebuilt from those artifacts is
// wrong, and nothing downstream can detect it.
//
// time.Local is pinned to a non-UTC zone for the duration of the test. Without
// that, the test would pass on a UTC machine whether or not the bug is
// present — a guard that cannot fail, which is worse than no guard.
func TestCreateChatUpdateStampsUTC(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	originalLocal := time.Local
	time.Local = time.FixedZone("TEST-0400", -4*60*60)
	t.Cleanup(func() { time.Local = originalLocal })

	chatID := uuid.New().String()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateChat(ctx, &Chat{
		ID:         chatID,
		Title:      "timestamp chat",
		ProjectID:  "test-project",
		UserID:     "test-user",
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	before := time.Now().UTC()
	require.NoError(t, repo.CreateChatUpdate(ctx, chatID,
		reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION, "node-1", `{"event_type":"started"}`))
	after := time.Now().UTC()

	updates, err := repo.GetUpdatesSince(ctx, chatID, 0, 10)
	require.NoError(t, err)
	require.Len(t, updates, 1)

	// The stored instant must be the real one. Under the bug this is off by
	// the local zone offset — four hours, for the zone pinned above.
	got := updates[0].CreatedAt.UTC()
	require.False(t, got.Before(before.Add(-time.Second)),
		"created_at %s predates the write (%s) — a local wall clock was stored as UTC", got, before)
	require.False(t, got.After(after.Add(time.Second)),
		"created_at %s postdates the write (%s) — a local wall clock was stored as UTC", got, after)

	// And the value the gRPC layer serializes must render as true Zulu.
	require.Equal(t, "Z", got.Format(time.RFC3339)[len(got.Format(time.RFC3339))-1:],
		"chat update timestamps must serialize as Zulu")
}
