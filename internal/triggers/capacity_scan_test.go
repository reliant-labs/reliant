package triggers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/workflowservice/v1"
)

type scanErrorRepository struct {
	EventRepo
	*capacityBoundary
	events  []*core.TriggerEvent
	scanErr error
}

func (r *scanErrorRepository) GetTriggerEventByDedupe(ctx context.Context, k core.TriggerEventKind, key string) (*core.TriggerEvent, error) {
	return r.capacityBoundary.GetTriggerEventByDedupe(ctx, k, key)
}
func (r *scanErrorRepository) ListStalePendingTriggerEvents(context.Context, time.Time, int) ([]*core.TriggerEvent, error) {
	return r.events, nil
}
func (r *scanErrorRepository) ListTriggerCapacityExecutionBatch(context.Context, int) ([]store.TriggerCapacityExecution, error) {
	return nil, r.scanErr
}

type describedRecordingStarter struct{ *recordingStarter }

func (s *describedRecordingStarter) DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return nil, errors.New("unavailable")
}

func TestCapacityScanErrorDoesNotPreventOrdinaryDeliveryRedrive(t *testing.T) {
	triggerID := "ordinary-trigger"
	scanErr := errors.New("capacity scan unavailable")
	repo := &scanErrorRepository{capacityBoundary: &capacityBoundary{}, scanErr: scanErr, events: []*core.TriggerEvent{{ID: "ordinary-event", TriggerID: &triggerID, Kind: core.TriggerEventKindIntegration, DedupeKey: "ordinary-delivery", CreatedAt: time.Now().Add(-time.Hour)}}}
	starter := &describedRecordingStarter{recordingStarter: &recordingStarter{}}
	err := NewRedriver(repo, starter, "").RedriveOnce(context.Background())
	require.ErrorIs(t, err, scanErr)
	require.Len(t, starter.snapshot(), 1, "ordinary work is redriven before capacity scan failure is returned")
}
