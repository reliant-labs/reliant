package core

import "testing"

// TestQueueBatchTransitions pins §3.2's batch life, every pair of states.
func TestQueueBatchTransitions(t *testing.T) {
	legal := map[[2]QueueBatchState]bool{
		{QueueBatchBuilding, QueueBatchGating}:    true,
		{QueueBatchBuilding, QueueBatchAbandoned}: true,
		{QueueBatchGating, QueueBatchPassed}:      true,
		{QueueBatchGating, QueueBatchFailed}:      true,
		{QueueBatchGating, QueueBatchUnproven}:    true,
		{QueueBatchGating, QueueBatchAbandoned}:   true,
		{QueueBatchPassed, QueueBatchLanded}:      true,
		{QueueBatchPassed, QueueBatchAbandoned}:   true,
		{QueueBatchUnproven, QueueBatchGating}:    true,
		{QueueBatchUnproven, QueueBatchAbandoned}: true,
	}
	for _, from := range AllQueueBatchStates {
		for _, to := range AllQueueBatchStates {
			if got, want := from.CanTransitionTo(to), legal[[2]QueueBatchState{from, to}]; got != want {
				t.Errorf("%s → %s: CanTransitionTo = %v, want %v", from, to, got, want)
			}
		}
	}
}
