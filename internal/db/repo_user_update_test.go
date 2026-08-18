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

	// A new logical user stream starts at one. Other users consume independent
	// counters, so their traffic cannot create gaps in this cursor.
	if update.SequenceNumber != 1 {
		t.Fatalf("expected first user sequence_number=1, got %d", update.SequenceNumber)
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

	// The scoped counter and ledger insert share a transaction, so even under
	// concurrent writes the committed stream is dense as well as unique.
	for i, update := range updates {
		want := int64(i + 1)
		if update.SequenceNumber != want {
			t.Fatalf("update %d sequence_number=%d, want %d; stream must be contiguous",
				i, update.SequenceNumber, want)
		}
	}
}
