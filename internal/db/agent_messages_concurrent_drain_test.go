package db

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two drains racing on one thread must not both deliver the same rows.
//
// The drain reads its queued rows outside the transaction that writes them, so
// two callers can select the same batch -- an interrupt re-dispatching call_llm
// while the previous activity is still finishing is exactly that shape. Before
// the status = 1 guard, the second caller's UPDATE matched the already-delivered
// rows anyway, silently repointing delivered_message_id at its own duplicate
// envelope. Both callers then wrote the queued bodies into the transcript and
// the user saw every message twice.
//
// The claim is what decides a winner, so it is the thing this pins: exactly one
// caller may come away with the rows, and the loser must come away with none --
// its signal to write nothing at all.
func TestMarkAgentMessagesDelivered_ConcurrentClaimHasOneWinner(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	chatID, threadID, _ := seedAgentMessageThreads(t, repo, ctx)

	now := time.Now().UTC()
	ids := []string{uuid.New().String(), uuid.New().String()}
	for i, id := range ids {
		enqueueTestAgentMessage(t, repo, ctx, id, chatID, threadID, threadID, "queued", now.Add(time.Duration(i)*time.Millisecond))
	}

	// One envelope is enough: the claim is what decides the winner, and
	// seedDeliveredMessage writes a fixed ordinal so it cannot be called twice
	// on one thread.
	envelope := seedDeliveredMessage(t, repo, ctx, chatID, threadID)

	var wg sync.WaitGroup
	claimed := make([][]string, 2)
	errs := make([]error, 2)
	start := make(chan struct{})

	for i := range claimed {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release both at once so the claim is genuinely contended
			claimed[i], errs[i] = repo.MarkAgentMessagesDelivered(ctx, ids, time.Now().UTC(), envelope)
		}(i)
	}
	close(start)
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	won, lost := claimed[0], claimed[1]
	if len(won) < len(lost) {
		won, lost = lost, won
	}
	assert.Len(t, won, len(ids), "one drain claims the whole batch")
	assert.Empty(t, lost, "the other claims nothing, so it writes nothing")
}

// A delivery attempt on rows that are already delivered claims nothing. Same
// guarantee without the concurrency, and it is what makes the drain safe to
// retry: Temporal can re-run the activity, and the re-run must not re-deliver.
func TestMarkAgentMessagesDelivered_AlreadyDeliveredClaimsNothing(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	chatID, threadID, _ := seedAgentMessageThreads(t, repo, ctx)

	id := uuid.New().String()
	enqueueTestAgentMessage(t, repo, ctx, id, chatID, threadID, threadID, "only", time.Now().UTC())

	envelope := seedDeliveredMessage(t, repo, ctx, chatID, threadID)

	first, err := repo.MarkAgentMessagesDelivered(ctx, []string{id}, time.Now().UTC(), envelope)
	require.NoError(t, err)
	require.Len(t, first, 1, "the first delivery claims the row")

	second, err := repo.MarkAgentMessagesDelivered(ctx, []string{id}, time.Now().UTC(), envelope)
	require.NoError(t, err)
	assert.Empty(t, second, "a redelivery attempt must claim nothing")
}
