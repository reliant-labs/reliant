// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// panicFailsRunChangeID gates turning a panic in workflow code into a failed
// run (recoveredPanic). The commands that follow a recovered panic — the
// failure bookkeeping, then the run's FailWorkflowExecution — are new, so a
// history with no marker at the panic re-panics as it always did.
//
// No history recorded before the change can actually take that branch: a
// panic used to unwind into the completion bookkeeping, whose first blocking
// call raised the SDK's "yield during panic unwinding" panic, so the workflow
// task failed and none of its commands were ever recorded. The panic point of
// a run wedged that way is in its live task, where GetVersion records the
// marker and the fixed worker fails the run cleanly. The frozen fixture set
// 2026-10-10-panic-wedges-workflow-task pins exactly that.
const panicFailsRunChangeID = "workflow-panic-fails-run"

// WorkflowPanicError is a panic in workflow code, recovered and turned into
// the error the run fails with. Its text is what the run records and the user
// is shown; the stack is logged, never shown.
type WorkflowPanicError struct {
	Value interface{}
	Stack string
}

func (e *WorkflowPanicError) Error() string {
	return fmt.Sprintf("panic: %v", e.Value)
}

// workflowPanicErrorType is the ApplicationError type a panicked run fails
// with, so Temporal's own record of the run names the cause.
const workflowPanicErrorType = "WorkflowPanic"

// workflowPanicSummary is the one line the chat shows for a run a panic ended.
const workflowPanicSummary = "This run stopped on an internal error in Reliant — not something you or the assistant did. Send a message to try again."

// capturedPanic is a panic stopped by capture, with the stack it was raised on.
//
// Recovering is split from handling because of a Temporal SDK rule: a
// deferred function must not block while a panic is unwinding (the SDK panics
// "yield during panic unwinding"), and that still holds after recover(),
// because the panicking frames stay on the stack until the recovering
// function returns. So capture only records — it is the deferred function,
// which is the one place recover() works — and the caller handles the panic
// (recoveredPanic, then any blocking bookkeeping) once the deferring function
// has returned normally.
type capturedPanic struct {
	value interface{}
	stack string
}

// capture records a panic in flight and stops it. Defer it directly —
// `defer caught.capture()` — and after every deferred function that may block,
// so it runs first.
func (c *capturedPanic) capture() {
	if r := recover(); r != nil {
		c.value = r
		c.stack = string(debug.Stack())
	}
}

// runRecovering runs body and returns what a panic in it becomes
// (recoveredPanic), or nil when it returned normally.
func runRecovering(ctx workflow.Context, body func()) error {
	var caught capturedPanic
	func() {
		defer caught.capture()
		body()
	}()
	if caught.value == nil {
		return nil
	}
	return recoveredPanic(ctx, caught)
}

// executeRecovering runs a node's body on the node's own coroutine, where
// DynamicWorkflow's recovery cannot reach: a panic in it becomes the node's
// error, so the graph fails the run through its ordinary error path instead
// of the panic taking down the whole workflow task.
func executeRecovering(ctx workflow.Context, execute func() (map[string]interface{}, error)) (output map[string]interface{}, err error) {
	if panicErr := runRecovering(ctx, func() { output, err = execute() }); panicErr != nil {
		return nil, panicErr
	}
	return output, err
}

// recoveredPanic decides what a captured panic in workflow code becomes. It
// re-panics, issuing no command, for:
//   - a panic the Temporal SDK raised itself (isTemporalSDKPanic). A replay
//     divergence (TMPRL1100) must fail the workflow TASK as non-deterministic,
//     not the workflow: the task retries until a worker whose code replays the
//     history picks it up, and the reconciler's wedge detector (#687) and the
//     resume path read that cause (WorkflowTaskOutcome.ReplayDiverged).
//     Converting it would end a run that a fixed worker could have healed.
//   - a history recorded before panicFailsRunChangeID.
//
// Anything else is a bug in our workflow code. Retrying the task cannot get
// past a deterministic bug, so the run fails with a *WorkflowPanicError and
// says so (handleWorkflowCompletion).
func recoveredPanic(ctx workflow.Context, caught capturedPanic) error {
	if isTemporalSDKPanic(caught.value) {
		panic(caught.value)
	}
	if workflow.GetVersion(ctx, panicFailsRunChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		panic(caught.value)
	}
	panicErr := &WorkflowPanicError{Value: caught.value, Stack: caught.stack}
	// The one report of the bug, with the stack that locates it. Callers log
	// what they did about it below ERROR.
	workflow.GetLogger(ctx).Error("[Workflow Runtime] Workflow code panicked",
		"workflowID", workflow.GetInfo(ctx).WorkflowExecution.ID,
		"panic", panicErr.Error(),
		"stack", panicErr.Stack,
	)
	return panicErr
}

// isTemporalSDKPanic reports whether a recovered value is a panic the Temporal
// SDK raised itself, rather than one from our code. The SDK raises its
// nondeterminism panics with an unexported type from its own package
// (internal.stateMachineIllegalStatePanic); the TMPRL1xxx code catches any it
// raises as plain text.
func isTemporalSDKPanic(recovered interface{}) bool {
	t := reflect.TypeOf(recovered)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t != nil && strings.HasPrefix(t.PkgPath(), "go.temporal.io/sdk") {
		return true
	}
	return strings.Contains(fmt.Sprint(recovered), "[TMPRL1")
}

// runFailureForPanic is the error a panicked run returns, so Temporal records
// the run FAILED (never retried: the same code panics the same way) under a
// type that names the cause.
func runFailureForPanic(err error) error {
	return temporal.NewNonRetryableApplicationError(err.Error(), workflowPanicErrorType, nil)
}

// notifyWorkflowPanic shows the user what ended the run: an error card naming
// the panic, with a summary that says it was ours and what to do next.
func notifyWorkflowPanic(ctx workflow.Context, chatID, workflowID, workflowName, thread string, panicErr *WorkflowPanicError) {
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    5 * time.Second,
			MaximumAttempts:    3,
		},
	})
	input := map[string]interface{}{
		"chat_id":       chatID,
		"workflow_id":   workflowID,
		"workflow_name": workflowName,
		"error_type":    "workflow_panic",
		"error_message": failureText(panicErr),
		"error_summary": workflowPanicSummary,
	}
	if thread != "" {
		input["thread"] = thread
	}
	if err := workflow.ExecuteActivity(activityCtx, "WorkflowError", input).Get(ctx, nil); err != nil {
		workflow.GetLogger(ctx).Warn("[Workflow Runtime] Failed to show the run's panic in the chat",
			"workflowID", workflowID, "error", err)
	}
}
