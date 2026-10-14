// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/lineage"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// applyOriginGuards decides an event by who caused it, before the trigger's own
// filter. Two checks, in this order, and the order is the point:
//
//  1. The lineage cap. HARD: it needs only the depth of the chain that led to the
//     run that wrote, and no expression, declaration or setting can reach it. A
//     pair of workflows that deliberately re-trigger each other through
//     origin_filter: "true" still stops here.
//  2. origin_filter, the author's own decision about whose echoes they want (the
//     default ignores a workflow's own).
//
// A skip names its reason, with the run when it is the owner's own, so the
// Automations history explains every one.
func (in *Intake) applyOriginGuards(ctx context.Context, trigger *core.Trigger, decl *Declaration, ev InboundEvent, origin *core.TriggerOrigin, occurredAt time.Time) (core.TriggerEventOutcome, string) {
	if origin != nil && origin.CausedByReliant {
		chain := lineage.FromCore(origin.Lineage)
		for len(chain) < origin.LineageDepth() {
			chain = append(lineage.Lineage{{TriggerID: "?"}}, chain...)
		}
		if reason, capped := chain.AtCap(); capped {
			return core.TriggerEventSkipped, reason
		}
	}

	expr := ""
	if decl != nil {
		expr = decl.OriginFilter
	}
	filter, err := triggerspec.CompileOriginFilter(expr)
	if err != nil {
		return core.TriggerEventFailed, "the trigger's origin_filter does not compile: " + err.Error()
	}
	root := FilterInput{
		Kind: string(ev.Kind), TriggerID: trigger.ID, OccurredAt: occurredAt, Payload: ev.Payload, Sender: ev.Sender, Origin: origin,
	}.Root()
	allowed, err := filter.Allow(ActivationScope(ctx, in.repo, trigger, decl, root))
	if err != nil {
		return core.TriggerEventFailed, fmt.Sprintf(
			"the trigger's origin_filter could not evaluate this event (%v); trigger.origin always has every key, so check the rest of the expression", err)
	}
	if !allowed {
		return core.TriggerEventSkipped, originSkipReason(origin, filter.Expr())
	}
	return core.TriggerEventPending, ""
}

// originSkipReason is what the firing history says about an event the
// origin_filter rejected: who caused it, as much as this owner may know.
func originSkipReason(origin *core.TriggerOrigin, expr string) string {
	if origin == nil || !origin.CausedByReliant {
		return "origin_filter rejected: " + expr
	}
	switch {
	case origin.Foreign:
		return fmt.Sprintf("origin_filter rejected: caused by a run of Reliant (%s match) that is not yours: %s", origin.Confidence, expr)
	case origin.RunID != "" && origin.SameWorkflow:
		return fmt.Sprintf("origin_filter rejected: caused by run %s of this workflow (%s match): %s", origin.RunID, origin.Confidence, expr)
	case origin.RunID != "":
		return fmt.Sprintf("origin_filter rejected: caused by run %s of workflow %s (%s match): %s", origin.RunID, orUnknown(origin.Workflow), origin.Confidence, expr)
	default:
		return fmt.Sprintf("origin_filter rejected: caused by Reliant's app, run unknown (%s match): %s", origin.Confidence, expr)
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
