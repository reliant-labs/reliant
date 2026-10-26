// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The failed-start stamp is what tells a stranded launch (PENDING, stamped)
// from a start in flight (PENDING, not), so each write is conditional on the
// row the overlap guard and a retry of the start race over.
func TestWorkflowStartFailedStampLifecycle(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	seedChat(t, env, "chat-s")
	for _, w := range []*core.Workflow{
		newWorkflow("root-pending", "chat-s", nil, core.Pending(), l2Base),
		newWorkflow("root-active", "chat-s", nil, core.Active(), l2Base),
	} {
		if err := store.InsertWorkflowIfAbsent(ctx, env.ORM, w); err != nil {
			t.Fatalf("InsertWorkflowIfAbsent(%s): %v", w.ID, err)
		}
	}
	load := func(id string) *core.Workflow {
		t.Helper()
		w, err := store.LoadWorkflow(ctx, env.ORM, id)
		if err != nil {
			t.Fatalf("LoadWorkflow(%s): %v", id, err)
		}
		return w
	}
	must := func(what string, got bool, err error, want bool) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if got != want {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}

	if load("root-pending").StartFailedAt != nil {
		t.Fatal("a new root must start with no failed start recorded")
	}

	// A start in flight is not stranded: there is nothing to abandon.
	ok, err := store.AbandonFailedWorkflowStart(ctx, env.ORM, "root-pending")
	must("abandon an unstamped root", ok, err, false)

	ok, err = store.MarkWorkflowStartFailed(ctx, env.ORM, "root-pending")
	must("mark a pending root", ok, err, true)
	stamped := load("root-pending").StartFailedAt
	if stamped == nil {
		t.Fatal("MarkWorkflowStartFailed left no stamp")
	}
	ok, err = store.MarkWorkflowStartFailed(ctx, env.ORM, "root-pending")
	must("mark it again", ok, err, false)
	if got := load("root-pending").StartFailedAt; got == nil || !got.Equal(*stamped) {
		t.Fatalf("a second mark moved the stamp: %v -> %v", stamped, got)
	}

	// A retry claims the start back: the stamp clears and the row stays
	// PENDING, so a fire arriving now skips behind it.
	ok, err = store.ClaimWorkflowStart(ctx, env.ORM, "root-pending")
	must("claim a stamped pending root", ok, err, true)
	if got := load("root-pending"); got.StartFailedAt != nil || got.Status != core.Pending() {
		t.Fatalf("after the claim: %+v, want PENDING with no stamp", got)
	}

	// Failed again, then abandoned by the next fire: the retry that comes
	// after finds the start no longer its to make.
	ok, err = store.MarkWorkflowStartFailed(ctx, env.ORM, "root-pending")
	must("mark after the claim", ok, err, true)
	ok, err = store.AbandonFailedWorkflowStart(ctx, env.ORM, "root-pending")
	must("abandon a stamped root", ok, err, true)
	if got := load("root-pending"); got.Status != core.Failed() || got.CompletedAt == nil {
		t.Fatalf("abandoned root = %+v, want STOPPED/FAILED with completed_at", got)
	}
	ok, err = store.ClaimWorkflowStart(ctx, env.ORM, "root-pending")
	must("claim an abandoned root", ok, err, false)

	// A run that left PENDING is never stamped, claimed or abandoned.
	ok, err = store.MarkWorkflowStartFailed(ctx, env.ORM, "root-active")
	must("mark an active root", ok, err, false)
	ok, err = store.ClaimWorkflowStart(ctx, env.ORM, "root-active")
	must("claim an active root", ok, err, false)
	ok, err = store.AbandonFailedWorkflowStart(ctx, env.ORM, "root-active")
	must("abandon an active root", ok, err, false)
	if got := load("root-active"); got.Status != core.Active() || got.StartFailedAt != nil {
		t.Fatalf("active root = %+v, want untouched", got)
	}
}
