// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// An orchestrator with several sub-agents in one chat drains a mailbox on
// every thread at each turn, so drains on different threads of one chat run at
// the same moment. Each drain writes messages and a chat_update into that chat:
// the same per-chat rows every drain (and every other message save) writes.
// The drain transaction used to be SERIALIZABLE, and SaveMessageAtomic's own
// READ COMMITTED joins the drain's transaction rather than opening its own, so
// the hottest write in the system ran SERIALIZABLE here: a drain that waited
// for one of those rows and found it updated by the transaction it waited for
// failed with SQLSTATE 40001 "could not serialize access due to concurrent
// update" (prod 53e2370c, "failed to save mailbox envelope"). Once its attempts
// ran out the queued messages missed that turn. Every invariant the drain keeps
// is a row lock (the claim is one conditional UPDATE), so under READ COMMITTED
// no attempt conflicts: counting retries, not timing them, keeps this exact on
// a loaded host.
func TestDrainAgentMessages_ConcurrentDrainsInOneChatDoNotConflict(t *testing.T) {
	t.Parallel()
	const threadsInChat = 8

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	threadIDs := make([]string, threadsInChat)
	for i := range threadIDs {
		threadIDs[i] = uuid.New().String()
		_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: threadIDs[i], ChatID: chatID})
		require.NoError(t, err)
		_, err = h.Repo().CreateContextWindow(ctx, &db.ContextWindow{
			ID:       chatID + ":" + threadIDs[i] + ":0",
			ThreadID: threadIDs[i],
			Sequence: 0,
		})
		require.NoError(t, err)
		enqueueDrainTestMessage(t, h.Repo(), chatID, chatID, threadIDs[i], fmt.Sprintf("message for agent %d", i), time.Now().UTC())
	}

	retries := db.RecordRetryBackoffs(h.Repo())
	act := newDrainTestActivity(h)
	start := make(chan struct{})
	outputs := make([]DrainAgentMessagesOutput, threadsInChat)
	errs := make([]error, threadsInChat)
	var wg sync.WaitGroup
	for i, thread := range threadIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// One activity environment per drain: a test environment runs
			// one activity at a time.
			env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
			env.RegisterActivity(act.Execute)
			<-start
			val, err := env.ExecuteActivity(act.Execute, DrainAgentMessagesInput{ChatID: chatID, Thread: thread})
			if err == nil {
				err = val.Get(&outputs[i])
			}
			errs[i] = err
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "drain of thread %d of %d", i+1, threadsInChat)
		assert.Equal(t, 1, outputs[i].Count, "drain of thread %d delivered its message", i+1)
	}
	assert.Zero(t, retries.Len(), "no drain transaction conflicted and had to be retried")
	for _, thread := range threadIDs {
		queued, err := h.Repo().ListQueuedAgentMessagesForThread(ctx, thread)
		require.NoError(t, err)
		assert.Empty(t, queued, "every queued message was delivered")
	}
}
