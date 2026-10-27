// Copyright (c) 2025 Reliant Labs
package runcontinuity

import (
	"context"
	"errors"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	queuepb "go.temporal.io/api/taskqueue/v1"
)

type histories map[string]Snapshot

func (h histories) Snapshot(_ context.Context, e Execution) (Snapshot, error) {
	if e.WorkflowID != "wf" || e.RunID == "" {
		return Snapshot{}, ErrUnverified
	}
	s, ok := h[e.RunID]
	if !ok {
		return Snapshot{}, errors.New("history missing")
	}
	return s, nil
}
func start(run, previous string) *historypb.WorkflowExecutionStartedEventAttributes {
	return &historypb.WorkflowExecutionStartedEventAttributes{WorkflowId: "wf", OriginalExecutionRunId: run, FirstExecutionRunId: "a", ContinuedExecutionRunId: previous, WorkflowType: &commonpb.WorkflowType{Name: "DynamicWorkflow"}, TaskQueue: &queuepb.TaskQueue{Name: "worker"}}
}
func can(next string) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW, Attributes: &historypb.HistoryEvent_WorkflowExecutionContinuedAsNewEventAttributes{WorkflowExecutionContinuedAsNewEventAttributes: &historypb.WorkflowExecutionContinuedAsNewEventAttributes{NewExecutionRunId: next, WorkflowType: &commonpb.WorkflowType{Name: "DynamicWorkflow"}, TaskQueue: &queuepb.TaskQueue{Name: "worker"}}}}
}
func validHistory() histories {
	return histories{"a": {Start: start("a", ""), Close: can("b")}, "b": {Start: start("b", "a"), Running: true}}
}

func TestResolveProvesOnlyBidirectionalInternalContinueAsNew(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(histories)
		want   bool
	}{{"Go SDK unspecified initiator", func(histories) {}, true}, {"explicit workflow initiator", func(h histories) {
		h["a"].Close.GetWorkflowExecutionContinuedAsNewEventAttributes().Initiator = enumspb.CONTINUE_AS_NEW_INITIATOR_WORKFLOW
		h["b"].Start.Initiator = enumspb.CONTINUE_AS_NEW_INITIATOR_WORKFLOW
	}, true}, {"retry", func(h histories) { h["b"].Start.Initiator = enumspb.CONTINUE_AS_NEW_INITIATOR_RETRY }, false}, {"cron", func(h histories) {
		h["a"].Close.GetWorkflowExecutionContinuedAsNewEventAttributes().Initiator = enumspb.CONTINUE_AS_NEW_INITIATOR_CRON_SCHEDULE
	}, false}, {"reset", func(h histories) { h["b"].Start.OriginalExecutionRunId = "reset-source" }, false}, {"unrelated same first ID", func(h histories) { h["b"].Start.ContinuedExecutionRunId = "human-new-turn" }, false}, {"different first ID", func(h histories) { h["b"].Start.FirstExecutionRunId = "new-root" }, false}, {"queue changes", func(h histories) { h["b"].Start.TaskQueue.Name = "other" }, false}, {"workflow changes", func(h histories) { h["b"].Start.WorkflowType.Name = "other" }, false}, {"wrong workflow ID", func(h histories) { h["b"].Start.WorkflowId = "other" }, false}, {"cycle", func(h histories) {
		h["a"].Close.GetWorkflowExecutionContinuedAsNewEventAttributes().NewExecutionRunId = "a"
	}, false}, {"missing successor", func(h histories) { delete(h, "b") }, false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := validHistory()
			tt.mutate(h)
			got, err := Resolve(context.Background(), h, Execution{"wf", "a"})
			if tt.want {
				if err != nil || !got.Live || got.Current.RunID != "b" || len(got.Edges) != 1 {
					t.Fatalf("%+v %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %+v", got)
			}
		})
	}
}

func TestResolveTerminalNeverFollowsFailureRetryRun(t *testing.T) {
	h := histories{"a": {Start: start("a", ""), Close: &historypb.HistoryEvent{EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED, Attributes: &historypb.HistoryEvent_WorkflowExecutionFailedEventAttributes{WorkflowExecutionFailedEventAttributes: &historypb.WorkflowExecutionFailedEventAttributes{NewExecutionRunId: "not-authorized"}}}}}
	got, err := Resolve(context.Background(), h, Execution{"wf", "a"})
	if err != nil || got.Live || got.Current.RunID != "a" || got.Terminal != enumspb.WORKFLOW_EXECUTION_STATUS_FAILED {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestResolveContextAndEmptyIdentityFailClosed(t *testing.T) {
	if _, err := Resolve(context.Background(), validHistory(), Execution{"wf", ""}); err == nil {
		t.Fatal("accepted latest lookup")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Resolve(ctx, validHistory(), Execution{"wf", "a"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
