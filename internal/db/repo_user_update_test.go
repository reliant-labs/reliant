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

	if update.SequenceNumber != 1 {
		t.Fatalf("expected sequence_number=1, got %d", update.SequenceNumber)
	}
	if update.ID != fmt.Sprintf("%s-%d", userID, 1) {
		t.Fatalf("unexpected generated ID: %s", update.ID)
	}

	updates, err := repo.GetUserUpdatesSince(ctx, userID, 0, 10)
	if err != nil {
		t.Fatalf("GetUserUpdatesSince failed: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if updates[0].SequenceNumber != 1 {
		t.Fatalf("expected persisted sequence_number=1, got %d", updates[0].SequenceNumber)
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

	seen := make(map[int64]bool, updateCount)
	for _, u := range updates {
		if u.SequenceNumber < 1 || u.SequenceNumber > updateCount {
			t.Fatalf("sequence out of expected range: %d", u.SequenceNumber)
		}
		if seen[u.SequenceNumber] {
			t.Fatalf("duplicate sequence number detected: %d", u.SequenceNumber)
		}
		seen[u.SequenceNumber] = true
	}
	for seq := int64(1); seq <= updateCount; seq++ {
		if !seen[seq] {
			t.Fatalf("missing sequence number %d", seq)
		}
	}
}
