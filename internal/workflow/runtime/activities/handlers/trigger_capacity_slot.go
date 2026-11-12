// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/store"
)

// TriggerCapacitySlotInput is the input for the TriggerCapacitySlot activity.
type TriggerCapacitySlotInput struct {
	// Action is "park" (the run waits on a provider and gives its slot back)
	// or "resume" (it takes the slot back before its next step).
	Action string `json:"action"`
	// EventID is the trigger event the run was admitted for.
	EventID string `json:"event_id" reliant:"-"`
	// WorkflowID and FirstRunID name the admitted chain; a later chain on the
	// same chat holds no slot.
	WorkflowID string `json:"workflow_id" reliant:"-"`
	FirstRunID string `json:"first_run_id" reliant:"-"`
}

// TriggerCapacitySlotOutput is the output from the TriggerCapacitySlot activity.
type TriggerCapacitySlotOutput struct {
	// Held is false when the run holds no workflow-capacity slot: it was not
	// capacity-admitted, or the slot was released. The run stops asking.
	Held bool `json:"held"`
	// Granted, for resume: the run holds its slot again and may run its next
	// step. False means the scope is full; the run is now ahead of every
	// queued firing and asks again later.
	Granted bool `json:"granted"`
}

// TriggerCapacitySlotActivity parks and resumes the workflow-capacity slot of
// a trigger-launched run that waits on a provider, so the wait does not count
// against concurrency.max_running (store.ParkTriggerCapacity).
type TriggerCapacitySlotActivity struct {
	repo TriggerCapacitySlotStore
}

// NewTriggerCapacitySlotActivity creates a new TriggerCapacitySlotActivity.
func NewTriggerCapacitySlotActivity(repo TriggerCapacitySlotStore) *TriggerCapacitySlotActivity {
	return &TriggerCapacitySlotActivity{repo: repo}
}

// Name returns the activity name for registration.
func (a *TriggerCapacitySlotActivity) Name() string {
	return "TriggerCapacitySlot"
}

// Execute parks or resumes the slot.
func (a *TriggerCapacitySlotActivity) Execute(ctx context.Context, input TriggerCapacitySlotInput) (TriggerCapacitySlotOutput, error) {
	slot := store.TriggerCapacitySlot{EventID: input.EventID, WorkflowID: input.WorkflowID, FirstRunID: input.FirstRunID}
	switch input.Action {
	case "park":
		held, err := a.repo.ParkTriggerCapacity(ctx, slot)
		if err != nil {
			return TriggerCapacitySlotOutput{}, err
		}
		return TriggerCapacitySlotOutput{Held: held}, nil
	case "resume":
		held, granted, err := a.repo.ResumeTriggerCapacity(ctx, slot)
		if err != nil {
			return TriggerCapacitySlotOutput{}, err
		}
		return TriggerCapacitySlotOutput{Held: held, Granted: granted}, nil
	default:
		return TriggerCapacitySlotOutput{}, fmt.Errorf("unknown trigger capacity slot action %q", input.Action)
	}
}
