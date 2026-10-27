// Copyright (c) 2025 Reliant Labs
package runcontinuity

import (
	"context"
	"fmt"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	servicepb "go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
)

// HistoryService is the read-only provider surface. Raw RPCs expose page tokens
// so malformed/cyclic pagination can be bounded, unlike an opaque iterator.
type HistoryService interface {
	GetWorkflowExecutionHistory(context.Context, *servicepb.GetWorkflowExecutionHistoryRequest, ...grpc.CallOption) (*servicepb.GetWorkflowExecutionHistoryResponse, error)
	DescribeWorkflowExecution(context.Context, *servicepb.DescribeWorkflowExecutionRequest, ...grpc.CallOption) (*servicepb.DescribeWorkflowExecutionResponse, error)
}

// TemporalReader fetches only the first event and close-event filter per exact
// execution, never all history payloads. Namespace is trusted server config.
type TemporalReader struct {
	Service   HistoryService
	Namespace string
}

// Snapshot reads immutable start/close proof and exact-run liveness. Histories
// unavailable after retention are an error; never authorize latest-run fallback.
func (r TemporalReader) Snapshot(ctx context.Context, e Execution) (Snapshot, error) {
	if r.Service == nil || r.Namespace == "" || !valid(e) {
		return Snapshot{}, ErrUnverified
	}
	execution := &commonpb.WorkflowExecution{WorkflowId: e.WorkflowID, RunId: e.RunID}
	first, err := r.Service.GetWorkflowExecutionHistory(ctx, &servicepb.GetWorkflowExecutionHistoryRequest{Namespace: r.Namespace, Execution: execution, MaximumPageSize: 1, HistoryEventFilterType: enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT})
	if err != nil {
		return Snapshot{}, err
	}
	events := first.GetHistory().GetEvents()
	if len(events) != 1 || events[0].GetEventId() != 1 || events[0].GetEventType() != enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED {
		return Snapshot{}, fmt.Errorf("%w: missing first event", ErrUnverified)
	}
	result := Snapshot{Start: events[0].GetWorkflowExecutionStartedEventAttributes()}
	token := []byte(nil)
	seen := map[string]bool{}
	for page := 0; page < 4; page++ {
		closePage, err := r.Service.GetWorkflowExecutionHistory(ctx, &servicepb.GetWorkflowExecutionHistoryRequest{Namespace: r.Namespace, Execution: execution, MaximumPageSize: 2, HistoryEventFilterType: enumspb.HISTORY_EVENT_FILTER_TYPE_CLOSE_EVENT, NextPageToken: token})
		if err != nil {
			return Snapshot{}, err
		}
		closes := closePage.GetHistory().GetEvents()
		if len(closes) > 1 || (len(closes) > 0 && result.Close != nil) {
			return Snapshot{}, fmt.Errorf("%w: ambiguous close", ErrUnverified)
		}
		if len(closes) == 1 {
			result.Close = closes[0]
		}
		token = closePage.GetNextPageToken()
		if len(token) == 0 {
			break
		}
		if seen[string(token)] || page == 3 {
			return Snapshot{}, fmt.Errorf("%w: history pagination limit", ErrUnverified)
		}
		seen[string(token)] = true
	}
	if result.Close != nil {
		return result, nil
	}
	description, err := r.Service.DescribeWorkflowExecution(ctx, &servicepb.DescribeWorkflowExecutionRequest{Namespace: r.Namespace, Execution: execution})
	if err != nil {
		return Snapshot{}, err
	}
	info := description.GetWorkflowExecutionInfo()
	if info.GetExecution().GetWorkflowId() != e.WorkflowID || info.GetExecution().GetRunId() != e.RunID {
		return Snapshot{}, ErrUnverified
	}
	// A close raced our first read: retry resolution rather than misclassify as
	// terminal or route to a new current pointer. Caller activity retry is durable.
	if info.GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		return Snapshot{}, fmt.Errorf("%w: execution closed while reading proof", ErrUnverified)
	}
	result.Running = true
	return result, nil
}

var _ Reader = TemporalReader{}
