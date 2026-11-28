// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Stores is the production store of both receivers and the delivery relay:
// the repository facade for the reads and the receipt transaction other areas
// share (RevokeIntegrationAccess,
// RecordTriggerDelivery, TriggerDeliveryEvent), joined with the generated
// stores that own the delivery inbox and the integration routes. The generated
// methods are forwarded one by one, not embedded.
//
// *db.Repo alone satisfies none of Store, RelayStore or ReceiptStore any more,
// so a receiver cannot be built over a repository without the delivery inbox:
// it does not compile.
type Stores struct {
	*db.Repo
	deliveries store.TriggerDeliveryExtStore
	triggers   store.TriggerExtStore
}

// NewStores joins the repository facade with the generated webhook stores.
func NewStores(repo *db.Repo) Stores {
	if repo == nil {
		panic("webhook: NewStores requires a repository")
	}
	orm := repo.ORM()
	return Stores{Repo: repo, deliveries: store.NewTriggerDeliveryExtStore(orm), triggers: store.NewTriggerExtStore(orm)}
}

func (s Stores) ClaimTriggerDeliveries(ctx context.Context, now, until time.Time, token string, limit int) ([]core.TriggerDelivery, error) {
	return s.deliveries.ClaimTriggerDeliveries(ctx, now, until, token, limit)
}

func (s Stores) FindTriggerDelivery(ctx context.Context, key core.TriggerDelivery) (*core.TriggerDelivery, error) {
	return s.deliveries.FindTriggerDelivery(ctx, key)
}

func (s Stores) RetryCallerNotifiedTriggerDelivery(ctx context.Context, d core.TriggerDelivery) (*core.TriggerDelivery, error) {
	return s.deliveries.RetryCallerNotifiedTriggerDelivery(ctx, d)
}

func (s Stores) SettleTriggerDelivery(ctx context.Context, id, token string, st core.TriggerDeliverySettlement) (bool, error) {
	return s.deliveries.SettleTriggerDelivery(ctx, id, token, st)
}

func (s Stores) RenewTriggerDelivery(ctx context.Context, id, token string, now, until time.Time) (bool, error) {
	return s.deliveries.RenewTriggerDelivery(ctx, id, token, now, until)
}

func (s Stores) PruneTriggerDeliveries(ctx context.Context, before time.Time) (int64, error) {
	return s.deliveries.PruneTriggerDeliveries(ctx, before)
}

func (s Stores) GetTrigger(ctx context.Context, id string) (*core.Trigger, error) {
	return s.triggers.GetTrigger(ctx, id)
}

func (s Stores) GetTriggerWebhookCredentials(ctx context.Context, id string) (*core.TriggerWebhookCredentials, error) {
	return s.triggers.GetTriggerWebhookCredentials(ctx, id)
}

func (s Stores) ListIntegrationTriggers(ctx context.Context, integration string) ([]*core.IntegrationTriggerRoute, error) {
	return s.triggers.ListIntegrationTriggers(ctx, integration)
}

func (s Stores) ListAccessRoutedTriggers(ctx context.Context, integration, account, resource string, freshAfter time.Time) ([]*core.Trigger, error) {
	return s.triggers.ListAccessRoutedTriggers(ctx, integration, account, resource, freshAfter)
}

var (
	_ Store        = Stores{}
	_ RelayStore   = Stores{}
	_ ReceiptStore = Stores{}
)
