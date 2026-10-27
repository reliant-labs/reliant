package reviewpublisheradapter

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	servicepb "go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
)

type continuityRPC struct{ reset, cycle, unavailable bool }

func (f continuityRPC) GetWorkflowExecutionHistory(_ context.Context, in *servicepb.GetWorkflowExecutionHistoryRequest, _ ...grpc.CallOption) (*servicepb.GetWorkflowExecutionHistoryResponse, error) {
	if f.unavailable {
		return nil, errors.New("history unavailable")
	}
	if in.Namespace != "private" || in.Execution.WorkflowId != "root" || in.Execution.RunId == "" {
		return nil, errors.New("not exact")
	}
	run := in.Execution.RunId
	var events []*historypb.HistoryEvent
	if in.HistoryEventFilterType == enumspb.HISTORY_EVENT_FILTER_TYPE_CLOSE_EVENT {
		if run == "initial" {
			next := "successor"
			if f.cycle {
				next = "initial"
			}
			events = []*historypb.HistoryEvent{{EventId: 9, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW, Attributes: &historypb.HistoryEvent_WorkflowExecutionContinuedAsNewEventAttributes{WorkflowExecutionContinuedAsNewEventAttributes: &historypb.WorkflowExecutionContinuedAsNewEventAttributes{NewExecutionRunId: next, WorkflowType: &commonpb.WorkflowType{Name: "PrivateTrustedReviewer"}, TaskQueue: &taskqueuepb.TaskQueue{Name: "private"}, Initiator: enumspb.CONTINUE_AS_NEW_INITIATOR_WORKFLOW}}}}
		}
	} else {
		start := &historypb.WorkflowExecutionStartedEventAttributes{WorkflowId: "root", OriginalExecutionRunId: run, FirstExecutionRunId: "initial", WorkflowType: &commonpb.WorkflowType{Name: "PrivateTrustedReviewer"}, TaskQueue: &taskqueuepb.TaskQueue{Name: "private"}}
		if run == "successor" {
			start.ContinuedExecutionRunId = "initial"
		}
		if f.reset {
			start.OriginalExecutionRunId = "different"
		}
		events = []*historypb.HistoryEvent{{EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: start}}}
	}
	return &servicepb.GetWorkflowExecutionHistoryResponse{History: &historypb.History{Events: events}}, nil
}
func (f continuityRPC) DescribeWorkflowExecution(_ context.Context, in *servicepb.DescribeWorkflowExecutionRequest, _ ...grpc.CallOption) (*servicepb.DescribeWorkflowExecutionResponse, error) {
	return &servicepb.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: in.Execution, Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING}}, nil
}
func TestResolveReviewerRecipientOnlyProvenCAN(t *testing.T) {
	a := store.ReviewAuthority{Binding: reviewpublisher.Binding{Reviewer: reviewpublisher.Execution{RootID: "root", RunID: "initial"}}}
	for _, tc := range []struct {
		name string
		rpc  continuityRPC
		run  string
		ok   bool
	}{
		{"successor", continuityRPC{}, "successor", true}, {"not latest", continuityRPC{}, "unrelated", false}, {"reset", continuityRPC{reset: true}, "successor", false}, {"cycle", continuityRPC{cycle: true}, "successor", false}, {"unavailable", continuityRPC{unavailable: true}, "successor", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (TemporalEvidence{Service: tc.rpc, Namespace: "private"}).VerifyRecipient(context.Background(), a, tc.run)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
