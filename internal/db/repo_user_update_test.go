package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestCreateUserUpdate_AssignsSequenceAndID(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "user-seq-test"

	update := &UserUpdate{
		UserID:     userID,
		UpdateType: UserUpdateChatCreated,
		EntityType: EntityTypeChat,
		EntityID:   "chat-1",
		Data:       []byte(`{"k":"v"}`),
	}

	if err := repo.CreateUserUpdate(ctx, update); err != nil {
		t.Fatalf("CreateUserUpdate failed: %v", err)
	}

	// Sequence numbers come from a shared Postgres sequence, not
	// MAX(sequence_number)+1 per user (see GetNextUserSequenceNumber for why:
	// the MAX() read took a predicate lock under SERIALIZABLE and aborted
	// concurrent writers with SQLSTATE 40001). So the value is NOT "1 for a
	// user's first update" — it is whatever the sequence hands out. What the
	// callers actually require is that it is positive, that the generated ID
	// matches it, and that the row round-trips with the same number.
	if update.SequenceNumber < 1 {
		t.Fatalf("expected a positive sequence_number, got %d", update.SequenceNumber)
	}
	if update.ID != fmt.Sprintf("%s-%d", userID, update.SequenceNumber) {
		t.Fatalf("unexpected generated ID: %s", update.ID)
	}

	updates, err := repo.GetUserUpdatesSince(ctx, userID, 0, 10)
	if err != nil {
		t.Fatalf("GetUserUpdatesSince failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if updates[0].SequenceNumber != update.SequenceNumber {
		t.Fatalf("persisted sequence_number=%d, want %d",
			updates[0].SequenceNumber, update.SequenceNumber)
	}
}

func TestCreateUserUpdate_ConcurrentWritesProduceUniqueSequences(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "user-concurrent-test"
	const updateCount = 10

	var wg sync.WaitGroup
	errCh := make(chan error, updateCount)

	for i := 0; i < updateCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := repo.CreateUserUpdate(ctx, &UserUpdate{
				UserID:     userID,
				UpdateType: UserUpdateChatActivityChanged,
				EntityType: EntityTypeChat,
				EntityID:   fmt.Sprintf("chat-%d", idx),
				Data:       []byte(fmt.Sprintf(`{"i":%d}`, idx)),
			})
			if err != nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("CreateUserUpdate concurrent write failed: %v", err)
		}
	}

	updates, err := repo.GetUserUpdatesSince(ctx, userID, 0, 100)
	if err != nil {
		t.Fatalf("GetUserUpdatesSince failed: %v", err)
	}
	if len(updates) != updateCount {
		t.Fatalf("expected %d updates, got %d", updateCount, len(updates))
	}

	// UNIQUENESS is the contract: the sequence number is the cursor a
	// reconnecting stream resumes from, and a duplicate makes a client skip or
	// replay updates.
	//
	// CONTIGUITY is deliberately NOT asserted. Allocation moved to a Postgres
	// sequence to stop concurrent writers aborting each other with SQLSTATE
	// 40001, and sequence values are gap-free only in the absence of rollbacks
	// — a retried transaction consumes its value permanently. No consumer
	// needs a dense range: readers page with `sequence_number > $cursor ORDER
	// BY sequence_number ASC` and the client stores a monotonic high-water
	// mark, both of which only require values to increase.
	seen := make(map[int64]bool, updateCount)
	for _, u := range updates {
		if u.SequenceNumber < 1 {
			t.Fatalf("expected a positive sequence number, got %d", u.SequenceNumber)
		}
		if seen[u.SequenceNumber] {
			t.Fatalf("duplicate sequence number detected: %d", u.SequenceNumber)
		}
		seen[u.SequenceNumber] = true
	}

	// Ordering must be strictly ascending, since that is what the resume
	// cursor relies on.
	for i := 1; i < len(updates); i++ {
		if updates[i].SequenceNumber <= updates[i-1].SequenceNumber {
			t.Fatalf("updates must be strictly ascending by sequence number, got %d after %d",
				updates[i].SequenceNumber, updates[i-1].SequenceNumber)
		}
	}
}
