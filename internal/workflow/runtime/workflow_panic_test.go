// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// A panic in workflow code used to wedge the run: DynamicWorkflow's deferred
// function called handleWorkflowCompletion, and THAT called recover() — one
// call too deep, so it always returned nil. The panic kept unwinding into the
// completion bookkeeping, whose first blocking call raised the SDK's "yield
// during panic unwinding" panic in place of the real one, and the workflow
// task failed and retried forever with the chat showing as active. These pin
// the fix: the run fails, says why, and Temporal's own panics still fail the
// task.

// injectedPanic is what panicOnActivity panics with: an ordinary bug in our
// workflow code, as far as the runtime can tell.
const injectedPanic = "injected: index out of range [3] with length 3"

// panicOnActivity is a workflow interceptor that runs panicWith in place of
// scheduling the named activity — inside the workflow coroutine that asked for
// it, exactly where a bug in our step code would panic.
type panicOnActivity struct {
	interceptor.WorkerInterceptorBase
	activityType string
	panicWith    func(ctx workflow.Context)
}

func (p *panicOnActivity) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &panicOnActivityInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, p: p}
}

type panicOnActivityInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	p *panicOnActivity
}

func (i *panicOnActivityInbound) Init(outbound interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&panicOnActivityOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: outbound}, p: i.p})
}

type panicOnActivityOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	p *panicOnActivity
}

func (o *panicOnActivityOutbound) ExecuteActivity(ctx workflow.Context, activityType string, args ...interface{}) workflow.Future {
	if activityType == o.p.activityType {
		o.p.panicWith(ctx)
	}
	return o.Next.ExecuteActivity(ctx, activityType, args...)
}

// panicRecorder records the user-facing error cards and cleanups a run emits.
type panicRecorder struct {
	*resumeEnvRecorder
	errorCards []map[string]interface{}
	cleanups   int
}

// setupPanicEnv is setupResumeEnv with CallLLM scheduling replaced by
// panicWith, and the WorkflowError and Cleanup activities recorded.
func setupPanicEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment, yamlStr string, panicWith func(workflow.Context)) *panicRecorder {
	t.Helper()
	env.SetWorkerOptions(temporaltest.WorkerOptions(worker.Options{
		Interceptors: []interceptor.WorkerInterceptor{&panicOnActivity{activityType: "CallLLM", panicWith: panicWith}},
	}))
	rec := &panicRecorder{resumeEnvRecorder: setupResumeEnv(t, env, yamlStr)}
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			rec.errorCards = append(rec.errorCards, input)
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "WorkflowError"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			rec.cleanups++
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "Cleanup"},
	)
	return rec
}

func panicsWithBug(workflow.Context) { panic(injectedPanic) }

// panicsNondeterministically raises the SDK's own TMPRL1100 panic from inside
// workflow code: a GetVersion whose range no longer admits the version this
// run recorded — what a worker hits when it runs code that cannot replay the
// history.
func panicsNondeterministically(ctx workflow.Context) {
	workflow.GetVersion(ctx, "test-replay-divergence", workflow.DefaultVersion, 1)
	workflow.GetVersion(ctx, "test-replay-divergence", 2, 2)
}

// statusNames returns the WorkflowStatus values the run reported, in order.
func statusNames(rec *resumeEnvRecorder) []string {
	var out []string
	for _, s := range rec.statuses {
		status, _ := s["status"].(string)
		out = append(out, status)
	}
	return out
}

func TestDynamicWorkflow_PanicFailsTheRunAndSaysWhy(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupPanicEnv(t, env, noOutcomeYAML, panicsWithBug)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-panic", nil))

	require.True(t, env.IsWorkflowCompleted())
	runErr := env.GetWorkflowError()
	require.Error(t, runErr, "a panicked run must end, failed")
	var sdkPanic *temporal.PanicError
	assert.False(t, errors.As(runErr, &sdkPanic),
		"the panic must fail the RUN, not escape as a task panic that Temporal retries forever: %v", runErr)
	var appErr *temporal.ApplicationError
	require.True(t, errors.As(runErr, &appErr), "want an application failure, got %T: %v", runErr, runErr)
	assert.Equal(t, workflowPanicErrorType, appErr.Type())
	assert.True(t, appErr.NonRetryable(), "the same code panics the same way; retrying the run cannot help")
	assert.Contains(t, appErr.Error(), injectedPanic, "Temporal's record of the run names the panic")

	final := terminalStatus(t, rec.resumeEnvRecorder)
	assert.Equal(t, "failed", final["status"], "the run is recorded failed, never completed; statuses: %v", statusNames(rec.resumeEnvRecorder))
	assert.Contains(t, final["error"], injectedPanic, "the recorded failure carries the panic message")
	assert.NotContains(t, statusNames(rec.resumeEnvRecorder), "completed")

	require.Len(t, rec.errorCards, 1, "the user is shown one error card for the panic")
	card := rec.errorCards[0]
	assert.Equal(t, "workflow_panic", card["error_type"])
	assert.Contains(t, card["error_message"], injectedPanic)
	assert.Equal(t, workflowPanicSummary, card["error_summary"])
	assert.Equal(t, "thread-resume", card["thread"], "the card belongs to the run's thread")
	assert.Equal(t, 1, rec.cleanups, "a failed run cancels what it left pending, like any other failure")
}

