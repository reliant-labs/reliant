// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"maps"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflowcheck"
)

// check: the run's side of a workflow's GitHub check (internal/workflowcheck).
//
// The check itself is the engine's: opened when the trigger's firing resolved
// the workflow, written to GitHub by the worker's syncer, which derives what
// it says from the firing and this run's workflow row. The run reports the
// two facts only it knows, each once:
//
//   - Started, right before its first node, so the check moves to
//     in_progress when work begins (not when a machine is still waking);
//   - Ended, as it ends, with its status, its verdict and the check output
//     it evaluated, so the check shows the run's own summary.
//
// Both are best-effort: a report that cannot be delivered is logged, and the
// syncer still concludes the check from the workflow row. Only the root run a
// trigger launched reports (a person's later turn in the same chat is not the
// trigger's run), and only when the workflow declares `check:`.

// workflowCheckChangeID gates the two report activities: they add commands
// to histories recorded before them.
const workflowCheckChangeID = "workflow-check"

// workflowCheckActivityOptions bound a report: a short attempt, retried for a
// few minutes. The syncer does not depend on it, so it never holds a run.
var workflowCheckActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout:    15 * time.Second,
	ScheduleToCloseTimeout: 3 * time.Minute,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    30 * time.Second,
	},
}

// reportsCheck reports whether this run reports to its workflow's check: a
// root run a trigger launched, of a workflow that declares one.
func reportsCheck(wf *reliantv1.Workflow, input WorkflowInput, parentWorkflowID string) bool {
	return wf.GetCheck() != nil && input.Trigger != nil && parentWorkflowID == "" && IsLaunchRun(input.Inputs) && input.ChatID != ""
}

// workflowCheckGate decides, once and only for a run that reports, whether
// this history reports. A disconnected context, so a cancelled run can still
// record the decision.
func workflowCheckGate(ctx workflow.Context) bool {
	dctx, _ := workflow.NewDisconnectedContext(ctx)
	return workflow.GetVersion(dctx, workflowCheckChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion
}

// reportCheckStarted tells the check that the run's first node is starting.
func reportCheckStarted(ctx workflow.Context, chatID string) {
	actx := workflow.WithActivityOptions(ctx, workflowCheckActivityOptions)
	if err := workflow.ExecuteActivity(actx, workflowcheck.StartedActivityName, workflowcheck.StartedInput{ChatID: chatID}).Get(actx, nil); err != nil {
		workflow.GetLogger(ctx).Warn("[Workflow Runtime] Could not report the check's start; the syncer reads it from the run", "chatID", chatID, "error", err)
	}
}

// reportCheckEnded tells the check how the run ended: status (a
// RunStatus* value), the graph's verdict, and the check's output evaluated
// against the run's final scope. On a disconnected context, so a cancelled
// run reports too.
func reportCheckEnded(ctx workflow.Context, check *reliantv1.WorkflowCheck, input WorkflowInput, workflowID string, execCtx *ExecutionContext, nodeOutputs map[string]interface{}, status, verdict string) {
	ending := core.WorkflowCheckEnding{Status: status, Verdict: verdict}
	ending.Output, ending.OutputError = evaluateCheckOutput(check.GetOutput(), input, workflowID, execCtx, nodeOutputs, status)
	dctx, _ := workflow.NewDisconnectedContext(ctx)
	actx := workflow.WithActivityOptions(dctx, workflowCheckActivityOptions)
	if err := workflow.ExecuteActivity(actx, workflowcheck.EndedActivityName, workflowcheck.EndedInput{ChatID: input.ChatID, Ending: ending}).Get(actx, nil); err != nil {
		workflow.GetLogger(ctx).Warn("[Workflow Runtime] Could not report the check's end; the syncer concludes it from the run", "chatID", input.ChatID, "error", err)
	}
}

// evaluateCheckOutput evaluates check.output in the scope a finally handler
// sees: the run's inputs and trigger, its nodes' outputs, the workflow
// namespace, and `run.status`. It must evaluate to an object; anything else,
// or an expression that fails, is reported as the output's error, and the
// check shows the engine's own summary.
func evaluateCheckOutput(expr string, input WorkflowInput, workflowID string, execCtx *ExecutionContext, nodeOutputs map[string]interface{}, status string) (map[string]any, string) {
	if expr == "" {
		return nil, ""
	}
	inputs := maps.Clone(input.Inputs)
	if inputs == nil {
		inputs = map[string]interface{}{}
	}
	inputs[RunInputKey] = runCELValue(status)
	if nodeOutputs == nil {
		nodeOutputs = map[string]interface{}{}
	}
	scope := &wfcel.NodeResolutionContext{
		Inputs:   normalizeNumericTypes(inputs),
		Nodes:    normalizeNumericTypes(nodeOutputs),
		Workflow: workflowContextToTyped(buildWorkflowContext(workflowID, input.WorkflowName, input.ChatID, input.Inputs, execCtx)),
	}
	value, err := wfcel.EvaluateTemplate(expr, scope)
	if err != nil {
		return nil, fmt.Sprintf("check.output did not evaluate: %v", err)
	}
	out, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Sprintf("check.output evaluated to %T, not an object with title and summary", value)
	}
	return out, ""
}
