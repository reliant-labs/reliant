// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// TestLegacyAllocatorBaseline_StillConflicts is the CONTROL for
// TestSaveMessage_ConcurrentWritersSameChat.
//
// A regression test that passes is only meaningful if it would have FAILED
// against the code it replaces. This test keeps the old write shape alive —
// MAX()-scan allocation (GetNextOrdinal / GetNextSeq) plus the message insert,
// all inside one SERIALIZABLE transaction — and asserts that shape still
// produces serialization failures under the same concurrency the new path
// handles cleanly.
//
// If this test ever stops reporting conflicts, the concurrency tests have lost
// their teeth and the harness itself is no longer exercising contention: fix
// the harness, do not delete this.
func TestLegacyAllocatorBaseline_StillConflicts(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	const writers = 8
	const messagesPerWriter = 5

	threadIDs := make([]string, writers)
	cwIDs := make([]string, writers)
	for i := range threadIDs {
		thread, cw := h.createThread(fmt.Sprintf("legacy-thread-%d", i), h.chatID)
		threadIDs[i] = thread.ID
		cwIDs[i] = cw.ID
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		conflicts int
		other     []error
	)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(threadID, cwID string, writerIdx int) {
			defer wg.Done()
			for m := 0; m < messagesPerWriter; m++ {
				// The OLD transaction body, verbatim in shape: scan for
				// ordinal, walk for seq, then insert into the range just read.
				//
				// RunTxNoRetry deliberately: RunTx's ladder absorbs these
				// conflicts and reports success, which hides the very thing
				// this control exists to measure. The production path DID
				// retry, and still exhausted the ladder 35 times in one
				// session — so the raw rate is the honest number.
				err := h.repo.RunTxNoRetry(ctx, func(txCtx context.Context) error {
					ordinal, err := h.repo.GetNextOrdinal(txCtx, threadID)
					if err != nil {
						return err
					}
					seq, err := h.repo.GetNextSeq(txCtx, h.chatID, threadID)
					if err != nil {
						return err
					}
					ts := time.Now().UTC()
					return h.repo.CreateMessage(txCtx, &db.Message{
						ID:              fmt.Sprintf("legacy-%d-%d", writerIdx, m),
						ChatID:          h.chatID,
						Ordinal:         ordinal,
						Seq:             seq,
						ThreadID:        threadID,
						ContextWindowID: cwID,
						Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
						TokenCount:      ptr.IntIfPositive(1),
						CreatedAt:       ts,
						UpdatedAt:       ts,
					})
				})

				mu.Lock()
				if err != nil {
					if strings.Contains(err.Error(), "40001") ||
						strings.Contains(err.Error(), "could not serialize") ||
						strings.Contains(err.Error(), "transaction failed after retries") {
						conflicts++
					} else {
						other = append(other, err)
					}
				}
				mu.Unlock()
			}
		}(threadIDs[w], cwIDs[w], w)
	}
	wg.Wait()

	for _, err := range other {
		t.Logf("non-serialization error from legacy path: %v", err)
	}

	total := writers * messagesPerWriter
	t.Logf("legacy allocator: %d/%d writes failed with serialization conflicts (%.0f%%)",
		conflicts, total, float64(conflicts)/float64(total)*100)

	// The whole point of the control. If the legacy shape stops conflicting
	// here, this harness is no longer generating contention and the
	// concurrency tests above are passing vacuously — fix the harness rather
	// than deleting this assertion.
	if conflicts == 0 {
		t.Fatal("expected the MAX()-scan allocator to produce serialization conflicts under " +
			"concurrent writers; it did not, so this harness is not exercising contention " +
			"and TestSaveMessage_ConcurrentWritersSameChat proves nothing")
	}

	// And the comparison that justifies the rewrite: the same workload through
	// the new single-statement path, with retries likewise disabled.
	newPathFailures := runNewPathUnderContention(t, h, writers, messagesPerWriter)
	t.Logf("single-statement path: %d/%d writes failed with serialization conflicts",
		newPathFailures, total)

	if newPathFailures > 0 {
		t.Errorf("the single-statement path still conflicts (%d/%d) — it allocates from "+
			"counter rows at READ COMMITTED and must not raise 40001 at all",
			newPathFailures, total)
	}
}

// runNewPathUnderContention drives the production SaveMessage path with the
// same writer count and asserts nothing about correctness — the concurrency
// tests do that. It exists to produce the second half of the before/after
// number in one run, so the comparison cannot drift between two test files.
func runNewPathUnderContention(t *testing.T, h *testHelper, writers, messagesPerWriter int) int {
	t.Helper()
	ctx := context.Background()

	threadIDs := make([]string, writers)
	for i := range threadIDs {
		thread, _ := h.createThread(fmt.Sprintf("newpath-thread-%d", i), h.chatID)
		threadIDs[i] = thread.ID
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		failures int
	)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(threadID string, writerIdx int) {
			defer wg.Done()
			for m := 0; m < messagesPerWriter; m++ {
				_, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
					ChatID:  h.chatID,
					Thread:  threadID,
					Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
					Content: fmt.Sprintf("newpath w%d m%d", writerIdx, m),
				})
				if err != nil {
					mu.Lock()
					failures++
					mu.Unlock()
					t.Logf("new path error: %v", err)
				}
			}
		}(threadIDs[w], w)
	}
	wg.Wait()

	return failures
}
