// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
)

// triggerCapacitySlots is the park/resume of a run's capacity slot over the
// store's trigger-capacity queries (trigger_capacity has no entity, so no
// generated ext store).
type triggerCapacitySlots struct{ db orm.Context }

// NewTriggerCapacitySlots is the TriggerCapacitySlotStore over db.
func NewTriggerCapacitySlots(db orm.Context) TriggerCapacitySlotStore {
	return triggerCapacitySlots{db: db}
}

func (t triggerCapacitySlots) ParkTriggerCapacity(ctx context.Context, slot store.TriggerCapacitySlot) (bool, error) {
	return store.ParkTriggerCapacity(ctx, t.db, slot)
}

func (t triggerCapacitySlots) ResumeTriggerCapacity(ctx context.Context, slot store.TriggerCapacitySlot) (bool, bool, error) {
	return store.ResumeTriggerCapacity(ctx, t.db, slot)
}
