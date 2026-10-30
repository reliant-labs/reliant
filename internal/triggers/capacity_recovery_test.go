package triggers

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
)

type fencedCapacityBoundary struct {
	*capacityBoundary
	fenced, won                      bool
	checked, attempted, releaseCalls int
	after                            *workflowservice.DescribeWorkflowExecutionResponse
	afterErr                         error
}

func (b *fencedCapacityBoundary) TriggerCapacityStartFenced(context.Context, store.TriggerCapacityExecution) (bool, error) {
	b.checked++
	return b.fenced, nil
}
func (b *fencedCapacityBoundary) FenceUnboundTriggerCapacityStart(context.Context, store.TriggerCapacityExecution) (bool, error) {
	b.attempted++
	return b.won, nil
}
func (b *fencedCapacityBoundary) ReleaseUnboundTriggerCapacity(context.Context, store.TriggerCapacityExecution, string) (bool, error) {
	b.releaseCalls++
	return true, nil
}
func (b *fencedCapacityBoundary) DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return b.after, b.afterErr
}

func TestCapacityMissingStartRequiresFenceAndSecondExternalRead(t *testing.T) {
	for _, tc := range []struct {
		name              string
		fenced, won, late bool
		want              int
	}{
		{"pending-retry-wins", false, false, false, 0},
		{"failed-start-fenced", false, true, false, 1},
		{"missing-root-fenced", true, false, false, 1},
		{"late-acceptance", false, true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &fencedCapacityBoundary{capacityBoundary: &capacityBoundary{}, fenced: tc.fenced, won: tc.won, afterErr: serviceerror.NewNotFound("absent")}
			if tc.late {
				b.afterErr = nil
				b.after = &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{}}
			}
			e := store.TriggerCapacityExecution{EventID: "event", WorkflowID: "workflow"}
			require.NoError(t, recoverMissingCapacityStart(context.Background(), b, b, e))
			require.Equal(t, tc.want, b.releaseCalls)
		})
	}
}
