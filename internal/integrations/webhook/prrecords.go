package webhook

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/prrecords"
)

// PRRecordConsumer feeds the PR-record projector from the inbox relay
// (dev-docs/pr-records.md §2.1): each stored integration delivery's events,
// before routing. The projector is idempotent per (provider, delivery key,
// event index), so a retried delivery applies nothing twice. Phase 1 is a
// shadow: nothing decides anything from the records.
func PRRecordConsumer(p prrecords.Service) DeliveryConsumer { return prRecordConsumer{p} }

type prRecordConsumer struct{ projector prrecords.Service }

func (c prRecordConsumer) ConsumeDelivery(ctx context.Context, provider, deliveryKey string, receivedAt time.Time, events []Event) error {
	d := prrecords.Delivery{Provider: provider, Key: deliveryKey, ReceivedAt: receivedAt, Events: make([]prrecords.Event, len(events))}
	for i, ev := range events {
		d.Events[i] = prrecords.Event{Type: ev.Type, AccountKey: ev.AccountKey, ResourceKey: ev.ResourceKey,
			Attributes: ev.Attributes, Data: ev.Data}
	}
	return c.projector.Apply(ctx, d)
}
