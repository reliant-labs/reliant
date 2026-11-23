// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflowcheck"
)

// CheckOpener opens the GitHub check of one firing, when its workflow
// declares a `check:` block. workflowcheck.Service is one.
type CheckOpener interface {
	Open(ctx context.Context, f workflowcheck.Firing) error
}

// openCheck opens ev's check: the workflow the trigger runs is read from the
// same pinned definition as the run's, and check.repository and
// check.head_sha are rendered against the `trigger` root the run will see.
// A workflow without `check:` (or a firer without checks) opens nothing. A
// failure to read the workflow or record the check is returned, so the fire
// retries; a check that cannot be addressed is the opener's to log.
func openCheck(ctx context.Context, checks CheckOpener, workflows WorkflowResolver, repo OwnerSource, trigger *core.Trigger, decl *Declaration, ev *core.TriggerEvent) error {
	if checks == nil || workflows == nil {
		return nil
	}
	wf, err := resolveTriggerWorkflow(ctx, workflows, trigger)
	if err != nil {
		return err
	}
	return openWorkflowCheck(ctx, checks, wf, repo, trigger, decl, ev)
}

// openRefusedCheck opens ev's check before the fire records that ev can never
// launch (EventFirer.refuse), so a firing refused before launch still
// concludes its check: the syncer reads the failed outcome and writes failure
// ("Reliant could not run this check"). Without it, a required check of a
// firing that never launched would stay missing on GitHub for good.
//
// It is openCheck, except that a workflow that does not resolve opens nothing
// instead of retrying: the refusal stands either way, and a workflow that
// cannot be read (a deleted machine takes its catalog with it) declares no
// check to open. A failure to READ it, or to record the check, is still
// returned, so the fire retries before it settles anything.
func openRefusedCheck(ctx context.Context, checks CheckOpener, workflows WorkflowResolver, repo OwnerSource, trigger *core.Trigger, decl *Declaration, ev *core.TriggerEvent) error {
	if checks == nil || workflows == nil {
		return nil
	}
	wf, err := resolveTriggerWorkflow(ctx, workflows, trigger)
	if err != nil {
		if retryableRead(err) {
			return err
		}
		return nil
	}
	return openWorkflowCheck(ctx, checks, wf, repo, trigger, decl, ev)
}

// openWorkflowCheck opens ev's check of wf, when wf declares one.
func openWorkflowCheck(ctx context.Context, checks CheckOpener, wf *reliantv1.Workflow, repo OwnerSource, trigger *core.Trigger, decl *Declaration, ev *core.TriggerEvent) error {
	if wf.GetCheck() == nil {
		return nil
	}
	scope := ActivationScope(ctx, repo, trigger, decl, FilterInput{
		Kind: string(ev.Kind), TriggerID: trigger.ID, EventID: ev.ID, OccurredAt: ev.OccurredAt,
		Payload: ev.Payload, Sender: ev.Sender, Origin: ev.Origin,
	}.Root())
	return checks.Open(ctx, workflowcheck.Firing{Workflow: wf, Trigger: trigger, Event: ev, Scope: scope})
}
