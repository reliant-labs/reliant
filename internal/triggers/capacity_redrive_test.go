package triggers

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

type capacityRedriveRepo struct {
	EventRepo
	events  []*core.TriggerEvent
	settled []string
}

func (r *capacityRedriveRepo) ListStalePendingTriggerEvents(context.Context, time.Time, int) ([]*core.TriggerEvent, error) {
	return r.events, nil
}
func (r *capacityRedriveRepo) SettlePendingTriggerEvent(_ context.Context, id string, _ core.TriggerEventOutcome, _ string) (bool, error) {
	r.settled = append(r.settled, id)
	return true, nil
}

// A queued capacity intent expires after redriveGiveUp like any undelivered
// event, so a queue that never drains surfaces instead of growing forever. A
// launched start awaiting its binding is not waiting and keeps being redriven.
func TestCapacityQueueExpiresButLaunchedStartIsRedriven(t *testing.T) {
	now := time.Now().UTC()
	triggerID := "activation"
	order := int64(1)
	repo := &capacityRedriveRepo{events: []*core.TriggerEvent{
		{ID: "queued-old", TriggerID: &triggerID, Kind: core.TriggerEventKindIntegration, DedupeKey: "queued-old", Outcome: core.TriggerEventPending, CreatedAt: now.Add(-48 * time.Hour), CapacityOrder: &order},
		{ID: "queued-fresh", TriggerID: &triggerID, Kind: core.TriggerEventKindIntegration, DedupeKey: "queued-fresh", Outcome: core.TriggerEventPending, CreatedAt: now.Add(-time.Hour), CapacityOrder: &order},
		{ID: "launched-unbound", TriggerID: &triggerID, Kind: core.TriggerEventKindIntegration, DedupeKey: "launched", Outcome: core.TriggerEventLaunched, CreatedAt: now.Add(-48 * time.Hour), CapacityOrder: &order},
		{ID: "ordinary-delivery", TriggerID: &triggerID, Kind: core.TriggerEventKindIntegration, DedupeKey: "ordinary", Outcome: core.TriggerEventPending, CreatedAt: now.Add(-48 * time.Hour)},
	}}
	starter := &recordingStarter{}
	redriver := NewRedriver(repo, starter, "")
	redriver.now = func() time.Time { return now }
	require.NoError(t, redriver.RedriveOnce(context.Background()))
	require.Equal(t, []string{"queued-old", "ordinary-delivery"}, repo.settled)
	require.Len(t, starter.snapshot(), 2, "the fresh queued intent and the launched start are redriven")
}
