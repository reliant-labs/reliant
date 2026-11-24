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
//
// inheritedMachine is whether any node routes to the machine the run
// inherits (the workflow-level one, else the chat's). When none does — every
// machine-bound node is placed on a pool — the tools, checkouts and CLI
// sign-ins are not
// checked here, on a machine no node uses: each pool member is checked when a
// step is first placed on it (placedRequirements). The integrations, which
// are the server's, are still checked here.
func checkRequirements(ctx workflow.Context, input WorkflowInput, execCtx *ExecutionContext, wf *reliantv1.Workflow, workflowID, thread string, inheritedMachine bool) error {
	req := wf.GetRequires()
	if requirements.Empty(req) || execCtx.Parent != nil {
		return nil
	}
	if !inheritedMachine {
		req = &reliantv1.WorkflowRequirements{Integrations: req.GetIntegrations()}
		if requirements.Empty(req) {
			return nil
		}
	}
	if workflow.GetVersion(ctx, preflightRequirementsChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return nil
	}
	in := requirementsActivityInput(input, workflowID, req)
	if inheritedMachine {
		if target := preflightTarget(input, execCtx); target["daemon_selector"] != nil {
			in["daemon_selector"] = target["daemon_selector"]
		}
	}
	err := workflow.ExecuteActivity(requirementsActivityCtx(ctx), PreflightRequirementsActivityName, in).Get(ctx, nil)
	if err == nil {
		return nil
	}
	if temporal.IsCanceledError(err) {
		return err
	}
	msg := requirementsFailureMessage(err)
	notifyWorkflowError(ctx, input.ChatID, workflowID, input.WorkflowName, thread, "requirements_unmet", msg)
	return fmt.Errorf("preflight requirements: %s", msg)
}

// requirementsActivityCtx is the activity options of a requirements check: a
// check that could not run (the machine dropped the probe, a connections
// lookup blipped) is retried on the infrastructure ladder; an unmet
// requirement is a verdict and is not.
func requirementsActivityCtx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: preflightRequirementsTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        5 * time.Second,
			MaximumAttempts:        infrastructureActivityMaxAttempts,
			NonRetryableErrorTypes: []string{preflightRequirementsUnmetErrType},
		},
	})
}

// requirementsActivityInput is PreflightRequirements' input for req, minus
// the machine it runs on.
func requirementsActivityInput(input WorkflowInput, workflowID string, req *reliantv1.WorkflowRequirements) map[string]interface{} {
	in := map[string]interface{}{
		"chat_id":         input.ChatID,
		"workflow_id":     workflowID,
		"tools":           req.GetTools(),
		"integrations":    req.GetIntegrations(),
		"repos":           req.GetRepos(),
		"cli_login":       req.GetCliLogin(),
		"acting_identity": ActingIdentity(input.Inputs),
	}
	if automation, ok := input.Inputs[InputKeyAutomation].(string); ok {
		in["automation"] = automation
	}
	return in
}

// isRequirementsVerdict reports whether err is an unmet-requirements verdict
// rather than a check that could not run.
func isRequirementsVerdict(err error) bool {
	var appErr *temporal.ApplicationError
	return errors.As(err, &appErr) && appErr.Type() == preflightRequirementsUnmetErrType
}

// requirementsFailureMessage is what a failed check tells the user.
func requirementsFailureMessage(err error) string {
	msg := preflightFailureMessage(err)
	if !isRequirementsVerdict(err) {
		msg = "could not check the run's requirements (requires:): " + msg
	}
	return msg
}

// ── Requirements on a placed pool member ────────────────────────────────

// placedRequirementsChangeID gates the check on a placed member. It is asked
// for only when a step placed on a pool is about to dispatch, and pools are
// newer than every history recorded without it.
const placedRequirementsChangeID = "placed-machine-requirements"

// placedRequirements checks the run's `requires:` tools, checkouts and CLI
// sign-ins on each pool member a step is placed on, once per member per run,
// before the first
// machine step there dispatches. The startup check (checkRequirements) covers
// the machine the run inherits; a placed member is a different machine, so
// what the run requires of a machine is checked again where it actually
// runs. One per root run, shared by every thread; deterministic workflow
// state, rebuilt on replay.
type placedRequirements struct {
	// base is the activity input minus the machine: tools, repos and CLI
	// sign-ins only (integrations are the server's, and checked once at
	// startup).
	base map[string]interface{}
	// byMachine is each member's check, by daemon id: done is set once it
	// is decided, so a parallel step placed there meanwhile waits for it.
	byMachine map[string]*placedRequirementsEntry
	// checkedID is the machine the startup check probed, "" when none.
	checkedID string
}

type placedRequirementsEntry struct {
	done workflow.Future
	err  error
}

// newPlacedRequirements returns the run's checker, or nil when there is
// nothing to check on a machine (no tools, repos or CLI sign-ins required) or the run is a
// child (its root checked). checkedID is a machine the startup check already
// probed ("" when none), which a placement onto it does not probe again.
func newPlacedRequirements(input WorkflowInput, execCtx *ExecutionContext, wf *reliantv1.Workflow, workflowID, checkedID string) *placedRequirements {
	req := wf.GetRequires()
	if !requirements.NeedsMachine(req) || execCtx.Parent != nil {
		return nil
	}
	return &placedRequirements{
		base:      requirementsActivityInput(input, workflowID, &reliantv1.WorkflowRequirements{Tools: req.GetTools(), Repos: req.GetRepos(), CliLogin: req.GetCliLogin()}),
		byMachine: map[string]*placedRequirementsEntry{},
		checkedID: checkedID,
	}
}

// check checks the requirements on the member sel was placed on, the first
// time a step routes there, and returns that verdict to every later step
// there. A selector that was not placed routes to the run's inherited
// machine, which the startup check covered. A check that could not run is not
// kept: the step it failed fails, and the next step placed there asks again.
func (p *placedRequirements) check(callerCtx workflow.Context, sel *DaemonSelectorValue) error {
	if p == nil || sel == nil || !sel.Placed || sel.ID == "" || sel.ID == p.checkedID {
		return nil
	}
	if entry, ok := p.byMachine[sel.ID]; ok {
		_ = entry.done.Get(callerCtx, nil)
		return entry.err
	}
	if workflow.GetVersion(callerCtx, placedRequirementsChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return nil
	}
	done, settle := workflow.NewFuture(callerCtx)
	entry := &placedRequirementsEntry{done: done}
	p.byMachine[sel.ID] = entry
	defer settle.SetValue(nil)

	in := make(map[string]interface{}, len(p.base)+1)
	for k, v := range p.base {
		in[k] = v
	}
	in["daemon_selector"] = map[string]interface{}{"id": sel.ID}
	err := workflow.ExecuteActivity(requirementsActivityCtx(callerCtx), PreflightRequirementsActivityName, in).Get(callerCtx, nil)
	if err == nil {
		return nil
	}
	if temporal.IsCanceledError(err) || !isRequirementsVerdict(err) {
		delete(p.byMachine, sel.ID)
	}
	if temporal.IsCanceledError(err) {
		entry.err = err
		return err
	}
	entry.err = fmt.Errorf("the machine this step was placed on does not meet the run's requirements (requires:): %s", requirementsFailureMessage(err))
	return entry.err
}
