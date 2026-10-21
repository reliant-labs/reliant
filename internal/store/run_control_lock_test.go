// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// previousReleaseLock takes the run-control lock exactly as the release
// before this port does: a hand database/sql transaction on the pool, with
// the same statements and key. During a rolling deploy both releases take it,
// so they must exclude each other.
func previousReleaseLock(ctx context.Context, env *storetest.Env, chatID string) (func(), error) {
	tx, err := env.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "run-control:"+chatID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return func() { _ = tx.Rollback() }, nil
}

// TestRunControlLockExcludesAcrossLayers: a lock held through the port blocks
// a taker on the hand path, and the other way round, and the Repository's
// method is the port.
func TestRunControlLockExcludesAcrossLayers(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()

	type taker struct {
		name string
		lock func(chatID string) (func(), error)
	}
	port := taker{"port", func(id string) (func(), error) { return store.LockChatRunControl(ctx, env.ORM, id) }}
	repo := taker{"repo", func(id string) (func(), error) { return env.Repo.LockChatRunControl(ctx, id) }}
	hand := taker{"previous release", func(id string) (func(), error) { return previousReleaseLock(ctx, env, id) }}

	for _, pair := range [][2]taker{{port, hand}, {hand, port}, {port, repo}} {
		holder, waiter := pair[0], pair[1]
		chatID := "chat-rc-" + holder.name + "-" + waiter.name
		release, err := holder.lock(chatID)
		if err != nil {
			t.Fatalf("%s lock: %v", holder.name, err)
		}
		got := make(chan time.Time, 1)
		go func() {
			rel, err := waiter.lock(chatID)
			if err != nil {
				t.Errorf("%s lock: %v", waiter.name, err)
				got <- time.Time{}
				return
			}
			got <- time.Now()
			rel()
		}()
		time.Sleep(150 * time.Millisecond)
		releasedAt := time.Now()
		release()
		release() // idempotent
		if acquired := <-got; acquired.Before(releasedAt) {
			t.Fatalf("%s acquired at %v, before the %s holder released at %v", waiter.name, acquired, holder.name, releasedAt)
		}
	}

	// Another chat's lock is independent.
	held, err := store.LockChatRunControl(ctx, env.ORM, "chat-rc-a")
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	other, err := store.LockChatRunControl(ctx, env.ORM, "chat-rc-b")
	if err != nil {
		t.Fatalf("independent chat: %v", err)
	}
	other()
}

// TestRunControlLockReleasedWhenCtxEnds: database/sql rolls the owning
// transaction back when ctx is done, so an abandoned request frees the lock.
func TestRunControlLockReleasedWhenCtxEnds(t *testing.T) {
	env := storetest.New(t)
	reqCtx, cancel := context.WithCancel(context.Background())
	if _, err := store.LockChatRunControl(reqCtx, env.ORM, "chat-rc-ctx"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	cancel()

	waitCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	rel, err := store.LockChatRunControl(waitCtx, env.ORM, "chat-rc-ctx")
	if err != nil {
		t.Fatalf("lock after the holder's ctx ended: %v", err)
	}
	rel()
}

// TestRunControlLockRefusedInsideTx: taken inside a transaction of either
// layer, the lock would be released at that transaction's commit.
func TestRunControlLockRefusedInsideTx(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	err := env.ORM.RunTx(ctx, func(txCtx context.Context) error {
		_, err := store.LockChatRunControl(txCtx, env.ORM, "chat-rc-tx")
		return err
	})
	if !errors.Is(err, store.ErrRunControlInTx) {
		t.Fatalf("inside orm RunTx: got %v, want ErrRunControlInTx", err)
	}
	err = env.Repo.RunTx(ctx, func(txCtx context.Context) error {
		_, err := env.Repo.LockChatRunControl(txCtx, "chat-rc-tx")
		return err
	})
	if !errors.Is(err, store.ErrRunControlInTx) {
		t.Fatalf("inside Repo.RunTx: got %v, want ErrRunControlInTx", err)
	}
}
