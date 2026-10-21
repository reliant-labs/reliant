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

// TestWorkflowCheckpointAgreesWithSQLC: one position per workflow, replaced
// on every save, read and cleared through either layer.
func TestWorkflowCheckpointAgreesWithSQLC(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()

	if cp, err := store.LoadWorkflowCheckpoint(ctx, env.ORM, "wf-cp"); cp != nil || err != nil {
		t.Fatalf("no checkpoint yet: %+v, %v; want nil, nil", cp, err)
	}
	stale := time.Now().Add(-24 * time.Hour)
	if err := store.SaveWorkflowCheckpoint(ctx, env.ORM, &core.WorkflowCheckpoint{
		WorkflowID: "wf-cp", ChatID: "chat-1", NodeID: "plan", UpdatedAt: stale,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := env.Repo.GetWorkflowCheckpoint(ctx, "wf-cp")
	if err != nil || first == nil || first.NodeID != "plan" || first.LoopIteration != 0 || first.UpdatedAt.Before(stale.Add(time.Hour)) {
		t.Fatalf("after the first save the twin read %+v, %v", first, err)
	}

	// The twin overwrites; the port reads it back.
	if err := env.Repo.UpsertWorkflowCheckpoint(ctx, &core.WorkflowCheckpoint{
		WorkflowID: "wf-cp", ChatID: "chat-2", NodeID: "loop", LoopIteration: 3,
	}); err != nil {
		t.Fatal(err)
	}
	port, err := store.LoadWorkflowCheckpoint(ctx, env.ORM, "wf-cp")
	if err != nil {
		t.Fatal(err)
	}
	twin, _ := env.Repo.GetWorkflowCheckpoint(ctx, "wf-cp")
	requireSame(t, "checkpoint", port, twin)
	if port.ChatID != "chat-2" || port.NodeID != "loop" || port.LoopIteration != 3 {
		t.Fatalf("checkpoint = %+v", port)
	}

	// The port overwrites again, then clears.
	if err := store.SaveWorkflowCheckpoint(ctx, env.ORM, &core.WorkflowCheckpoint{
		WorkflowID: "wf-cp", ChatID: "chat-2", NodeID: "loop", LoopIteration: 4,
	}); err != nil {
		t.Fatal(err)
	}
	if twin, _ := env.Repo.GetWorkflowCheckpoint(ctx, "wf-cp"); twin.LoopIteration != 4 || twin.UpdatedAt.Before(port.UpdatedAt) {
		t.Fatalf("second save: %+v", twin)
	}
	if err := store.ClearWorkflowCheckpoint(ctx, env.ORM, "wf-cp"); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearWorkflowCheckpoint(ctx, env.ORM, "wf-cp"); err != nil {
		t.Fatalf("clearing a cleared checkpoint is a no-op, as its twin is: %v", err)
	}
	if err := env.Repo.DeleteWorkflowCheckpoint(ctx, "wf-cp"); err != nil {
		t.Fatal(err)
	}
	if twin, err := env.Repo.GetWorkflowCheckpoint(ctx, "wf-cp"); twin != nil || err != nil {
		t.Fatalf("after clear: %+v, %v", twin, err)
	}
}
