// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// Interrupt with work in flight, end to end through DynamicWorkflow.
//
// The prod report (2026-10-30, chats f9f60912 and 2ee014da): "Interrupt & send
// now" returned 200 but the agent kept working, and the message landed only at
// the next natural turn boundary. These drive the real agent loop with the
// activity the user is waiting on still running and assert that the interrupt
// ends it and the loop takes its next call_llm — the call that delivers the
// mailbox — promptly, rather than after the work finishes on its own.

type inflightInterruptEnv struct {
	mu            sync.Mutex
	callLLMStarts []time.Time
	callLLMCount  int32

	// toolsCancelled records whether the long-running ExecuteTools saw its
	// context end (the interrupt), as opposed to running out its own timer.
	toolsCancelled atomic.Bool
	// llmCancelled is the same for the long-running CallLLM.
	llmCancelled atomic.Bool
	// toolsDispatches counts ExecuteTools dispatches.
	toolsDispatches int32
}

const inflightWorkDuration = 10 * time.Minute

func newInflightInterruptEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, blockIn string) *inflightInterruptEnv {
	t.Helper()
	e := &inflightInterruptEnv{}
	registerRootDrainSupportActivities(t, env)
	env.RegisterActivityWithOptions(
		func(context.Context, types.ActivityInput) (interface{}, error) {
			return map[string]interface{}{"message_id": "msg-save"}, nil
		},
		activity.RegisterOptions{Name: "SaveMessage"},
	)

	registry := NewActivityRegistry(&wrapperTestRepo{})
	registry.SetMessageWriter(&recordingMessageWriter{})
	registerWrapped(env, registry, "CallLLM",
		func(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			idx := int(atomic.AddInt32(&e.callLLMCount, 1)) - 1
			e.mu.Lock()
			e.callLLMStarts = append(e.callLLMStarts, time.Now())
			e.mu.Unlock()
			if idx == 0 && blockIn == "call_llm" {
				// A long stream: runs until something stops it.
				select {
				case <-ctx.Done():
					e.llmCancelled.Store(true)
					return nil, ctx.Err()
				case <-time.After(inflightWorkDuration):
				}
			}
			if idx == 0 {
				return map[string]interface{}{
					"response_text": "waiting",
					"tool_calls": []interface{}{map[string]interface{}{
						"id": "tc-wait", "name": "shell_wait", "input": `{"timeout":1200}`,
					}},
					"message": map[string]interface{}{"role": "assistant", "text": "waiting"},
				}, nil
			}
			return map[string]interface{}{
				"response_text": "done",
				"tool_calls":    nil,
				"message":       map[string]interface{}{"role": "assistant", "text": "done"},
			}, nil
		})
	registerWrapped(env, registry, "ExecuteTools",
		func(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			// Only the first dispatch blocks. InterruptThread records the
			// in-flight call as Cancelled before it signals, so the step the
			// loop re-dispatches after the cancel returns that recorded result
			// at once (checkPriorTerminalResult) instead of waiting again.
			if blockIn == "execute_tools" && atomic.AddInt32(&e.toolsDispatches, 1) == 1 {
				select {
				case <-ctx.Done():
					e.toolsCancelled.Store(true)
					return nil, ctx.Err()
				case <-time.After(inflightWorkDuration):
				}
			}
			return map[string]interface{}{"tool_results": []interface{}{map[string]interface{}{
				"tool_call_id": "tc-wait", "content": "stopped", "is_error": false,
			}}}, nil
		})
	return e
}

func (e *inflightInterruptEnv) starts() []time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]time.Time(nil), e.callLLMStarts...)
}

func runInflightInterrupt(t *testing.T, blockIn string) {
	var s temporaltest.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.SetTestTimeout(60 * time.Second)

	const chatID = "chat-interrupt-inflight"
	const rootThread = "wf-" + chatID
	e := newInflightInterruptEnv(t, env, blockIn)

	var interruptedAt atomic.Value
	env.RegisterDelayedCallback(func() {
		interruptedAt.Store(time.Now())
		env.SignalWorkflow(InterruptThreadSignalName, InterruptThreadSignal{ThreadID: rootThread})
	}, 2*time.Second)

	env.ExecuteWorkflow(DynamicWorkflow, rootDrainWorkflowInput(chatID, rootThread))

	require.True(t, env.IsWorkflowCompleted(), "the run must finish once the interrupt ends the work in flight")
	require.NoError(t, env.GetWorkflowError())

	starts := e.starts()
	require.GreaterOrEqual(t, len(starts), 2,
		"after the interrupt the loop must take another call_llm — that call delivers the queued message")
	at, _ := interruptedAt.Load().(time.Time)
	require.False(t, at.IsZero(), "the interrupt signal must have been sent")
	var next time.Time
	for _, s := range starts {
		if s.After(at) {
			next = s
			break
		}
	}
	require.False(t, next.IsZero(), "a call_llm must start after the interrupt")
	require.Less(t, next.Sub(at), 15*time.Second,
		"the call_llm that delivers the message must start within seconds of the interrupt, not when the work finishes")
}

func TestInterrupt_LongToolCallInFlight_NextCallLLMRunsPromptly(t *testing.T) {
	t.Parallel()
	runInflightInterrupt(t, "execute_tools")
}

func TestInterrupt_LongLLMStreamInFlight_NextCallLLMRunsPromptly(t *testing.T) {
	t.Parallel()
	runInflightInterrupt(t, "call_llm")
}
