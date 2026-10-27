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

func TestCapacityQueueNeverExpiresAsDelivery(t *testing.T) {
	now := time.Now().UTC()
	triggerID := "activation"
	order := int64(1)
	repo := &capacityRedriveRepo{events: []*core.TriggerEvent{
		{ID: "capacity", TriggerID: &triggerID, Kind: core.TriggerEventKindIntegration, DedupeKey: "capacity", CreatedAt: now.Add(-48 * time.Hour), CapacityOrder: &order},
		{ID: "ordinary-delivery", TriggerID: &triggerID, Kind: core.TriggerEventKindIntegration, DedupeKey: "ordinary", CreatedAt: now.Add(-48 * time.Hour)},
	}}
	starter := &recordingStarter{}
	redriver := NewRedriver(repo, starter, "")
	redriver.now = func() time.Time { return now }
	require.NoError(t, redriver.RedriveOnce(context.Background()))
	require.Equal(t, []string{"ordinary-delivery"}, repo.settled)
	require.Len(t, starter.snapshot(), 1, "intentional capacity wait must be retried, not expired")
}
