package db

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// RecordTriggerDelivery commits a verified receipt before any fan-out. A
// revocation and its receipt are atomic: a worker never sees the delivery
// before the access removal, and a duplicate cannot reapply the revocation.
func (r *Repo) RecordTriggerDelivery(ctx context.Context, d core.TriggerDelivery, revocations []core.IntegrationAccessRevocation) (bool, error) {
	if len(revocations) == 0 {
		return store.InsertTriggerDelivery(ctx, r.ORM(), d)
	}
	var created bool
	err := r.RunTx(ctx, orm.ReadCommitted, func(tx context.Context) error {
		var err error
		created, err = store.InsertTriggerDelivery(tx, r.ORM(), d)
		if err != nil || !created {
			return err
		}
		for _, rev := range revocations {
			if rev.AccountKey == "" && rev.SubjectID == "" {
				continue
			}
			if _, err := store.RevokeIntegrationAccess(tx, r.ORM(), d.Provider, rev); err != nil {
				return err
			}
		}
		return nil
	})
	return created && err == nil, err
}

func (r *Repo) TriggerDeliveryEvent(ctx context.Context, id, triggerID, userID string) (*core.TriggerEvent, error) {
	return store.LoadDeliveryTriggerEvent(ctx, r.ORM(), id, triggerID, userID)
}

// RecordDeliveryTriggerEvent commits the event and its receipt link together.
func (r *Repo) RecordDeliveryTriggerEvent(ctx context.Context, id, token string, event *core.TriggerEvent) (stored *core.TriggerEvent, created bool, err error) {
	err = r.RunTx(ctx, orm.Serializable, func(tx context.Context) error {
		created = false
		linked, err := store.LockTriggerDeliveryEvent(tx, r.ORM(), id, token)
		if err != nil {
			return err
		}
		if linked != "" {
			stored, err = r.TriggerDeliveryEvent(tx, linked, *event.TriggerID, event.UserID)
			return err
		}
		if created, err = store.InsertTriggerEventIfAbsent(tx, r.ORM(), event); err != nil {
			return err
		}
		stored = event
		if !created {
			stored, err = store.GetTriggerEventByDedupe(tx, r.ORM(), event.Kind, event.DedupeKey)
			if err != nil {
				return err
			}
		}
		return store.LinkTriggerDeliveryEvent(tx, r.ORM(), id, token, stored.ID)
	})
	return stored, created, err
}
