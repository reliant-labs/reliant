//go:build integration

// Copyright (c) 2025 Reliant Labs
package privatepipelineharness

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Infrastructure workflow only: verifies the real history adapter without
// pretending that a test-supplied cursor authenticates an author or verdict.
func continuationInfrastructure(ctx workflow.Context, successor bool) error {
	if !successor {
		return workflow.NewContinueAsNewError(ctx, continuationInfrastructure, true)
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
	if err := workflow.ExecuteActivity(ctx, "ContinuationReady").Get(ctx, nil); err != nil {
		return err
	}
	var stop bool
	workflow.GetSignalChannel(ctx, "fixture-stop").Receive(ctx, &stop)
	return nil
}

func TestFutureChatBRealHistoryContinuity(t *testing.T) {
	f := newResources(t)
	ready := make(chan runcontinuity.Execution, 1)
	w := worker.New(f.Temporal, f.Queue, worker.Options{})
	w.RegisterWorkflow(continuationInfrastructure)
	w.RegisterActivityWithOptions(func(ctx context.Context) error {
		info := activity.GetInfo(ctx)
		select {
		case ready <- runcontinuity.Execution{WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID}:
		default:
		}
		return nil
	}, activity.RegisterOptions{Name: "ContinuationReady"})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	run, err := f.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "history-" + uuid.NewString(), TaskQueue: f.Queue}, continuationInfrastructure, false)
	if err != nil {
		t.Fatal(err)
	}
	var successor runcontinuity.Execution
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if successor.RunID != "" {
			_ = f.Temporal.TerminateWorkflow(cleanup, successor.WorkflowID, successor.RunID, "owned exact fixture cleanup")
		}
	})
	select {
	case successor = <-ready:
	case <-ctx.Done():
		t.Fatal("actual continue-as-new successor did not execute its activity")
	}
	original := runcontinuity.Execution{WorkflowID: run.GetID(), RunID: run.GetRunID()}
	reader := runcontinuity.TemporalReader{Service: f.Temporal.WorkflowService(), Namespace: f.Namespace}
	proof, err := runcontinuity.Resolve(ctx, reader, original)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.Live || proof.Original != original || proof.Current != successor || len(proof.Edges) != 1 || proof.Edges[0].From != original || proof.Edges[0].To != successor || successor.RunID == original.RunID {
		t.Fatalf("real server did not authenticate exact internal continuation: %+v", proof)
	}
	// A namespace mismatch must fail, not fall back to latest/current or default.
	wrong := runcontinuity.TemporalReader{Service: f.Temporal.WorkflowService(), Namespace: "missing-" + uuid.NewString()}
	if _, err := runcontinuity.Resolve(ctx, wrong, original); err == nil {
		t.Fatal("wrong namespace unexpectedly authenticated continuation")
	}
	if err := f.Temporal.SignalWorkflow(ctx, successor.WorkflowID, successor.RunID, "fixture-stop", true); err != nil {
		t.Fatal(err)
	}
	if err := run.Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	closed, err := runcontinuity.Resolve(ctx, reader, original)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Live || closed.Current != successor {
		t.Fatal("terminal same-author chain was treated as a live replacement")
	}
}
