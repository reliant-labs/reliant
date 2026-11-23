// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/prrecords"
)

// prRecordConsumer feeds the PR-record projector from the trigger inbox
// (dev-docs/pr-records.md §2.1): every stored integration delivery's events,
// before routing. The projector is idempotent per (provider, delivery key,
// event index), so a retried delivery applies nothing twice. Phase 1 is a
// shadow: nothing decides anything from the records.
type prRecordConsumer struct{ projector prrecords.Service }

func (c prRecordConsumer) ConsumeDelivery(ctx context.Context, provider, deliveryKey string, receivedAt time.Time, events []webhook.Event) error {
	d := prrecords.Delivery{Provider: provider, Key: deliveryKey, ReceivedAt: receivedAt, Events: make([]prrecords.Event, len(events))}
	for i, ev := range events {
		d.Events[i] = prrecords.Event{Type: ev.Type, AccountKey: ev.AccountKey, ResourceKey: ev.ResourceKey,
			Attributes: ev.Attributes, Data: ev.Data}
	}
	return c.projector.Apply(ctx, d)
}
