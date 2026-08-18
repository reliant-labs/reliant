// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// Temporal history economics: a REGULAR activity costs exactly six history
// events (ActivityTaskScheduled/Started/Completed, each preceded by a
// WorkflowTask triple), while a LOCAL activity is batched into the enclosing
// workflow task and recorded as a single MARKER_RECORDED event.
//
// A chat was terminated at Temporal's hard cap of 51,200 history events. In
// that run DrainAgentMessages cost 8,100 events and EmitStreamFinalized 8,094
// — a third of the whole history, spent on two calls per agent turn that are
// both best-effort and idempotent. Dispatching them locally is what makes a
// long conversation affordable.
//
// These tests pin the DISPATCH MODE, which is the thing that carries the
// saving. A revert to ExecuteActivity would still pass every behavioral test
// in this package (the activity runs either way, with the same arguments and
// the same result) while silently restoring ~12 events per turn. The test
// environment distinguishes the two, so it can catch that.

// dispatchCounts counts how a named activity was dispatched, split by mode.
//
// This split is the whole point of these tests, and it needs the right
// instrument. Both dispatch modes invoke the same registered function with the
// same arguments, and OnActivity interception catches BOTH — so neither
// call-counting nor mocking can tell them apart, and a revert to
// ExecuteActivity would keep passing every other test in this package. The
// SDK's two separate started-listeners are the discriminator: the regular
// listener fires only for server-scheduled activities (6 history events) and
// the local one only for local activities (1 marker event).
//
// Must be installed BEFORE ExecuteWorkflow.
type dispatchCounts struct {
	name    string
	regular int
	local   int
}

func watchDispatch(env *testsuite.TestWorkflowEnvironment, name string) *dispatchCounts {
	d := &dispatchCounts{name: name}
	env.SetOnActivityStartedListener(
		func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
			if info.ActivityType.Name == name {
				d.regular++
			}
		})
	env.SetOnLocalActivityStartedListener(
		func(info *activity.Info, _ context.Context, _ []interface{}) {
			if info.ActivityType.Name == name {
				d.local++
			}
		})
	return d
}

// TestEmitStreamFinalized_DispatchedLocally pins the SUCCESS path — the hot
// one, fired once per agent turn — as a local activity.
func TestEmitStreamFinalized_DispatchedLocally(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	var got types.EmitStreamFinalizedInput
	var calls int
	env.RegisterActivityWithOptions(
		func(_ context.Context, in types.EmitStreamFinalizedInput) (types.EmitStreamFinalizedOutput, error) {
			calls++
			got = in
			return types.EmitStreamFinalizedOutput{Success: true}, nil
		},
		activity.RegisterOptions{Name: "EmitStreamFinalized"},
	)
	dispatch := watchDispatch(env, "EmitStreamFinalized")

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = WithStreamIDTracker(ctx, NewStreamIDTracker())
		emitStreamFinalized(ctx, "chat-1", "msg-1", "thread-1", streamReasonCompleted, 42)
		return nil
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 1, calls)
	require.Equal(t, "msg-1", got.MessageID)
	require.Equal(t, streamReasonCompleted, got.Reason)
	require.Equal(t, int64(42), got.LastStreamSeq,
		"last_stream_seq must survive the switch to a typed local-activity input")

	require.Zero(t, dispatch.regular,
		"the success-path finalize was dispatched as a REGULAR activity (6 history "+
			"events); it must use workflow.ExecuteLocalActivity (1 marker event). It "+
			"fires once per agent turn and cost 8,094 events in the history-cap incident")
	require.Equal(t, 1, dispatch.local, "expected exactly one local dispatch")
}

// TestEmitStreamFinalized_TerminalPathStaysRegular pins the deliberate
// exception. finalizeOutstandingStreams runs from handleWorkflowCompletion on
// the cancel / error / panic paths, where the workflow is closing and may get
// no further workflow task — a local activity, which executes as part of a
// workflow task, can be lost there and leave a phantom streaming placeholder
// in the user's chat forever. A server-scheduled activity on a disconnected
// context survives that. It is also rare (measured: one such call in a
// 51,199-event history), so it costs nothing to leave durable.
func TestEmitStreamFinalized_TerminalPathStaysRegular(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	var calls int
	env.RegisterActivityWithOptions(
		func(_ context.Context, in types.EmitStreamFinalizedInput) (types.EmitStreamFinalizedOutput, error) {
			calls++
			require.Equal(t, streamReasonAborted, in.Reason)
			return types.EmitStreamFinalizedOutput{Success: true}, nil
		},
		activity.RegisterOptions{Name: "EmitStreamFinalized"},
	)
	dispatch := watchDispatch(env, "EmitStreamFinalized")

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = WithStreamIDTracker(ctx, NewStreamIDTracker())
		tracker := streamIDTrackerFrom(ctx)
		tracker.Register("msg-outstanding", "chat-1", "thread-1")

		// Cancellation is what routes emitStreamFinalized to the durable
		// path, so drive it the way handleWorkflowCompletion does.
		cancelCtx, cancel := workflow.WithCancel(ctx)
		cancel()
		_ = workflow.Sleep(ctx, time.Millisecond)
		finalizeOutstandingStreams(cancelCtx, streamReasonAborted)
		return nil
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 1, calls, "the outstanding id must be finalized exactly once")

	// Inverted sense from the tests above: here reaching the REGULAR path is
	// the requirement, so a scheduled event is what we demand.
	require.Equal(t, 1, dispatch.regular,
		"the terminal finalize must stay a durable server-scheduled activity: a local "+
			"activity executes as part of a workflow task, and a closing execution may "+
			"never get another one — losing it strands a phantom streaming placeholder "+
			"in the user's chat")
}
