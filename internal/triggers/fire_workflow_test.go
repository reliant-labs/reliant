// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

func newWorkflowEnv(t *testing.T) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(TriggerFireWorkflow, workflow.RegisterOptions{Name: FireWorkflowName})
	return env
}

// The workflow's whole job is to hand the activity the two facts only it can
// read: its own id and the scheduled time.
func TestFireWorkflowPassesItsIdentityToTheActivity(t *testing.T) {
	env := newWorkflowEnv(t)

	var got FireRequest
	env.RegisterActivityWithOptions(
		func(_ context.Context, req FireRequest) (*FireOutput, error) {
			got = req
			return &FireOutput{Outcome: "launched", ChatID: "chat-1"}, nil
		},
		activity.RegisterOptions{Name: FireActivityName},
	)

	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: "trigger-fire-t1-2026-01-02T09:00:00Z"})
	env.ExecuteWorkflow(TriggerFireWorkflow, FireInput{TriggerID: "t1"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out FireOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if out.Outcome != "launched" || out.ChatID != "chat-1" {
		t.Errorf("out = %+v, want the activity's result passed through", out)
	}

	if got.TriggerID != "t1" {
		t.Errorf("TriggerID = %q", got.TriggerID)
	}
	if got.FireWorkflowID != "trigger-fire-t1-2026-01-02T09:00:00Z" {
		t.Errorf("FireWorkflowID = %q, want the workflow's own id", got.FireWorkflowID)
	}
	// With no scheduled-start search attribute the workflow falls back to the
	// start time, which is what a manual fire has.
	if got.ScheduledAt.IsZero() {
		t.Error("ScheduledAt is zero; the fallback to the start time did not apply")
	}
	if got.Manual {
		t.Error("Manual should be false")
	}
}

func TestFireWorkflowCarriesManualThrough(t *testing.T) {
	env := newWorkflowEnv(t)

	var got FireRequest
	env.RegisterActivityWithOptions(
		func(_ context.Context, req FireRequest) (*FireOutput, error) {
			got = req
			return &FireOutput{Outcome: "launched"}, nil
		},
		activity.RegisterOptions{Name: FireActivityName},
	)

	env.ExecuteWorkflow(TriggerFireWorkflow, FireInput{TriggerID: "t1", Manual: true})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	if !got.Manual {
		t.Error("Manual did not reach the activity")
	}
}

// A validation failure must end the fire on the first attempt. The retry
// policy's NonRetryableErrorTypes is what makes that true, and it is the one
// part of this workflow that can silently stop working.
func TestFireWorkflowDoesNotRetryValidationFailures(t *testing.T) {
	env := newWorkflowEnv(t)

	attempts := 0
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ FireRequest) (*FireOutput, error) {
			attempts++
			return nil, temporal.NewNonRetryableApplicationError(
				"workflow does not exist", nonRetryableFireError, nil)
		},
		activity.RegisterOptions{Name: FireActivityName},
	)

	env.ExecuteWorkflow(TriggerFireWorkflow, FireInput{TriggerID: "t1"})
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("want the validation failure surfaced as a workflow error")
	}
	if attempts != 1 {
		t.Errorf("activity ran %d times, want 1: validation failures must not be retried", attempts)
	}
}

func TestFireWorkflowRetriesTransientFailures(t *testing.T) {
	env := newWorkflowEnv(t)

	attempts := 0
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ FireRequest) (*FireOutput, error) {
			attempts++
			if attempts < 3 {
				return nil, errors.New("connection reset by peer")
			}
			return &FireOutput{Outcome: "launched"}, nil
		},
		activity.RegisterOptions{Name: FireActivityName},
	)

	env.ExecuteWorkflow(TriggerFireWorkflow, FireInput{TriggerID: "t1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error after retries: %v", err)
	}
	if attempts != 3 {
		t.Errorf("activity ran %d times, want 3 (two failures then success)", attempts)
	}
}

// The real Firer, driven through the real workflow, with fakes underneath.
func TestFireWorkflowWithTheRealActivity(t *testing.T) {
	env := newWorkflowEnv(t)

	repo := newFakeRepo()
	trigger := testTrigger(t, nil)
	repo.triggers[trigger.ID] = trigger
	launcher := &fakeLauncher{}

	env.RegisterActivityWithOptions(
		NewFirer(repo, launcher).Fire,
		activity.RegisterOptions{Name: FireActivityName},
	)

	fireID := FireWorkflowID(trigger.ID) + "-2026-01-02T09:00:00Z"
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: fireID})
	env.ExecuteWorkflow(TriggerFireWorkflow, FireInput{TriggerID: trigger.ID})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out FireOutput
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("GetWorkflowResult: %v", err)
	}
	if out.Outcome != "launched" {
		t.Fatalf("Outcome = %q (%+v)", out.Outcome, out)
	}
	calls := launcher.snapshot()
	if len(calls) != 1 {
		t.Fatalf("launcher got %d calls", len(calls))
	}
	if calls[0].Event.DedupeKey != fireID {
		t.Errorf("DedupeKey = %q, want the fire workflow id %q", calls[0].Event.DedupeKey, fireID)
	}
}
