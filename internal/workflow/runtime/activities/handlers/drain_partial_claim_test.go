// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// The signal the drain's rollback depends on: a claim that cannot take the
// whole batch takes only what is still queued, and says so.
//
// The drain lists its rows outside the transaction that writes them, so the
// batch can shrink between the list and the claim — a competing drain, a
// "Send now" (ClaimQueuedAgentMessagesForThread), or a cancel all remove queued
// rows. The drain detects that by comparing what it got back against what it
// asked for, and rolls back rather than delivering a partial batch.
//
// Rolling back is the load-bearing half. The rows a losing drain DID claim are
// marked delivered inside its still-open transaction; committing there would
// leave them status = 2 pointing at no message, and every read path filters on
// status = 1, so nothing would ever find them again — the user's message gone
// permanently, with no error raised anywhere. That is why the drain returns an
// error (errDrainBatchTaken) rather than nil on this path: RunTx commits on nil.
func TestMarkAgentMessagesDelivered_PartialBatchReportsOnlyWhatItTook(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	fromThreadID := uuid.New().String()
	_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: fromThreadID, ChatID: chatID})
	require.NoError(t, err)

	now := time.Now().UTC()
	enqueueDrainTestMessage(t, h.Repo(), chatID, fromThreadID, chatID, "A", now)
	enqueueDrainTestMessage(t, h.Repo(), chatID, fromThreadID, chatID, "B", now.Add(time.Minute))

	queued, err := h.Repo().ListQueuedAgentMessagesForThread(ctx, chatID)
	require.NoError(t, err)
	require.Len(t, queued, 2)
	ids := []string{queued[0].ID, queued[1].ID}

	// A racer takes the second row, exactly as a competing drain would.
	stolen, err := h.Repo().MarkAgentMessagesDelivered(ctx, ids[1:], time.Now().UTC(), "")
	require.NoError(t, err)
	require.Len(t, stolen, 1)

	// A claim for the ORIGINAL two-row batch now comes back short. That
	// shortfall is the whole signal: len(claimed) != len(ids).
	claimed, err := h.Repo().MarkAgentMessagesDelivered(ctx, ids, time.Now().UTC(), "")
	require.NoError(t, err)
	assert.Len(t, claimed, 1, "only the still-queued row can be claimed")
	assert.Equal(t, ids[0], claimed[0])
	assert.NotEqual(t, len(ids), len(claimed),
		"a short claim is what tells the drain its batch was taken")
}

// A drain whose batch was taken before it claimed delivers nothing, does not
// error, and leaves nothing stranded.
func TestDrainAgentMessages_BatchTakenDeliversNothingAndStrandsNothing(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	fromThreadID := uuid.New().String()
	_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: fromThreadID, ChatID: chatID})
	require.NoError(t, err)

	enqueueDrainTestMessage(t, h.Repo(), chatID, fromThreadID, chatID, "only row",
		time.Now().UTC().Add(-time.Hour))

	queued, err := h.Repo().ListQueuedAgentMessagesForThread(ctx, chatID)
	require.NoError(t, err)
	require.Len(t, queued, 1)

	// Someone delivers it first.
	claimed, err := h.Repo().MarkAgentMessagesDelivered(ctx, []string{queued[0].ID}, time.Now().UTC(), "")
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	// The drain now finds an empty queue: nothing delivered, no error, and no
	// fabricated envelope.
	act := newDrainTestActivity(h)
	var output DrainAgentMessagesOutput
	err = h.ExecuteActivity(act.Execute, DrainAgentMessagesInput{ChatID: chatID, Thread: chatID}, &output)
	require.NoError(t, err)
	assert.False(t, output.HasMessages, "nothing was left to deliver")
	assert.Zero(t, output.Count)
}
