// Copyright (c) 2025 Reliant Labs
package runcontinuity

import (
	"context"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	servicepb "go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
)

type rpcFake struct {
	calls    int
	cyclic   bool
	wrongRun bool
}

func (f *rpcFake) GetWorkflowExecutionHistory(_ context.Context, in *servicepb.GetWorkflowExecutionHistoryRequest, _ ...grpc.CallOption) (*servicepb.GetWorkflowExecutionHistoryResponse, error) {
	f.calls++
	if in.GetExecution().GetWorkflowId() != "wf" || in.GetExecution().GetRunId() != "a" || in.GetWaitNewEvent() {
		return nil, ErrUnverified
	}
	if in.GetHistoryEventFilterType() == enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT {
		return &servicepb.GetWorkflowExecutionHistoryResponse{History: &historypb.History{Events: []*historypb.HistoryEvent{{EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: start("a", "")}}}}}, nil
	}
	if f.cyclic {
		return &servicepb.GetWorkflowExecutionHistoryResponse{NextPageToken: []byte("cycle")}, nil
	}
	return &servicepb.GetWorkflowExecutionHistoryResponse{}, nil
}
func (f *rpcFake) DescribeWorkflowExecution(_ context.Context, in *servicepb.DescribeWorkflowExecutionRequest, _ ...grpc.CallOption) (*servicepb.DescribeWorkflowExecutionResponse, error) {
	if in.GetExecution().GetRunId() != "a" {
		return nil, ErrUnverified
	}
	run := "a"
	if f.wrongRun {
		run = "latest"
	}
	return &servicepb.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: "wf", RunId: run}, Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING}}, nil
}
func TestTemporalReaderExactRunAndBoundedPagination(t *testing.T) {
	for _, tt := range []struct {
		name                      string
		cyclic, wrongRun, wantErr bool
	}{{"live", false, false, false}, {"cycle", true, false, true}, {"wrong described run", false, true, true}} {
		t.Run(tt.name, func(t *testing.T) {
			fake := &rpcFake{cyclic: tt.cyclic, wrongRun: tt.wrongRun}
			reader := TemporalReader{Service: fake, Namespace: "trusted"}
			got, err := reader.Snapshot(context.Background(), Execution{"wf", "a"})
			if tt.wantErr && err == nil {
				t.Fatalf("accepted %+v", got)
			}
			if !tt.wantErr && (err != nil || !got.Running) {
				t.Fatalf("%+v %v", got, err)
			}
			if fake.calls > 5 {
				t.Fatalf("unbounded RPCs %d", fake.calls)
			}
		})
	}
}
