// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"

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
func openCheck(ctx context.Context, checks CheckOpener, workflows WorkflowResolver, repo any, trigger *core.Trigger, decl *Declaration, ev *core.TriggerEvent) error {
	if checks == nil || workflows == nil {
		return nil
	}
	wf, err := resolveTriggerWorkflow(ctx, workflows, trigger)
	if err != nil {
		return err
	}
	if wf.GetCheck() == nil {
		return nil
	}
	scope := ActivationScope(ctx, repo, trigger, decl, FilterInput{
		Kind: string(ev.Kind), TriggerID: trigger.ID, EventID: ev.ID, OccurredAt: ev.OccurredAt,
		Payload: ev.Payload, Sender: ev.Sender, Origin: ev.Origin,
	}.Root())
	return checks.Open(ctx, workflowcheck.Firing{Workflow: wf, Trigger: trigger, Event: ev, Scope: scope})
}