// A panic in a node that runs on its own coroutine (an inline sub-workflow,
// a router's dispatch) cannot reach DynamicWorkflow's deferred recover. It
// fails that node, and the node's failure fails the run.
func TestDynamicWorkflow_PanicInANodeCoroutineFailsTheRun(t *testing.T) {
	t.Parallel()
	const inlineYAML = `
name: resume-test
entry: [sub]
nodes:
  - id: sub
    type: workflow
    inline:
      name: inner
      entry: [work]
      nodes:
        - id: work
          type: call_llm
`
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupPanicEnv(t, env, inlineYAML, panicsWithBug)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-panic-node", nil))

	require.True(t, env.IsWorkflowCompleted())
	runErr := env.GetWorkflowError()
	require.Error(t, runErr)
	var sdkPanic *temporal.PanicError
	assert.False(t, errors.As(runErr, &sdkPanic), "a node's panic must not escape as a task panic: %v", runErr)
	assert.Contains(t, runErr.Error(), injectedPanic)

	final := terminalStatus(t, rec.resumeEnvRecorder)
	assert.Equal(t, "failed", final["status"], "statuses: %v", statusNames(rec.resumeEnvRecorder))
	assert.Contains(t, final["error"], injectedPanic)
	require.Len(t, rec.errorCards, 1, "one card for one failure: the node's own inline-workflow card is not added on top")
	assert.Equal(t, "workflow_panic", rec.errorCards[0]["error_type"])
}

// Temporal's own panics are not ours to convert. A replay divergence must keep
// failing the workflow TASK as non-deterministic: the task retries until a
// worker that replays the history picks it up, and the reconciler's wedge
// detector and the resume path read that cause. Converting it would end, as
// failed, a run that a fixed worker could have healed.
func TestDynamicWorkflow_NondeterminismPanicStillFailsTheTask(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupPanicEnv(t, env, noOutcomeYAML, panicsNondeterministically)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-tmprl1100", nil))

	runErr := env.GetWorkflowError()
	require.Error(t, runErr)
	var sdkPanic *temporal.PanicError
	require.True(t, errors.As(runErr, &sdkPanic),
		"a TMPRL1100 panic must leave the workflow code as the SDK's own panic, got %T: %v", runErr, runErr)
	assert.Contains(t, sdkPanic.Error(), "[TMPRL1100]",
		"the task fails with the SDK's nondeterminism panic itself, not one raised while unwinding it")

	assert.Empty(t, rec.errorCards, "no failure is reported for a run a fixed worker can still heal")
	assert.Zero(t, rec.cleanups, "nothing is cleaned up: the run is not over")
	for _, status := range statusNames(rec.resumeEnvRecorder) {
		assert.NotContains(t, []string{"failed", "completed", "cancelled"}, status,
			"no terminal status for a run that has not ended")
	}
}

// A history with no panicFailsRunChangeID marker at the panic keeps failing
// the task, as it was recorded to.
func TestDynamicWorkflow_PanicInAHistoryBeforeTheGateStillFailsTheTask(t *testing.T) {
	t.Parallel()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupPanicEnv(t, env, noOutcomeYAML, panicsWithBug)
	env.OnGetVersion(panicFailsRunChangeID, workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)

	env.ExecuteWorkflow(DynamicWorkflow, resumeWorkflowInput("chat-panic-pre-gate", nil))

	runErr := env.GetWorkflowError()
	var sdkPanic *temporal.PanicError
	require.True(t, errors.As(runErr, &sdkPanic), "got %T: %v", runErr, runErr)
	assert.Contains(t, sdkPanic.Error(), injectedPanic)
	assert.Empty(t, rec.errorCards)
	assert.NotContains(t, statusNames(rec.resumeEnvRecorder), "failed")
}

func TestIsTemporalSDKPanic(t *testing.T) {
	t.Parallel()
	assert.False(t, isTemporalSDKPanic(injectedPanic))
	assert.False(t, isTemporalSDKPanic(errors.New("yield during panic unwinding: a deferred function attempted to block")),
		"the SDK's complaint about OUR deferred code is our bug, not a replay divergence")
	assert.False(t, isTemporalSDKPanic(&WorkflowPanicError{Value: "x"}))
	assert.True(t, isTemporalSDKPanic("[TMPRL1100] lookup failed for scheduledEventID to activityID"))
	assert.True(t, isTemporalSDKPanic(&temporal.PanicError{}), "any value of an SDK type is the SDK's")
}
