package triggers

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enums "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
)

type capacityBoundary struct {
	event    *core.TriggerEvent
	released bool
	desc     *workflowservice.DescribeWorkflowExecutionResponse
	err      error
}

func (b *capacityBoundary) EnqueueTriggerCapacity(context.Context, *core.TriggerEvent, store.TriggerCapacityScope) error {
	return nil
}
func (b *capacityBoundary) CanLaunchTriggerCapacity(context.Context, string, store.TriggerCapacityScope, uint32) (bool, error) {
	return b.released, nil
}
func (b *capacityBoundary) BindTriggerCapacityExecution(context.Context, string, string, string) error {
	return nil
}
func (b *capacityBoundary) GetTriggerEventByDedupe(context.Context, core.TriggerEventKind, string) (*core.TriggerEvent, error) {
	return b.event, nil
}
func (b *capacityBoundary) ListTriggerCapacityExecutions(context.Context, store.TriggerCapacityScope) ([]store.TriggerCapacityExecution, error) {
	return []store.TriggerCapacityExecution{{EventID: "occupied", WorkflowID: "workflow", FirstRunID: "first"}}, nil
}
func (b *capacityBoundary) ReleaseTriggerCapacityExecution(context.Context, store.TriggerCapacityExecution) error {
	b.released = true
	return nil
}
func (b *capacityBoundary) DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return b.desc, b.err
}

func TestCapacityReleaseRequiresMatchingTerminalTemporalChain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status enums.WorkflowExecutionStatus
		first  string
		apiErr bool
		want   bool
	}{
		{"running", enums.WORKFLOW_EXECUTION_STATUS_RUNNING, "first", false, false},
		{"continued", enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW, "first", false, false},
		{"missing-first", enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, "", false, false},
		{"different-chain", enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, "other", false, false},
		{"outage", enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, "first", true, false},
		{"completed", enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, "first", false, true},
		{"failed", enums.WORKFLOW_EXECUTION_STATUS_FAILED, "first", false, true},
		{"cancelled", enums.WORKFLOW_EXECUTION_STATUS_CANCELED, "first", false, true},
		{"terminated", enums.WORKFLOW_EXECUTION_STATUS_TERMINATED, "first", false, true},
		{"timed-out", enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT, "first", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &capacityBoundary{event: &core.TriggerEvent{ID: "waiting", Outcome: core.TriggerEventPending}, desc: &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: "workflow", RunId: "successor"}, FirstRunId: tc.first, Status: tc.status}}}
			if tc.apiErr {
				b.err = errors.New("unavailable")
			}
			spec := launch.Spec{}
			require.NoError(t, prepareCapacity(context.Background(), b, b, &core.Trigger{UserID: "user", ProjectID: "project", Workflow: "workflow-ref"}, b.event, 1, &spec))
			require.Equal(t, tc.want, b.released)
			reason, err := spec.Guard(context.Background())
			require.NoError(t, err)
			if tc.want {
				require.Empty(t, reason)
			} else {
				require.Equal(t, capacityWaiting, reason)
			}
		})
	}
}
