// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// "Interrupt & send now" while a turn's tool batch is still in its
// activation-approval phase (prod chat 17037094, 2026-11-20).
//
// The batch held one update_trigger_activation call. Before ExecuteTools runs,
// the workflow previews the activation (AutomationApprovalPreview) and, when it
// needs one, raises the owner's approval card and waits on it. Neither phase
// writes a tool_calls row, so InterruptThread had nothing to cancel, and the
// interrupt's CanceledError reached the loop's cancel handler, which treated it
// as a pause and dispatched the SAME execute_tools step again: the preview ran
// again (and hung again), call_llm was never reached, and the queued messages
// were never delivered. Four interrupts, four re-dispatches.
//
// What an interrupt must do instead: end the step without starting new work.
// Every call of the turn gets a tool_result (the user-cancelled one for a call
// that never ran), save_message persists them, and the loop takes its next
// call_llm, which delivers the mailbox.

type interruptSettleEnv struct {
	mu sync.Mutex

	callLLMStarts []time.Time
	callLLMCount  int32

	previews       int32
	approvals      int32
	resolved       []string
	toolsDispatch  []types.RuntimeContext
	savedToolTurns int32

	// previewBlocks makes AutomationApprovalPreview hang until cancelled,
	// as it did in prod; otherwise it asks for a card.
	previewBlocks bool
}

const interruptSettleCallID = "tc-activation"

func newInterruptSettleEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, previewBlocks bool) *interruptSettleEnv {
	t.Helper()
	e := &interruptSettleEnv{previewBlocks: previewBlocks}
	registerRootDrainSupportActivities(t, env)
	env.RegisterActivityWithOptions(
		func(context.Context, types.ActivityInput) (interface{}, error) {
			return map[string]interface{}{"message_id": "msg-save"}, nil
		},
		activity.RegisterOptions{Name: "SaveMessage"},
	)

	registry := NewActivityRegistry(&wrapperTestRepo{})
	registry.SetMessageWriter(messageWriterFunc(func(_ context.Context, rtx types.RuntimeContext, args *reliantv1.SaveMessageNodeArgs, _ string, _ int32) (*reliantv1.SaveMessageOutput, error) {
		if strings.HasPrefix(rtx.StepID, "execute_tools") && args != nil {
			atomic.AddInt32(&e.savedToolTurns, 1)
		}
		return &reliantv1.SaveMessageOutput{MessageId: "msg-" + rtx.StepID, Thread: rtx.Thread}, nil
	}))
	registerWrapped(env, registry, "CallLLM",
		func(context.Context, types.ActivityInput) (map[string]interface{}, error) {
			idx := atomic.AddInt32(&e.callLLMCount, 1) - 1
			e.mu.Lock()
			e.callLLMStarts = append(e.callLLMStarts, time.Now())
			e.mu.Unlock()
			if idx == 0 {
				return map[string]interface{}{
					"response_text": "updating the trigger",
					"tool_calls": []interface{}{map[string]interface{}{
						"id": interruptSettleCallID, "name": names.ToolUpdateTriggerActivation, "input": `{"trigger_id":"trg-1"}`,
					}},
					"capabilities": map[string]interface{}{
						"offered":    []interface{}{names.ToolUpdateTriggerActivation},
						"permission": "auto",
					},
					"message": map[string]interface{}{"role": "assistant", "text": "updating the trigger"},
				}, nil
			}
			return map[string]interface{}{
				"response_text": "done",
				"tool_calls":    nil,
				"message":       map[string]interface{}{"role": "assistant", "text": "done"},
			}, nil
		})
	registerWrapped(env, registry, "ExecuteTools",
		func(_ context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			e.mu.Lock()
			e.toolsDispatch = append(e.toolsDispatch, input.Runtime)
			e.mu.Unlock()
			content := "updated"
			if input.Runtime.InterruptedTurn {
				content = "stopped by the user"
			}
			return map[string]interface{}{"tool_results": []interface{}{map[string]interface{}{
				"tool_call_id": interruptSettleCallID, "content": content, "is_error": input.Runtime.InterruptedTurn,
			}}}, nil
		})
	env.RegisterActivityWithOptions(
		func(ctx context.Context, _ types.AutomationApprovalInput) (types.AutomationApprovalPreview, error) {
			atomic.AddInt32(&e.previews, 1)
			if e.previewBlocks {
				select {
				case <-ctx.Done():
					return types.AutomationApprovalPreview{}, ctx.Err()
				case <-time.After(inflightWorkDuration):
				}
			}
			return types.AutomationApprovalPreview{Hash: "hash", Title: "Update the trigger"}, nil
		},
		activity.RegisterOptions{Name: "AutomationApprovalPreview"},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
			atomic.AddInt32(&e.approvals, 1)
			return map[string]interface{}{"approval_id": "approval-1"}, nil
		},
		activity.RegisterOptions{Name: "ApprovalCreate"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
			status, _ := in["status"].(string)
			e.mu.Lock()
			e.resolved = append(e.resolved, status)
			e.mu.Unlock()
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "ApprovalResolve"},
	)
	return e
}

