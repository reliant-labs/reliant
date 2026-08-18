package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestUpdateStreamCountersAreScoped(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	for _, userID := range []string{"scoped-user-a", "scoped-user-b"} {
		update := &UserUpdate{
			UserID:     userID,
			UpdateType: UserUpdateNotification,
			EntityType: EntityTypeSystem,
			EntityID:   "notice",
			Data:       []byte(`{"ok":true}`),
		}
		if err := repo.CreateUserUpdate(ctx, update); err != nil {
			t.Fatalf("CreateUserUpdate(%s): %v", userID, err)
		}
		if update.SequenceNumber != 1 {
			t.Fatalf("first update for %s got sequence %d, want 1", userID, update.SequenceNumber)
		}
	}

	for _, chatID := range []string{"scoped-chat-a", "scoped-chat-b"} {
		if err := repo.CreateChatUpdate(ctx, chatID, UpdateTypeInfo, "info", `{"ok":true}`); err != nil {
			t.Fatalf("CreateChatUpdate(%s): %v", chatID, err)
		}
		updates, err := repo.GetUpdatesSince(ctx, chatID, 0, 10)
		if err != nil {
			t.Fatalf("GetUpdatesSince(%s): %v", chatID, err)
		}
		if len(updates) != 1 || updates[0].SequenceNumber != 1 {
			t.Fatalf("first update for %s = %+v, want one row at sequence 1", chatID, updates)
		}
	}
}

func TestUserUpdateCounterRollsBackWithLedgerWrite(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	rollback := errors.New("force rollback")
	err := repo.RunTx(ctx, func(txCtx context.Context) error {
		if err := repo.CreateUserUpdate(txCtx, &UserUpdate{
			UserID:     "rollback-user",
			UpdateType: UserUpdateNotification,
			EntityType: EntityTypeSystem,
			EntityID:   "discarded",
			Data:       []byte(`{}`),
		}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("RunTx error=%v, want forced rollback", err)
	}

	committed := &UserUpdate{
		UserID:     "rollback-user",
		UpdateType: UserUpdateNotification,
		EntityType: EntityTypeSystem,
		EntityID:   "committed",
		Data:       []byte(`{}`),
	}
	if err := repo.CreateUserUpdate(ctx, committed); err != nil {
		t.Fatalf("CreateUserUpdate after rollback: %v", err)
	}
	if committed.SequenceNumber != 1 {
		t.Fatalf("sequence after rollback=%d, want reused sequence 1", committed.SequenceNumber)
	}
}

func TestChatUpdateCounterRollsBackWithLedgerWrite(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const chatID = "rollback-chat"
	rollback := errors.New("force rollback")
	err := repo.RunTx(ctx, func(txCtx context.Context) error {
		if err := repo.CreateChatUpdate(txCtx, chatID, UpdateTypeInfo, "discarded", `{}`); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("RunTx error=%v, want forced rollback", err)
	}

	if err := repo.CreateChatUpdate(ctx, chatID, UpdateTypeInfo, "committed", `{}`); err != nil {
		t.Fatalf("CreateChatUpdate after rollback: %v", err)
	}
	updates, err := repo.GetUpdatesSince(ctx, chatID, 0, 10)
	if err != nil {
		t.Fatalf("GetUpdatesSince: %v", err)
	}
	if len(updates) != 1 || updates[0].SequenceNumber != 1 {
		t.Fatalf("updates after rollback=%+v, want one row at sequence 1", updates)
	}
}

func TestConcurrentChatUpdatesAreContiguous(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	const (
		chatID      = "concurrent-scoped-chat"
		updateCount = 10
	)

	var wg sync.WaitGroup
	errCh := make(chan error, updateCount)
	for i := 0; i < updateCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if err := repo.CreateChatUpdate(
				ctx,
				chatID,
				UpdateTypeInfo,
				fmt.Sprintf("info-%d", index),
				`{}`,
			); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent CreateChatUpdate failed: %v", err)
	}

	updates, err := repo.GetUpdatesSince(ctx, chatID, 0, updateCount+1)
	if err != nil {
		t.Fatalf("GetUpdatesSince: %v", err)
	}
	if len(updates) != updateCount {
		t.Fatalf("got %d updates, want %d", len(updates), updateCount)
	}
	for i, update := range updates {
		want := int64(i + 1)
		if update.SequenceNumber != want {
			t.Fatalf("update %d sequence=%d, want %d", i, update.SequenceNumber, want)
		}
	}
}

func TestUpdateSequenceAllocatorsRequireLedgerTransaction(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	if _, err := repo.allocateUpdateSequence(ctx, updateStreamKindUser, "unbound-user"); err == nil {
		t.Fatal("user sequence allocation outside transaction succeeded")
	}
	if _, err := repo.allocateUpdateSequence(ctx, updateStreamKindChat, "unbound-chat"); err == nil {
		t.Fatal("chat sequence allocation outside transaction succeeded")
	}
}

func TestUpdateNotificationsRunOnlyAfterOutermostCommit(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	var userNotifications, chatNotifications int
	repo.SetUpdateNotifiers(
		func(update *UserUpdate) {
			userNotifications++
			if update.SequenceNumber != 1 {
				t.Errorf("notified user sequence=%d, want 1", update.SequenceNumber)
			}
		},
		func(_ string, sequenceNumber int64, _ ChatUpdate) {
			chatNotifications++
			if sequenceNumber != 1 {
				t.Errorf("notified chat sequence=%d, want 1", sequenceNumber)
			}
		},
	)

	ctx := context.Background()
	rollback := errors.New("force rollback")
	err := repo.RunTx(ctx, func(txCtx context.Context) error {
		if err := repo.CreateUserUpdate(txCtx, &UserUpdate{
			UserID:     "notification-user",
			UpdateType: UserUpdateNotification,
			EntityType: EntityTypeSystem,
			EntityID:   "discarded",
			Data:       []byte(`{}`),
		}); err != nil {
			return err
		}
		if err := repo.CreateChatUpdate(txCtx, "notification-chat", UpdateTypeInfo, "discarded", `{}`); err != nil {
			return err
		}
		if userNotifications != 0 || chatNotifications != 0 {
			t.Fatalf("notifications ran before outer commit: user=%d chat=%d", userNotifications, chatNotifications)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("RunTx error=%v, want forced rollback", err)
	}
	if userNotifications != 0 || chatNotifications != 0 {
		t.Fatalf("rolled-back transaction published notifications: user=%d chat=%d", userNotifications, chatNotifications)
	}

	err = repo.RunTx(ctx, func(txCtx context.Context) error {
		if err := repo.CreateUserUpdate(txCtx, &UserUpdate{
			UserID:     "notification-user",
			UpdateType: UserUpdateNotification,
			EntityType: EntityTypeSystem,
			EntityID:   "committed",
			Data:       []byte(`{}`),
		}); err != nil {
			return err
		}
		if err := repo.CreateChatUpdate(txCtx, "notification-chat", UpdateTypeInfo, "committed", `{}`); err != nil {
			return err
		}
		if userNotifications != 0 || chatNotifications != 0 {
			t.Fatalf("notifications ran before outer commit: user=%d chat=%d", userNotifications, chatNotifications)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("committing RunTx: %v", err)
	}
	if userNotifications != 1 || chatNotifications != 1 {
		t.Fatalf("committed notifications: user=%d chat=%d, want one each", userNotifications, chatNotifications)
	}
}
