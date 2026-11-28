// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbstore "github.com/reliant-labs/reliant/internal/store"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/threads"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// coordinatedDrainStore pauses only the first two claims, after their SQL has
// established transaction snapshots. Retries bypass the barrier. The embedded
// store and the thread service share the real Repo, including its transaction.
type coordinatedDrainStore struct {
	DrainAgentMessagesStore
	MailboxQueue
	serializable bool
	claims       atomic.Int32
	arrived      chan struct{}
	release      chan struct{}
}

func (s *coordinatedDrainStore) RunTx(ctx context.Context, policy orm.TxPolicy, f func(context.Context) error) error {
	if s.serializable {
		policy = orm.Serializable.WithMaxAttempts(orm.DefaultTxMaxAttempts)
	}
	return s.DrainAgentMessagesStore.RunTx(ctx, policy, f)
}

func (s *coordinatedDrainStore) MarkAgentMessagesDelivered(ctx context.Context, ids []string, at time.Time, messageID string) ([]string, error) {
	claimed, err := s.MailboxQueue.MarkAgentMessagesDelivered(ctx, ids, at, messageID)
	if err != nil {
		return nil, err
	}
	if s.claims.Add(1) <= 2 {
		s.arrived <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return claimed, nil
}

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
	for _, serializable := range []bool{false, true} {
		t.Run(fmt.Sprintf("serializable=%t", serializable), func(t *testing.T) {
			t.Parallel()
			testCoordinatedDrains(t, serializable)
		})
	}
}

func testCoordinatedDrains(t *testing.T, serializable bool) {
	const threadsInChat = 2

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
		enqueueDrainTestMessage(t, h.Repo(), chatID, chatID, threadIDs[i], fmt.Sprintf("message for agent %d", i), time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC))
	}

	retries := db.RecordRetryBackoffs(h.Repo())
	store := &coordinatedDrainStore{
		DrainAgentMessagesStore: h.Repo(),
		MailboxQueue:            dbstore.NewAgentMessageExtStore(h.Repo().ORM()),
		serializable:            serializable,
		arrived:                 make(chan struct{}, threadsInChat),
		release:                 make(chan struct{}),
	}
	act := NewDrainAgentMessagesActivity(DrainAgentMessagesDeps{Store: store, Mailbox: store, Threads: threads.New(threads.Deps{Repo: h.Repo(), Messages: dbstore.NewMessageExtStore(h.Repo().ORM()), Senders: dbstore.NewThreadExtStore(h.Repo().ORM())})})
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
	// MarkAgentMessagesDelivered has executed a statement in each transaction,
	// establishing its snapshot, but neither may write the shared chat yet.
	for range threadsInChat {
		select {
		case <-store.arrived:
		case <-time.After(30 * time.Second):
			t.Error("drains did not both reach the transaction barrier")
		}
	}
	close(store.release)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "drain of thread %d of %d", i+1, threadsInChat)
		assert.Equal(t, 1, outputs[i].Count, "drain of thread %d delivered its message", i+1)
	}
	if serializable {
		assert.Positive(t, retries.Len(), "the old isolation must conflict on the overlapping snapshots")
	} else {
		assert.Zero(t, retries.Len(), "no drain transaction conflicted and had to be retried")
	}
	for _, thread := range threadIDs {
		queued, err := dbstore.ListQueuedAgentMessagesForThread(ctx, h.Repo().ORM(), thread)
		require.NoError(t, err)
		assert.Empty(t, queued, "every queued message was delivered")
	}
}
