// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

func notifyOnlyWorkflow(ctx workflow.Context, opts *workflowStatusOpts) error {
	notifyWorkflowStatus(ctx, "chat-1", "wf-1", "builtin://agent", "paused", "", "thread-1", opts)
	return nil
}

// The breaker's pause is the only one that asks to be resumed when the machine
// connects; the flag has to reach the WorkflowStatus activity for the chat to
// be marked, and any other pause must not carry it.
func TestNotifyWorkflowStatus_PausedForMachineReachesTheActivity(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		opts *workflowStatusOpts
		want bool
	}{
		"breaker pause": {&workflowStatusOpts{PausedForMachine: true}, true},
		"other pause":   {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var suite temporaltest.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			var got map[string]interface{}
			env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
				got = in
				return map[string]interface{}{}, nil
			}, activity.RegisterOptions{Name: "WorkflowStatus"})

			env.ExecuteWorkflow(notifyOnlyWorkflow, tc.opts)

			require.NoError(t, env.GetWorkflowError())
			require.Equal(t, "paused", got["status"])
			v, present := got["paused_for_machine"]
			require.Equal(t, tc.want, present && v == true)
		})
	}
}
