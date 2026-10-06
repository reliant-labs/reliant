// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// Stopping an agent records a cancellation under its thread id. A later
// spawn(agent_id=X) reuses that thread id, so without scoping the resumption
// is cancelled at its first step boundary.

type resumeProbe struct {
	CancelledAfterPrepare  bool
	CancelledAfterNewStop  bool
	OldToolCallStaysCancel bool
}

func resumeAfterStopWorkflow(ctx workflow.Context) (resumeProbe, error) {
	cancelled := map[string]bool{}
	setupCancelThreadHandler(ctx, cancelled, "wf-root")

	makeCtrl := func(thread string) *PauseController {
		return &PauseController{
			Cancelled:      func() bool { return cancelled[thread] },
			ResetCancelled: func() { delete(cancelled, thread) },
		}
	}

	// The user stops agent-X's first run (tool call tc-1) and the agent is
	// resumed by a new tool call tc-2.
	_ = workflow.Sleep(ctx, 5*time.Second)
	prep := prepareSpawnInline(ctx, &spawnChildWorkflowConfig{
		childWorkflowID: "child-wf-2", childThread: "agent-X", isResumption: true,
		promptStr: "continue", toolCallID: "tc-2", presetName: "researcher",
	}, "/project", "chat-1", "wf-root", "parent-thread", map[string]interface{}{}, makeCtrl)
	if prep.earlyResult != nil {
		return resumeProbe{}, fmt.Errorf("prepare failed early: %s", prep.earlyResult.Content)
	}

	var out resumeProbe
	out.CancelledAfterPrepare = prep.pauseCtrl.IsCancelled()
	out.OldToolCallStaysCancel = makeCtrl("tc-1").IsCancelled()

	// Stopping the live resumption must still work.
	_ = workflow.Sleep(ctx, 2*time.Minute)
	out.CancelledAfterNewStop = prep.pauseCtrl.IsCancelled()
	return out, nil
}

func TestSpawnResumeAfterStop(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	for name, fn := range map[string]interface{}{
		"ExecuteTools":             stubExecuteTools,
		"FetchThreadResult":        stubFetchThreadResult,
		"NotifyWorkflowStatus":     stubNotifyWorkflowStatus,
		"LoadPresetParams":         stubLoadPresetParams,
		"LoadWorkflow":             stubLoadWorkflow,
		"SaveMessage":              stubSaveMessage,
		"EmitThreadEvent":          stubEmitThreadEvent,
		"ValidateThreadOwnership":  stubValidateThreadOwnership,
		"CreateWorkflowWithThread": stubEmitThreadEvent,
	} {
		env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	}

	// First run stopped before the resumption is dispatched (same agent thread).
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CancelThreadSignalName, CancelThreadSignal{Thread: "agent-X", ToolCallID: "tc-1"})
	}, 2*time.Second)
	// A stop aimed at the live resumption's tool call, after it started.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(CancelThreadSignalName, CancelThreadSignal{Thread: "agent-X", ToolCallID: "tc-2"})
	}, 60*time.Second)

	env.ExecuteWorkflow(resumeAfterStopWorkflow)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var got resumeProbe
	require.NoError(t, env.GetWorkflowResult(&got))

	require.False(t, got.CancelledAfterPrepare, "a resumption must not inherit the stop aimed at the agent's earlier run")
	require.True(t, got.OldToolCallStaysCancel, "the stopped run's own tool call stays cancelled")
	require.True(t, got.CancelledAfterNewStop, "stopping the live resumption must still cancel it")
}
