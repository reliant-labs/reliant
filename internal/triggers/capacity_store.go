// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
)

// CapacityStores is the generated capacity stores joined into CapacityStore.
// Their method sets do not overlap.
type CapacityStores struct {
	store.TriggerCapacityExtStore
	store.TriggerCapacityBindingExtStore
	store.TriggerCapacityRecoveryExtStore
	events store.TriggerEventExtStore
}

// NewCapacityStores builds the production CapacityStore over a database handle.
func NewCapacityStores(db orm.Context) CapacityStores {
	return CapacityStores{
		TriggerCapacityExtStore:         store.NewTriggerCapacityExtStore(db),
		TriggerCapacityBindingExtStore:  store.NewTriggerCapacityBindingExtStore(db),
		TriggerCapacityRecoveryExtStore: store.NewTriggerCapacityRecoveryExtStore(db),
		events:                          store.NewTriggerEventExtStore(db),
	}
}

func (c CapacityStores) ExpirePendingTriggerCapacityEvents(ctx context.Context, recordedBefore time.Time, detail string, limit int) (int64, error) {
	return c.events.ExpirePendingTriggerCapacityEvents(ctx, recordedBefore, detail, limit)
}

// requireCapacity is the construction-time guard: a firer or redriver built
// over a nil store fails here, not on the first capped fire.
func requireCapacity(who string, c CapacityStore) {
	if c == nil {
		panic("triggers: " + who + " requires a CapacityStore")
	}
}
