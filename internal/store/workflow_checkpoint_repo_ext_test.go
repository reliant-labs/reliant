// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestWorkflowCheckpoint: one position per workflow, replaced on every save,
// stamped by the database, read and cleared per workflow.
func TestWorkflowCheckpoint(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()

	if cp, err := store.LoadWorkflowCheckpoint(ctx, env.ORM, "wf-cp"); cp != nil || err != nil {
		t.Fatalf("no checkpoint yet: %+v, %v; want nil, nil", cp, err)
	}

	// updated_at is the database's NOW(), whatever the caller carries.
	stale := time.Now().Add(-24 * time.Hour)
	save := func(cp *core.WorkflowCheckpoint) {
		t.Helper()
		if err := store.SaveWorkflowCheckpoint(ctx, env.ORM, cp); err != nil {
			t.Fatal(err)
		}
	}
	save(&core.WorkflowCheckpoint{WorkflowID: "wf-cp", ChatID: "chat-1", NodeID: "plan", UpdatedAt: stale})
	first, err := store.LoadWorkflowCheckpoint(ctx, env.ORM, "wf-cp")
	if err != nil || first == nil {
		t.Fatalf("after the first save: %+v, %v", first, err)
	}
	requireRecent(t, "first updated_at", first.UpdatedAt)
	requireSame(t, "first save", first, &core.WorkflowCheckpoint{WorkflowID: "wf-cp", ChatID: "chat-1", NodeID: "plan", UpdatedAt: first.UpdatedAt})

	// Saving again replaces every field of the one row, and restamps it.
	save(&core.WorkflowCheckpoint{WorkflowID: "wf-cp", ChatID: "chat-2", NodeID: "loop", LoopIteration: 3})
	second, _ := store.LoadWorkflowCheckpoint(ctx, env.ORM, "wf-cp")
	requireSame(t, "second save", second, &core.WorkflowCheckpoint{WorkflowID: "wf-cp", ChatID: "chat-2", NodeID: "loop", LoopIteration: 3, UpdatedAt: second.UpdatedAt})
	if second.UpdatedAt.Before(first.UpdatedAt) {
		t.Fatalf("updated_at went backwards: %v then %v", first.UpdatedAt, second.UpdatedAt)
	}
	// Leaving the loop resets the iteration rather than keeping the old one.
	save(&core.WorkflowCheckpoint{WorkflowID: "wf-cp", ChatID: "chat-2", NodeID: "summarize"})
	third, _ := store.LoadWorkflowCheckpoint(ctx, env.ORM, "wf-cp")
	requireSame(t, "third save", third, &core.WorkflowCheckpoint{WorkflowID: "wf-cp", ChatID: "chat-2", NodeID: "summarize", UpdatedAt: third.UpdatedAt})
	var rows int
	if err := env.SQL.QueryRow(`SELECT count(*) FROM workflow_checkpoints`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("%d checkpoint rows after three saves, %v; want one per workflow", rows, err)
	}

	// Another workflow has its own position, and clearing one leaves it alone.
	save(&core.WorkflowCheckpoint{WorkflowID: "wf-other", ChatID: "chat-1", NodeID: "review", LoopIteration: 1})
	if err := store.ClearWorkflowCheckpoint(ctx, env.ORM, "wf-cp"); err != nil {
		t.Fatal(err)
	}
	if cp, err := store.LoadWorkflowCheckpoint(ctx, env.ORM, "wf-cp"); cp != nil || err != nil {
		t.Fatalf("after clear: %+v, %v; want nil, nil", cp, err)
	}
	if err := store.ClearWorkflowCheckpoint(ctx, env.ORM, "wf-cp"); err != nil {
		t.Fatalf("clearing a cleared checkpoint is a no-op: %v", err)
	}
	if err := store.ClearWorkflowCheckpoint(ctx, env.ORM, "never-saved"); err != nil {
		t.Fatalf("clearing a workflow that never saved is a no-op: %v", err)
	}
	other, err := store.LoadWorkflowCheckpoint(ctx, env.ORM, "wf-other")
	if err != nil || other == nil {
		t.Fatalf("clearing wf-cp removed wf-other's checkpoint: %+v, %v", other, err)
	}
	requireSame(t, "other workflow", other, &core.WorkflowCheckpoint{WorkflowID: "wf-other", ChatID: "chat-1", NodeID: "review", LoopIteration: 1, UpdatedAt: other.UpdatedAt})

	// A saved-again position after a clear starts over.
	save(&core.WorkflowCheckpoint{WorkflowID: "wf-cp", ChatID: "chat-3", NodeID: "restart"})
	again, _ := store.LoadWorkflowCheckpoint(ctx, env.ORM, "wf-cp")
	requireSame(t, "save after clear", again, &core.WorkflowCheckpoint{WorkflowID: "wf-cp", ChatID: "chat-3", NodeID: "restart", UpdatedAt: again.UpdatedAt})
}
