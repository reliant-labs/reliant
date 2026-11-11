// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/store"
)

func TestTriggerCapacitySlotActivity(t *testing.T) {
	var parked, resumed store.TriggerCapacitySlot
	repo := &MockTriggerCapacitySlotStore{
		ParkTriggerCapacityFunc: func(_ context.Context, slot store.TriggerCapacitySlot) (bool, error) {
			parked = slot
			return true, nil
		},
		ResumeTriggerCapacityFunc: func(_ context.Context, slot store.TriggerCapacitySlot) (bool, bool, error) {
			resumed = slot
			return true, false, nil
		},
	}
	act := NewTriggerCapacitySlotActivity(repo)
	in := TriggerCapacitySlotInput{EventID: "evt", WorkflowID: "wf", FirstRunID: "run"}
	want := store.TriggerCapacitySlot{EventID: "evt", WorkflowID: "wf", FirstRunID: "run"}

	in.Action = "park"
	out, err := act.Execute(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, TriggerCapacitySlotOutput{Held: true}, out)
	require.Equal(t, want, parked)

	in.Action = "resume"
	out, err = act.Execute(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, TriggerCapacitySlotOutput{Held: true, Granted: false}, out, "a full scope is not granted")
	require.Equal(t, want, resumed)

	in.Action = "steal"
	_, err = act.Execute(context.Background(), in)
	require.Error(t, err)

	repo.ParkTriggerCapacityFunc = func(context.Context, store.TriggerCapacitySlot) (bool, error) {
		return false, errors.New("db down")
	}
	in.Action = "park"
	_, err = act.Execute(context.Background(), in)
	require.Error(t, err, "a store failure is the activity's failure; the workflow keeps the slot")
}
