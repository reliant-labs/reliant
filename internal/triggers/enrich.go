// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Enrichment is what an Enricher learned about an arriving event. A nil field
// is "nothing to add".
type Enrichment struct {
	// Origin is trigger.origin: whether Reliant caused the event.
	Origin *core.TriggerOrigin
	// Sender replaces the receiver's trigger.sender, for an enricher that knows
	// more about who sent it than the source attested (a trusted identity).
	Sender *core.TriggerSender
}

// Enricher adds what only this deployment knows about an event the source
// delivered: who really caused it, who the sender is to this owner. It is
// declared here, at the consumer; Intake knows no concrete enricher.
//
// Enrichers run before the trigger's filters, so the filters see their output,
// and what they return is persisted on the event row, so the firing history can
// explain a decision after the evidence (a ledger entry) is gone.
//
// An enricher must not fail an event: one that cannot decide returns an error,
// Intake logs it and records the event unenriched.
type Enricher interface {
	Enrich(ctx context.Context, trigger *core.Trigger, ev InboundEvent) (Enrichment, error)
}

// WithEnrichers sets the enrichers Accept runs, in order; a later enricher's
// non-nil field replaces an earlier one's.
func (in *Intake) WithEnrichers(enrichers ...Enricher) *Intake {
	in.enrichers = append(in.enrichers[:0:0], enrichers...)
	return in
}

// enrich runs every enricher over ev and applies the result: the enriched
// sender replaces ev's, and the origin is returned for the row.
func (in *Intake) enrich(ctx context.Context, trigger *core.Trigger, ev *InboundEvent) *core.TriggerOrigin {
	var origin *core.TriggerOrigin
	for _, e := range in.enrichers {
		out, err := e.Enrich(ctx, trigger, *ev)
		if err != nil {
			slog.Warn("trigger event enrichment failed; recording the event unenriched",
				"trigger_id", trigger.ID, "kind", ev.Kind, "error", err)
			continue
		}
		if out.Sender != nil {
			ev.Sender = out.Sender
		}
		if out.Origin != nil {
			origin = out.Origin
		}
	}
	return origin
}
