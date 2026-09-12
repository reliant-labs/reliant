// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// TestSaveMessage_ConcurrentWritersSameChat is the regression test for the
// 40001 storm that surfaced to users as red "Workflow error in Save Message"
// banners carrying a raw SQLSTATE.
//
// The shape it reproduces is a workflow chat running several sub-workflow
// threads at once. Every one of them saves messages into the SAME chat, so
// they all contend on that chat's seq allocation and its chat_update sequence.
//
// Against the old implementation this failed: SaveMessage ran a MAX(ordinal)
// scan, a WITH RECURSIVE seq walk, the message insert, one insert per content
// block and the chat_update, all inside one SERIALIZABLE transaction. The
// MAX-then-INSERT pattern takes an SSI predicate lock over the range being
// inserted into, so concurrent savers aborted each other with
// "could not serialize access due to read/write dependencies among
// transactions", and the counter upsert added "could not serialize access due
// to concurrent update" on top. Observed in a real session: 881 conflicts, 35
// of which exhausted all seven retry attempts.
//
// It also pins the two invariants that made the naive fixes wrong:
//
//   - Every seq is UNIQUE. A lost update would hand the same number to two
//     messages, which messages_chat_seq_key would reject — and if it did not,
//     two messages would occupy one slot in the transcript.
//   - Every seq is CONTIGUOUS. The client resumes with
//     `WHERE sequence_number > cursor`, so a hole is indistinguishable from
//     lost delivery and triggers a spurious resync.
func TestSaveMessage_ConcurrentWritersSameChat(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	const (
		writers            = 8
		messagesPerWriter  = 5
		expectedTotalSaved = writers * messagesPerWriter
	)

	// Each writer gets its own thread, exactly as parallel sub-workflows do,
	// but they all share one chat.
	threadIDs := make([]string, writers)
	for i := range threadIDs {
		thread, _ := h.createThread(fmt.Sprintf("concurrent-thread-%d", i), h.chatID)
		threadIDs[i] = thread.ID
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		errs    []error
		seqSeen []int64
	)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(threadID string, writerIdx int) {
			defer wg.Done()
			for m := 0; m < messagesPerWriter; m++ {
				res, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
					ChatID:  h.chatID,
					Thread:  threadID,
					Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
					Content: fmt.Sprintf("writer %d message %d", writerIdx, m),
				})
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				} else {
					seqSeen = append(seqSeen, res.Ordinal)
				}
				mu.Unlock()
			}
		}(threadIDs[w], w)
	}
	wg.Wait()

	// No serialization failure may reach the caller. Before the fix this is
	// where the test failed, with SQLSTATE 40001 in the message.
	if len(errs) > 0 {
		serialization := 0
		for _, err := range errs {
			if strings.Contains(err.Error(), "40001") ||
				strings.Contains(err.Error(), "could not serialize") {
				serialization++
			}
		}
		t.Fatalf("SaveMessage failed %d/%d times under %d concurrent writers (%d were serialization conflicts); first error: %v",
			len(errs), expectedTotalSaved, writers, serialization, errs[0])
	}

	// Every message must exist exactly once, with a unique chat-global seq.
	messages, err := h.repo.ListMessages(ctx, h.chatID, db.MessageListOptions{})
	if err != nil {
		t.Fatalf("failed to list messages: %v", err)
	}
	if len(messages) != expectedTotalSaved {
		t.Fatalf("expected %d messages, got %d", expectedTotalSaved, len(messages))
	}

	seen := make(map[int64]string, len(messages))
	for _, msg := range messages {
		if prev, dup := seen[msg.Seq]; dup {
			t.Errorf("seq %d assigned twice: messages %s and %s", msg.Seq, prev, msg.ID)
		}
		seen[msg.Seq] = msg.ID
	}

	// Contiguity: the client's resume cursor treats a hole as lost delivery.
	for want := int64(0); want < int64(expectedTotalSaved); want++ {
		if _, ok := seen[want]; !ok {
			t.Errorf("seq %d missing — a gap makes the client resync spuriously", want)
		}
	}
}

// TestSaveMessage_ChatUpdateSequenceIsContiguous pins the ledger half of the
// same invariant. The message rows can be perfect while the chat_updates
// stream has holes, and it is the STREAM the client resumes from — a gap there
// is what actually reaches the user as a message that never appears.
func TestSaveMessage_ChatUpdateSequenceIsContiguous(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	const writers = 6
	const messagesPerWriter = 4

	threadIDs := make([]string, writers)
	for i := range threadIDs {
		thread, _ := h.createThread(fmt.Sprintf("cu-thread-%d", i), h.chatID)
		threadIDs[i] = thread.ID
	}

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(threadID string, writerIdx int) {
			defer wg.Done()
			for m := 0; m < messagesPerWriter; m++ {
				if _, err := h.svc.SaveMessage(ctx, SaveMessageOpts{
					ChatID:  h.chatID,
					Thread:  threadID,
					Role:    int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
					Content: fmt.Sprintf("w%d-m%d", writerIdx, m),
				}); err != nil {
					t.Errorf("SaveMessage failed: %v", err)
					return
				}
			}
		}(threadIDs[w], w)
	}
	wg.Wait()

	updates, err := h.repo.GetUpdatesSince(ctx, h.chatID, 0, 1000)
	if err != nil {
		t.Fatalf("failed to read chat updates: %v", err)
	}

	want := writers * messagesPerWriter
	if len(updates) < want {
		t.Fatalf("expected at least %d chat_updates, got %d — updates were lost", want, len(updates))
	}

	for i := 1; i < len(updates); i++ {
		prev := updates[i-1].SequenceNumber
		cur := updates[i].SequenceNumber
		if cur != prev+1 {
			t.Errorf("chat_update sequence jumped %d -> %d; the client reads a gap as lost delivery", prev, cur)
		}
	}
}
