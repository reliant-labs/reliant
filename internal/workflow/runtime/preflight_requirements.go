// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/requirements"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// PreflightRequirementsActivityName checks a workflow's declared `requires:`
// (handlers.PreflightRequirementsActivity).
const PreflightRequirementsActivityName = "PreflightRequirements"

// preflightRequirementsChangeID gates the requirements check: a run recorded
// before it existed schedules no such activity, and replays as recorded.
const preflightRequirementsChangeID = "preflight-requirements"

// preflightRequirementsUnmetErrType is the activity's verdict error type.
const preflightRequirementsUnmetErrType = requirements.UnmetErrType

// preflightRequirementsTimeout bounds one attempt: the machine probe
// (ls-remote of each repo) plus each connection lookup.
const preflightRequirementsTimeout = 5 * time.Minute

// checkRequirements runs the requirements preflight for a root run whose
// workflow declares `requires:`, after the machine preflight and before graph
// entry. An unmet requirement fails the run with every problem named — the
// run's recorded failure, and for an unattended run its trigger's failure in
// the owner's inbox — before any node has a side effect. A child workflow
// shares its root's machine and connections, which the root checked.
func checkRequirements(ctx workflow.Context, input WorkflowInput, execCtx *ExecutionContext, wf *reliantv1.Workflow, workflowID, thread string) error {
	req := wf.GetRequires()
	if requirements.Empty(req) || execCtx.Parent != nil {
		return nil
	}
	if workflow.GetVersion(ctx, preflightRequirementsChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return nil
	}
	// A check that could not run (the machine dropped the probe, a
	// connections lookup blipped) is retried on the infrastructure ladder; an
	// unmet requirement is a verdict and is not.
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: preflightRequirementsTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        5 * time.Second,
			MaximumAttempts:        infrastructureActivityMaxAttempts,
			NonRetryableErrorTypes: []string{preflightRequirementsUnmetErrType},
		},
	})
	in := map[string]interface{}{
		"chat_id":         input.ChatID,
		"workflow_id":     workflowID,
		"tools":           req.GetTools(),
		"integrations":    req.GetIntegrations(),
		"repos":           req.GetRepos(),
		"acting_identity": ActingIdentity(input.Inputs),
	}
	if automation, ok := input.Inputs[InputKeyAutomation].(string); ok {
		in["automation"] = automation
	}
	if target := preflightTarget(input, execCtx); target["daemon_selector"] != nil {
		in["daemon_selector"] = target["daemon_selector"]
	}
	err := workflow.ExecuteActivity(actCtx, PreflightRequirementsActivityName, in).Get(ctx, nil)
	if err == nil {
		return nil
	}
	if temporal.IsCanceledError(err) {
		return err
	}
	msg := preflightFailureMessage(err)
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) || appErr.Type() != preflightRequirementsUnmetErrType {
		msg = "could not check the run's requirements (requires:): " + msg
	}
	notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName, thread, "requirements_unmet", msg)
	return fmt.Errorf("preflight requirements: %s", msg)
}
