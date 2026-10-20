// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// declaredDedupeKey is the dedupe key an arriving event is recorded under.
//
// A webhook trigger whose declaration sets dedupe_key names the real-world
// event itself: the key renders over the same `trigger` root a filter sees and
// REPLACES the receiver's default (the sender's idempotency header, else the
// body hash in a one-minute bucket). The rendered text is hashed into a fixed
// clock bucket of dedupe_window, so deliveries of one event within the window
// share a key — the (kind, dedupe_key) constraint then records the repeat as a
// duplicate and launches nothing — and the same event after the window is new.
//
// Anything else keeps the receiver's key: another kind, an ad hoc trigger, a
// declaration without dedupe_key, and a delivery whose key cannot be rendered
// (a missing field, an empty result). That last case degrades to the default
// rather than dropping or merging events, and is logged so the author hears
// about a key that does not fit the payload.
func (in *Intake) declaredDedupeKey(ctx context.Context, trigger *core.Trigger, decl *Declaration, ev InboundEvent, occurredAt time.Time) string {
	if decl == nil || decl.DedupeKey == "" || ev.Kind != core.TriggerEventKindWebhook {
		return ev.DedupeKey
	}
	window, err := triggerspec.ParseDedupeWindow(decl.DedupeWindow)
	if err != nil {
		logging.WarnContext(ctx, "trigger dedupe_window unusable; using the receiver's dedupe key", "trigger_id", trigger.ID, "error", err)
		return ev.DedupeKey
	}
	root := FilterInput{Kind: string(ev.Kind), TriggerID: trigger.ID, OccurredAt: occurredAt, Payload: ev.Payload, Sender: ev.Sender}.Root()
	rendered, err := triggerspec.RenderDedupeKey(decl.DedupeKey, ActivationScope(ctx, in.repo, trigger, decl, root))
	if err != nil {
		logging.WarnContext(ctx, "trigger dedupe_key did not render for a delivery; using the receiver's dedupe key",
			"trigger_id", trigger.ID, "error", err)
		return ev.DedupeKey
	}
	return trigger.ID + ":" + triggerspec.DedupeToken(rendered, window, in.now())
}
