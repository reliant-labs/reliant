// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The empty-id and empty-user guards of the two facade methods wave 25 deleted
// (SetTriggerWebhookSecret, RecentTriggerFirings) live in the store functions:
// the handler reaches them through the generated ext stores, so a blank id
// must fail before any SQL, never write or read "no trigger".
func TestWebhookSecretAndFiringsReadsFailClosedOnABlankID(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	if err := store.NewTriggerExtStore(env.ORM).SetTriggerWebhookSecret(ctx, "", []byte("sealed")); err == nil {
		t.Fatal("SetTriggerWebhookSecret(\"\") = nil, want an error")
	}
	if _, err := store.NewTriggerEventExtStore(env.ORM).RecentTriggerFirings(ctx, "", []string{"t"}, 5); err == nil {
		t.Fatal("RecentTriggerFirings(no user) = nil error, want one")
	}
}

// The empty-id guards the repository facade used to carry live in the store
// functions, so the generated TriggerExtStore refuses an empty id before any
// SQL: a lock or projection write can never be aimed at "no trigger".
func TestTriggerLockAndProjectionFailClosedOnAnEmptyID(t *testing.T) {
	t.Parallel()
	env := storetest.New(t)
	ctx := context.Background()
	if err := store.LockTrigger(ctx, env.ORM, ""); err == nil {
		t.Fatal("LockTrigger(\"\") = nil, want an error")
	}
	if err := store.SetTriggerProjection(ctx, env.ORM, "", core.TriggerProjection{Filter: "x"}); err == nil {
		t.Fatal("SetTriggerProjection(\"\") = nil, want an error")
	}
	ext := store.NewTriggerExtStore(env.ORM)
	if err := ext.LockTrigger(ctx, ""); err == nil {
		t.Fatal("TriggerExtStore.LockTrigger(\"\") = nil, want an error")
	}
}
