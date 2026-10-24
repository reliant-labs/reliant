// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/workflow"
)

func TestProviderSwitchVersionMarkersAreBoundedAfterFirstNewAnswer(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	const id = providerSwitchChangeID + "/update/1"
	env.OnGetVersion(id, workflow.DefaultVersion, 1).Return(workflow.Version(1)).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) (int, error) {
		tracker := &ChildWorkflowTracker{}
		for n := 1; n <= 100; n++ {
			if !tracker.providerVersion(ctx, "update", n) {
				return n, nil
			}
		}
		return 100, nil
	})
	require.NoError(t, env.GetWorkflowError())
	var count int
	require.NoError(t, env.GetWorkflowResult(&count))
	require.Equal(t, 100, count)
	env.AssertExpectations(t)
}
