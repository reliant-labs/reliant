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

// GetTrigger finds none: the redriver reads a settled event's trigger only
// to name it on its span.
func (r *capacityRedriveRepo) GetTrigger(context.Context, string) (*core.Trigger, error) {
	return nil, core.ErrTriggerNotFound
}
func (r *capacityRedriveRepo) SettlePendingTriggerEvent(_ context.Context, id string, _ core.TriggerEventOutcome, _ string, _ *core.TriggerEventDefinition) (bool, error) {
	r.settled = append(r.settled, id)
	return true, nil
}

// A queued capacity intent expires after redriveGiveUp like any undelivered
// event, so a queue that never drains surfaces instead of growing forever. A
// launched start awaiting its binding is not waiting and keeps being redriven.
func TestCapacityQueueExpiresButLaunchedStartIsRedriven(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
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

type capacityExpiryRepo struct {
	*capacityRedriveRepo
	expiredBefore []time.Time
	// expired is what the sweep reports it settled.
	expired []*core.TriggerEvent
}

func (r *capacityExpiryRepo) ExpirePendingTriggerCapacityEvents(_ context.Context, recordedBefore time.Time, detail string, _ int) ([]*core.TriggerEvent, error) {
	if detail != capacityExpiredDetail {
		return nil, nil
	}
	r.expiredBefore = append(r.expiredBefore, recordedBefore)
	return r.expired, nil
}

// The listing returns one head per scope in priority order, so an expired
// low-priority event behind a fresh higher-priority head is never listed. The
// redriver sweeps every waiting event past the deadline on its own, so that
// event expires; the head is still redriven.
func TestCapacityExpirySweepsBehindPriorityHeads(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	triggerID := "activation"
	order := int64(2)
	repo := &capacityExpiryRepo{capacityRedriveRepo: &capacityRedriveRepo{events: []*core.TriggerEvent{
		{ID: "fresh-high", TriggerID: &triggerID, Kind: core.TriggerEventKindIntegration, DedupeKey: "fresh-high", Outcome: core.TriggerEventPending, CreatedAt: now.Add(-time.Hour), CapacityOrder: &order},
	}}}
	starter := &recordingStarter{}
	redriver := NewRedriver(repo, starter, "")
	redriver.now = func() time.Time { return now }
	require.NoError(t, redriver.RedriveOnce(context.Background()))
	require.Equal(t, []time.Time{now.Add(-redriveGiveUp)}, repo.expiredBefore)
	require.Empty(t, repo.settled, "the fresh head is not expired")
	require.Len(t, starter.snapshot(), 1, "the head is redriven")
}
