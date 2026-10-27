package reviewpublisheradapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	servicepb "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/grpc"
)

type historyFixture struct {
	events []*historypb.HistoryEvent
	cycle  bool
	calls  int
}

func (f *historyFixture) GetWorkflowExecutionHistory(_ context.Context, in *servicepb.GetWorkflowExecutionHistoryRequest, _ ...grpc.CallOption) (*servicepb.GetWorkflowExecutionHistoryResponse, error) {
	if in.GetExecution().GetWorkflowId() != "review-root" || in.GetExecution().GetRunId() != "review-run" || in.Namespace != "private" {
		return nil, errors.New("wrong exact execution")
	}
	f.calls++
	events := f.events
	if in.MaximumPageSize == 1 {
		events = events[:1]
	} else if in.HistoryEventFilterType == enumspb.HISTORY_EVENT_FILTER_TYPE_CLOSE_EVENT {
		events = events[len(events)-1:]
	}
	out := &servicepb.GetWorkflowExecutionHistoryResponse{History: &historypb.History{Events: events}}
	if f.cycle && in.MaximumPageSize == 256 {
		out.NextPageToken = []byte("cycle")
	}
	return out, nil
}
func (f *historyFixture) DescribeWorkflowExecution(context.Context, *servicepb.DescribeWorkflowExecutionRequest, ...grpc.CallOption) (*servicepb.DescribeWorkflowExecutionResponse, error) {
	return nil, errors.New("must not describe latest")
}
func payload(t *testing.T, v any) *commonpb.Payloads {
	t.Helper()
	p, err := converter.GetDefaultDataConverter().ToPayloads(v)
	require.NoError(t, err)
	return p
}

func evidenceFixture(t *testing.T) (TemporalEvidence, store.ReviewAuthority, *historyFixture) {
	t.Helper()
	graph := json.RawMessage(`{"name":"trusted","nodes":[{"id":"finish"}]}`)
	snapshot, err := json.Marshal(GraphSnapshot{WorkflowType: "DynamicWorkflow", RootRef: "trusted", Graphs: map[string]json.RawMessage{"trusted": graph}, CompletionNode: "finish", CompletionActivity: CompletionActivityName, OutputSchema: CompletionSchema})
	require.NoError(t, err)
	digest, err := store.DigestReviewSnapshot(snapshot)
	require.NoError(t, err)
	a := store.ReviewAuthority{ID: "assignment", Binding: reviewpublisher.Binding{Reviewer: reviewpublisher.Execution{RootID: "review-root", RunID: "review-run"}, WorkflowDigest: digest}, GraphSnapshot: snapshot, CompletionNode: "finish"}
	scheduled := func(id int64, name, activityID string, input any) *historypb.HistoryEvent {
		return &historypb.HistoryEvent{EventId: id, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED, Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{ActivityId: activityID, ActivityType: &commonpb.ActivityType{Name: name}, Input: payload(t, input)}}}
	}
	completed := func(id, schedule int64, result any) *historypb.HistoryEvent {
		return &historypb.HistoryEvent{EventId: id, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED, Attributes: &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{ScheduledEventId: schedule, Result: payload(t, result)}}}
	}
	f := &historyFixture{events: []*historypb.HistoryEvent{
		{EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{WorkflowId: "review-root", OriginalExecutionRunId: "review-run", FirstExecutionRunId: "review-run", WorkflowType: &commonpb.WorkflowType{Name: "DynamicWorkflow"}, TaskQueue: &taskqueuepb.TaskQueue{Name: "private-review"}}}},
		scheduled(2, "ActivityLoadWorkflow", "load", map[string]string{"workflow_name": "trusted"}), completed(3, 2, struct {
			WorkflowJSON []byte `json:"workflow_json"`
		}{graph}),
		scheduled(4, CompletionActivityName, "complete", CompletionInput{AssignmentID: "assignment", Runtime: types.RuntimeContext{WorkflowID: "review-root", StepID: "finish", NodePath: "finish"}, Result: GraphResult{Verdict: reviewpublisher.Approve}}), completed(5, 4, map[string]bool{"recorded": true}),
		scheduled(6, "WorkflowStatus", "status", map[string]string{"workflow_id": "review-root", "status": "completed", "outcome": "success"}), completed(7, 6, nil),
		{EventId: 8, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED, Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{Result: payload(t, map[string]any{"outputs": map[string]any{"review_verdict": "approve"}})}}},
	}}
	return TemporalEvidence{Service: f, Namespace: "private"}, a, f
}

