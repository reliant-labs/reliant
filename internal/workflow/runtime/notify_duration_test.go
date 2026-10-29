// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// runThenEnd is a root run that works for d and then reports its end.
func runThenEnd(status, parent string, d time.Duration) func(workflow.Context) error {
	return func(ctx workflow.Context) error {
		_ = workflow.Sleep(ctx, d)
		notifyWorkflowStatus(ctx, "chat-1", "wf-1", "builtin://agent", status, parent, "thread-1", nil)
		return nil
	}
}

func statusInput(t *testing.T, wf func(workflow.Context) error) map[string]interface{} {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var got map[string]interface{}
	env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		got = in
		return map[string]interface{}{}, nil
	}, activity.RegisterOptions{Name: "WorkflowStatus"})
	env.ExecuteWorkflow(wf)
	require.NoError(t, env.GetWorkflowError())
	return got
}

// workflow_ended.duration_ms was always 0: nothing told the status activity
// when the run began. A root run's terminal status carries how long it ran,
// read from the workflow's own clock.
func TestNotifyWorkflowStatus_TerminalRootStatusCarriesHowLongTheRunRan(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			got := statusInput(t, runThenEnd(status, "", 90*time.Second))
			assert.EqualValues(t, (90 * time.Second).Milliseconds(), got["duration_ms"])
		})
	}
}

// A run that is only starting, pausing or resuming has not ended, and a child
// run's end is not a product event: neither carries a duration.
func TestNotifyWorkflowStatus_OnlyARootRunsEndCarriesADuration(t *testing.T) {
	t.Parallel()
	assert.NotContains(t, statusInput(t, runThenEnd("started", "", time.Minute)), "duration_ms")
	assert.NotContains(t, statusInput(t, runThenEnd("paused", "", time.Minute)), "duration_ms")
	assert.NotContains(t, statusInput(t, runThenEnd("failed", "parent-wf", time.Minute)), "duration_ms")
}