func runInterruptSettle(t *testing.T, previewBlocks bool) *interruptSettleEnv {
	t.Helper()
	var s temporaltest.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(60 * time.Second)

	const chatID = "chat-interrupt-settle"
	const rootThread = "wf-" + chatID
	e := newInterruptSettleEnv(t, env, previewBlocks)

	var interruptedAt atomic.Value
	env.RegisterDelayedCallback(func() {
		interruptedAt.Store(time.Now())
		env.SignalWorkflow(InterruptThreadSignalName, InterruptThreadSignal{ThreadID: rootThread})
	}, 2*time.Second)

	env.ExecuteWorkflow(DynamicWorkflow, rootDrainWorkflowInput(chatID, rootThread))

	require.True(t, env.IsWorkflowCompleted(), "the run must finish once the interrupt ends the tool step")
	require.NoError(t, env.GetWorkflowError())

	e.mu.Lock()
	defer e.mu.Unlock()
	require.Len(t, e.callLLMStarts, 2,
		"after the interrupt the loop must take another call_llm: that call delivers the queued message")
	at, _ := interruptedAt.Load().(time.Time)
	require.False(t, at.IsZero(), "the interrupt signal must have been sent")
	require.True(t, e.callLLMStarts[1].After(at), "the second call_llm runs after the interrupt")
	require.Len(t, e.toolsDispatch, 1, "the interrupted batch is settled by exactly one ExecuteTools dispatch")
	require.True(t, e.toolsDispatch[0].InterruptedTurn,
		"that dispatch settles the batch (runs nothing) rather than executing it")
	require.Equal(t, int32(1), atomic.LoadInt32(&e.savedToolTurns),
		"the settled tool_results are saved, so the assistant's tool_calls never dangle")
	return e
}

func TestInterrupt_DuringActivationPreview_SettlesStepInsteadOfRedispatching(t *testing.T) {
	t.Parallel()
	e := runInterruptSettle(t, true)
	require.Equal(t, int32(1), atomic.LoadInt32(&e.previews),
		"the interrupt must not re-run the preview it cancelled")
	require.Zero(t, atomic.LoadInt32(&e.approvals), "no approval card is raised for an interrupted call")
}

func TestInterrupt_DuringActivationApprovalCard_ResolvesCardAndSettlesStep(t *testing.T) {
	t.Parallel()
	e := runInterruptSettle(t, false)
	require.Equal(t, int32(1), atomic.LoadInt32(&e.previews), "the preview runs once")
	require.Equal(t, int32(1), atomic.LoadInt32(&e.approvals),
		"the card raised before the interrupt is the only one: no second card is raised for the same call")
	require.Equal(t, []string{"interrupted"}, e.resolved, "the pending card is resolved as interrupted")
}