func TestHistoryCompletionRequiresTerminalGraphEvidence(t *testing.T) {
	r, a, _ := evidenceFixture(t)
	pending, err := r.Verify(context.Background(), a, "review-run", "complete", 4, false)
	require.NoError(t, err)
	require.False(t, pending.Terminal)
	require.False(t, pending.GraphSucceeded)
	final, err := r.Verify(context.Background(), a, "review-run", "complete", 4, true)
	require.NoError(t, err)
	require.True(t, final.Terminal)
	require.True(t, final.GraphSucceeded)
	require.Equal(t, pending.Digest, final.Digest)
}

func TestHistorySpoofingAndFailure(t *testing.T) {
	for _, name := range []string{"other node", "other assignment", "other digest", "unloaded dependency", "different loaded graph", "claimcheck missing", "failed sibling", "missing status completion", "different output"} {
		t.Run(name, func(t *testing.T) {
			r, a, f := evidenceFixture(t)
			denied := true
			switch name {
			case "other node", "other assignment":
				var in CompletionInput
				require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(f.events[3].GetActivityTaskScheduledEventAttributes().Input, &in))
				if name == "other node" {
					in.Runtime.NodePath = "author.finish"
				} else {
					in.AssignmentID = "forged"
				}
				f.events[3].GetActivityTaskScheduledEventAttributes().Input = payload(t, in)
			case "other digest":
				a.Binding.WorkflowDigest = "sha256:forged"
			case "unloaded dependency":
				var s GraphSnapshot
				require.NoError(t, json.Unmarshal(a.GraphSnapshot, &s))
				s.Graphs["unloaded"] = json.RawMessage(`{}`)
				a.GraphSnapshot, _ = json.Marshal(s)
				a.Binding.WorkflowDigest, _ = store.DigestReviewSnapshot(a.GraphSnapshot)
			case "different loaded graph":
				f.events[2].GetActivityTaskCompletedEventAttributes().Result = payload(t, struct {
					WorkflowJSON []byte `json:"workflow_json"`
				}{[]byte(`{"name":"edited"}`)})
			case "claimcheck missing":
				f.events[2].GetActivityTaskCompletedEventAttributes().Result = &commonpb.Payloads{Payloads: []*commonpb.Payload{{Metadata: map[string][]byte{"encoding": []byte("claim-check/v1")}, Data: []byte(`{"key":"sha256:missing","size":100,"zsize":10}`)}}}
			case "failed sibling":
				denied = false
				f.events[5].GetActivityTaskScheduledEventAttributes().Input = payload(t, map[string]string{"workflow_id": "review-root", "status": "completed", "outcome": "failure"})
			case "missing status completion":
				denied = false
				f.events = append(f.events[:6], f.events[7:]...)
			case "different output":
				denied = false
				f.events[7].GetWorkflowExecutionCompletedEventAttributes().Result = payload(t, map[string]any{"outputs": map[string]any{"review_verdict": "changes"}})
			}
			got, err := r.Verify(context.Background(), a, "review-run", "complete", 4, true)
			if denied {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.False(t, got.GraphSucceeded, "reserved approve cannot override failed/missing logical result")
			}
		})
	}
}

func TestHistoryBounds(t *testing.T) {
	r, a, f := evidenceFixture(t)
	f.cycle = true
	_, err := r.Verify(context.Background(), a, "review-run", "complete", 4, true)
	require.Error(t, err)
	require.Less(t, f.calls, 8)
}
