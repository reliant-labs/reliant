// Copyright (c) 2025 Reliant Labs
package temporaltest

import (
	"os"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// slowWorkflowTask stands in for a workflow task on a loaded machine: it holds
// the workflow goroutine without yielding for longer than the SDK's default
// one-second budget, but nowhere near a genuine hang.
func slowWorkflowTask(workflow.Context) error {
	start := time.Now()
	for time.Since(start) < 1500*time.Millisecond {
	}
	return nil
}

// The pair is the proof: the same task fails under the SDK's own suite and
// completes under this one. The SDK half also pins the default this package
// exists to override — if a Temporal upgrade changes it, this says so.
func TestWorkflowTestSuiteToleratesASlowWorkflowTask(t *testing.T) {
	t.Run("SDK default fails it as a deadlock", func(t *testing.T) {
		if os.Getenv("TEMPORAL_DEBUG") != "" {
			t.Skip("TEMPORAL_DEBUG disables the SDK's deadlock detector")
		}
		var sdkSuite testsuite.WorkflowTestSuite
		env := sdkSuite.NewTestWorkflowEnvironment()
		env.ExecuteWorkflow(slowWorkflowTask)

		err := env.GetWorkflowError()
		if err == nil || !strings.Contains(err.Error(), "TMPRL1101") {
			t.Fatalf("workflow error = %v, want the SDK's TMPRL1101 deadlock failure", err)
		}
	})

	t.Run("harness suite lets it finish", func(t *testing.T) {
		var suite WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.ExecuteWorkflow(slowWorkflowTask)

		if !env.IsWorkflowCompleted() {
			t.Fatal("workflow did not complete")
		}
		if err := env.GetWorkflowError(); err != nil {
			t.Fatalf("workflow error = %v, want nil: a 1.5s task is slow, not deadlocked", err)
		}
	})
}

// Embedding is how the testify suites in this repository use the SDK type, so
// the shadowing method must win there too.
func TestWorkflowTestSuiteShadowsWhenEmbedded(t *testing.T) {
	var harness struct {
		WorkflowTestSuite
	}
	env := harness.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(slowWorkflowTask)

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error = %v, want nil through an embedded suite", err)
	}
}

func TestWorkerOptions(t *testing.T) {
	t.Run("applies the harness timeout", func(t *testing.T) {
		t.Setenv("TEMPORAL_DEBUG", "")
		got := WorkerOptions(worker.Options{Identity: "kept"})
		if got.DeadlockDetectionTimeout != DeadlockDetectionTimeout {
			t.Fatalf("DeadlockDetectionTimeout = %v, want %v", got.DeadlockDetectionTimeout, DeadlockDetectionTimeout)
		}
		if got.Identity != "kept" {
			t.Fatalf("Identity = %q, want the caller's other options preserved", got.Identity)
		}
	})

	t.Run("keeps an explicit timeout", func(t *testing.T) {
		t.Setenv("TEMPORAL_DEBUG", "")
		got := WorkerOptions(worker.Options{DeadlockDetectionTimeout: 5 * time.Second})
		if got.DeadlockDetectionTimeout != 5*time.Second {
			t.Fatalf("DeadlockDetectionTimeout = %v, want the caller's 5s", got.DeadlockDetectionTimeout)
		}
	})

	t.Run("leaves TEMPORAL_DEBUG to the SDK", func(t *testing.T) {
		t.Setenv("TEMPORAL_DEBUG", "1")
		if got := WorkerOptions(worker.Options{}); got.DeadlockDetectionTimeout != 0 {
			t.Fatalf("DeadlockDetectionTimeout = %v, want 0 so the SDK's debug mode disables detection", got.DeadlockDetectionTimeout)
		}
	})
}
