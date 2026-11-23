// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The empty-id guards the repository facade used to carry live in the store
// functions, so the generated TriggerExtStore refuses an empty id before any
// SQL: a lock or projection write can never be aimed at "no trigger".
func TestTriggerLockAndProjectionFailClosedOnAnEmptyID(t *testing.T) {
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
