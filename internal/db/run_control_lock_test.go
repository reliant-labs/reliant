// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"
)

// The lock must exclude a second holder for the SAME chat — that is the whole
// point — and must not couple unrelated chats, or one slow pause would stall
// every send in the system.
func TestLockChatRunControl_SerializesOneChatOnly(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()

	release, err := repo.LockChatRunControl(ctx, "chat-a")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	otherChat, err := repo.LockChatRunControl(ctx, "chat-b")
	if err != nil {
		t.Fatalf("a different chat must not wait: %v", err)
	}
	otherChat()

	acquired := make(chan func(), 1)
	go func() {
		second, err := repo.LockChatRunControl(ctx, "chat-a")
		if err != nil {
			t.Errorf("second acquire: %v", err)
			close(acquired)
			return
		}
		acquired <- second
	}()

	select {
	case <-acquired:
		t.Fatal("a second holder for the same chat got the lock while the first still held it")
	case <-time.After(200 * time.Millisecond):
	}

	release()
	release() // idempotent: a deferred release after an early one must be harmless

	select {
	case second, ok := <-acquired:
		if ok {
			second()
		}
	case <-time.After(10 * time.Second):
		t.Fatal("releasing the lock did not let the waiter in")
	}
}

// A request that goes away while waiting must stop waiting, not hold a pooled
// connection until the holder finishes.
func TestLockChatRunControl_CancelledWaitGivesUp(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	t.Cleanup(cleanup)

	release, err := repo.LockChatRunControl(context.Background(), "chat-a")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	t.Cleanup(release)

	waitCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := repo.LockChatRunControl(waitCtx, "chat-a"); err == nil {
		t.Fatal("a cancelled wait must return an error, not the lock")
	}
}
