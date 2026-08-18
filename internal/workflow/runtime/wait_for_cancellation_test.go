// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// No step waits for a cancelled activity to return.
//
// WaitForCancellation used to be set (first on everything, then on call_llm
// alone), and it is what made stopping slow: it holds the activity future open
// until the activity's own return arrives, and that return only reaches the
// worker on a heartbeat, throttled to 3s. Measured on live chats while it was
// set: pause 1.05s, interrupt 1.97s, interrupt 2.81s — against a cancel REQUEST
// that always landed in ~10ms.
//
// It is gone because nothing needs it any more. Work that must survive
// cancellation is written by the activity itself: execute_tools upserts each
// tool's outcome through a detached context and a re-dispatch returns the
// recorded outcome instead of re-running it; call_llm persists its partial turn
// the same way, keyed on its position in the graph so the re-dispatch converges
// on one row. The SDK discards a cancelled activity's late return regardless, so
// waiting for one was never a guarantee — only a delay.
//
// This asserts the absence directly against the built options, so re-adding the
// flag anywhere in this path fails here rather than in a user's chat.
func TestActivityOptions_NeverWaitForCancellation(t *testing.T) {
	for _, nodeType := range []string{
		model.NodeTypeCallLLM,
		model.NodeTypeExecuteTools,
		model.NodeTypeSaveMessage,
	} {
		t.Run(nodeType, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()

			env.ExecuteWorkflow(func(ctx workflow.Context) (bool, error) {
				e := &StepExecutor{ctx: ctx, pauseCtrl: &PauseController{}}
				optsCtx := e.activityOptions(&reliantv1.Node{Id: "n", Type: nodeType})
				return workflow.GetActivityOptions(optsCtx).WaitForCancellation, nil
			})

			require.True(t, env.IsWorkflowCompleted())
			require.NoError(t, env.GetWorkflowError())

			var waits bool
			require.NoError(t, env.GetWorkflowResult(&waits))
			assert.False(t, waits,
				"%s must stop at cancel-request time, not wait out a heartbeat", nodeType)
		})
	}
}
