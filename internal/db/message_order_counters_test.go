// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// Every message and context-window number comes from message_order_counters
// (internal/store/message_order_counter_repo_ext.go), never from the highest
// number already written. These pin what the allocators promise concurrent
// writers: every number is handed out once, each writer's numbers increase,
// and gaps are allowed.

const (
	counterWriters  = 8
	counterPerWrite = 6
)

// allocateConcurrently runs counterWriters goroutines, each taking
// counterPerWrite numbers in turn, every allocation in a SERIALIZABLE
// transaction of its own (as a launch or a send runs), and returns each
// writer's numbers in the order it got them.
func allocateConcurrently(t *testing.T, repo *Repo, allocate func(ctx context.Context) (int64, error)) [][]int64 {
	t.Helper()
	got := make([][]int64, counterWriters)
	errs := make([]error, counterWriters)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := range counterWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range counterPerWrite {
				var n int64
				err := repo.RunTx(context.Background(), orm.Serializable, func(ctx context.Context) error {
					var err error
					n, err = allocate(ctx)
					return err
				})
				if err != nil {
					errs[w] = err
					return
				}
				got[w] = append(got[w], n)
			}
		}()
	}
	close(start)
	wg.Wait()
	for w, err := range errs {
		require.NoError(t, err, "writer %d", w)
	}
	return got
}

// assertUniqueAndMonotonic checks that no number was handed out twice, that
// each writer's numbers strictly increase, and that every number is at least
// floor. It does not require them to be dense.
func assertUniqueAndMonotonic(t *testing.T, got [][]int64, floor int64) {
	t.Helper()
	seen := map[int64]int{}
	for w, nums := range got {
		require.Len(t, nums, counterPerWrite, "writer %d", w)
		for i, n := range nums {
			assert.GreaterOrEqual(t, n, floor, "writer %d took %d, below the counter's start", w, n)
			if i > 0 {
				assert.Greater(t, n, nums[i-1], "writer %d's numbers must increase", w)
			}
			if prev, dup := seen[n]; dup {
				t.Errorf("%d was handed to writers %d and %d", n, prev, w)
			}
			seen[n] = w
		}
	}
}

// createCounterTestWindow opens the chat's root thread's first context window
// at windowSequence (createActivityTestChat created the thread).
func createCounterTestWindow(t *testing.T, repo *Repo, chatID string, windowSequence int) string {
	t.Helper()
	cwID := fmt.Sprintf("%s:%s:%d", chatID, chatID, windowSequence)
	_, err := repo.CreateContextWindow(context.Background(), &ContextWindow{ID: cwID, ThreadID: chatID, Sequence: windowSequence, CreatedAt: time.Now().UTC()})
	require.NoError(t, err)
	return cwID
}

func TestAllocateMessageOrder_ConcurrentWritersGetUniqueIncreasingNumbers(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	chatID := "chat-alloc-" + uuid.NewString()
	createActivityTestChat(t, repo, chatID)

	var mu sync.Mutex
	ordinals := map[int64]int64{}
	seqs := allocateConcurrently(t, repo, func(ctx context.Context) (int64, error) {
		ordinal, seq, err := repo.AllocateMessageOrder(ctx, chatID, chatID)
		mu.Lock()
		ordinals[seq] = ordinal
		mu.Unlock()
		return seq, err
	})
	assertUniqueAndMonotonic(t, seqs, 0)

	// The thread's ordinals come from their own counter, likewise unique.
	seenOrdinal := map[int64]bool{}
	for _, ordinal := range ordinals {
		assert.False(t, seenOrdinal[ordinal], "ordinal %d handed out twice", ordinal)
		seenOrdinal[ordinal] = true
	}
}

// A row whose numbers its writer chose (a fixture, a row written before the
// counters were authoritative) raises the counters on insert, so the next
// allocation clears it without reading messages.
func TestCreateMessage_RaisesTheCountersToTheRowsNumbers(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-raise-" + uuid.NewString()
	createActivityTestChat(t, repo, chatID)
	cwID := createCounterTestWindow(t, repo, chatID, 0)

	now := time.Now().UTC()
	require.NoError(t, repo.CreateMessage(ctx, &Message{
		ID: uuid.NewString(), ChatID: chatID, ThreadID: chatID, ContextWindowID: cwID, Ordinal: 6, Seq: 9,
		Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, CreatedAt: now, UpdatedAt: now,
	}))

	msg, err := repo.SaveMessageToThread(ctx, chatID, chatID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "next", nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(7), msg.Ordinal)
	assert.Equal(t, int64(10), msg.Seq)

	numbered := &Message{
		ID: uuid.NewString(), ChatID: chatID, ThreadID: chatID, ContextWindowID: cwID,
		Role: reliantv1.MessageRole_MESSAGE_ROLE_TOOL, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateNumberedMessage(ctx, numbered))
	assert.Equal(t, int64(8), numbered.Ordinal)
	assert.Equal(t, int64(11), numbered.Seq)
}

func TestAllocateContextWindowSequence_ConcurrentWritersGetUniqueIncreasingNumbers(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := setupTestRepo(t)
	defer cleanup()

	chatID := "chat-cw-alloc-" + uuid.NewString()
	createActivityTestChat(t, repo, chatID)
	// A forked thread starts at its parent's window sequence; its next
	// window must follow that, not 0.
	const forkedAt = 3
	createCounterTestWindow(t, repo, chatID, forkedAt)

	got := allocateConcurrently(t, repo, func(ctx context.Context) (int64, error) {
		n, err := repo.AllocateContextWindowSequence(ctx, chatID)
		return int64(n), err
	})
	assertUniqueAndMonotonic(t, got, forkedAt+1)

	_, err := repo.AllocateContextWindowSequence(context.Background(), "thread-without-a-window")
	require.Error(t, err, "a thread with no window has no sequence to follow")
}
