package triggers

import (
	"context"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	queuepb "go.temporal.io/api/taskqueue/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	servicepb "go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
)

// singleAdmissionHistory gives older exact-run recovery tests authenticated
// start/close evidence in addition to their controlled Describe projection.
type singleAdmissionHistory struct{ probe *admissionTemporalProbe }

func (h singleAdmissionHistory) Snapshot(_ context.Context, e runcontinuity.Execution) (runcontinuity.Snapshot, error) {
	if h.probe.err != nil {
		return runcontinuity.Snapshot{}, h.probe.err
	}
	info := h.probe.desc.GetWorkflowExecutionInfo()
	if info.GetExecution().GetWorkflowId() != e.WorkflowID || info.GetExecution().GetRunId() != e.RunID {
		return runcontinuity.Snapshot{}, runcontinuity.ErrUnverified
	}
	s := runcontinuity.Snapshot{Start: admissionChainStart(e.WorkflowID, e.RunID, e.RunID, "")}
	switch info.GetStatus() {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING:
		s.Running = true
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		s.Close = &historypb.HistoryEvent{EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED}
	default:
		return runcontinuity.Snapshot{}, runcontinuity.ErrUnverified
	}
	return s, nil
}

func admissionChainStart(root, run, first, previous string) *historypb.WorkflowExecutionStartedEventAttributes {
	return &historypb.WorkflowExecutionStartedEventAttributes{WorkflowId: root, OriginalExecutionRunId: run, FirstExecutionRunId: first, ContinuedExecutionRunId: previous, WorkflowType: &commonpb.WorkflowType{Name: "DynamicWorkflow"}, TaskQueue: &queuepb.TaskQueue{Name: "review-worker"}}
}
func admissionChainCAN(next string) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{EventId: 10, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW, Attributes: &historypb.HistoryEvent_WorkflowExecutionContinuedAsNewEventAttributes{WorkflowExecutionContinuedAsNewEventAttributes: &historypb.WorkflowExecutionContinuedAsNewEventAttributes{NewExecutionRunId: next, WorkflowType: &commonpb.WorkflowType{Name: "DynamicWorkflow"}, TaskQueue: &queuepb.TaskQueue{Name: "review-worker"}}}}
}

// admissionHistoryRPC exercises the real TemporalReader using exact raw history
// RPCs, including Temporal's complete first persistence batch. No latest-run
// history lookup is allowed, even if the chat was changed to a human followup.
type admissionHistoryRPC struct {
	root        string
	snapshots   map[string]runcontinuity.Snapshot
	calls       []string
	unavailable bool
}

func (h *admissionHistoryRPC) GetWorkflowExecutionHistory(_ context.Context, in *servicepb.GetWorkflowExecutionHistoryRequest, _ ...grpc.CallOption) (*servicepb.GetWorkflowExecutionHistoryResponse, error) {
	if in.GetNamespace() != "trusted-review-namespace" || in.GetExecution().GetWorkflowId() != h.root || in.GetExecution().GetRunId() == "" {
		return nil, fmt.Errorf("untrusted namespace or nonexact history request")
	}
	h.calls = append(h.calls, in.GetExecution().GetRunId())
	if h.unavailable {
		return nil, fmt.Errorf("history unavailable")
	}
	s, ok := h.snapshots[in.GetExecution().GetRunId()]
	if !ok {
		return nil, fmt.Errorf("unknown run")
	}
	if in.GetHistoryEventFilterType() == enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT {
		return &servicepb.GetWorkflowExecutionHistoryResponse{History: &historypb.History{Events: []*historypb.HistoryEvent{
			{EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: s.Start}},
			{EventId: 2, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED},
		}}}, nil
	}
	out := &servicepb.GetWorkflowExecutionHistoryResponse{History: &historypb.History{}}
	if s.Close != nil {
		out.History.Events = []*historypb.HistoryEvent{s.Close}
	}
	return out, nil
}
func (h *admissionHistoryRPC) DescribeWorkflowExecution(_ context.Context, in *servicepb.DescribeWorkflowExecutionRequest, _ ...grpc.CallOption) (*servicepb.DescribeWorkflowExecutionResponse, error) {
	if in.GetNamespace() != "trusted-review-namespace" || in.GetExecution().GetWorkflowId() != h.root || in.GetExecution().GetRunId() == "" {
		return nil, fmt.Errorf("nonexact describe")
	}
	s, ok := h.snapshots[in.GetExecution().GetRunId()]
	if !ok {
		return nil, fmt.Errorf("unknown run")
	}
	status := enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
	if s.Close != nil {
		status = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
		if s.Close.GetEventType() == enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW {
			status = enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW
		}
	}
	return &servicepb.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: in.Execution, Status: status}}, nil
}

func admissionCANFixture(t *testing.T) (*storetest.Env, *Redriver, *admissionHistoryRPC, string, string) {
	t.Helper()
	env := storetest.New(t)
	ctx := context.Background()
	ev := reviewAdmissionEvent(t, env, "can-event", "pr", nil)
	reviewReserve(t, env, ev.ID)
	root, initial := "logical-review-root", "original-review-run"
	if err := env.Repo.CreateChat(ctx, &db.Chat{ID: root, Title: "review", ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID}); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.UpdateTriggerEventOutcome(ctx, ev.ID, core.TriggerEventLaunched, "", &root); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.BindTriggerAdmissionRun(ctx, ev.ID, initial); err != nil {
		t.Fatal(err)
	}
	successor := "internal-successor"
	histories := &admissionHistoryRPC{root: root, snapshots: map[string]runcontinuity.Snapshot{
		initial:   {Start: admissionChainStart(root, initial, initial, ""), Close: admissionChainCAN(successor)},
		successor: {Start: admissionChainStart(root, successor, initial, initial), Running: true},
	}}
	probe := &admissionTemporalProbe{desc: &servicepb.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: root, RunId: initial}, Status: enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW}}}
	r := NewRedriver(env.Repo, probe, "").WithContinuity(runcontinuity.TemporalReader{Service: histories, Namespace: "trusted-review-namespace"})
	return env, r, histories, ev.ID, initial
}

func TestAdmissionLogicalCANRetainsThenReleasesOriginalReservation(t *testing.T) {
	env, r, h, eventID, original := admissionCANFixture(t)
	ctx := context.Background()
	// Delete the chat: reconciliation must still start from immutable root/run.
	if _, err := env.SQL.ExecContext(ctx, `DELETE FROM chats WHERE id=$1`, h.root); err != nil {
		t.Fatal(err)
	}
	if err := r.redriveAdmissions(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := env.Repo.GetTriggerAdmission(ctx, eventID)
	if a.State != "reserved" || a.RunID != original {
		t.Fatalf("CAN original close freed/rewrote permit: %+v", a)
	}
	successor := h.snapshots["internal-successor"]
	successor.Running = false
	successor.Close = &historypb.HistoryEvent{EventId: 20, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED}
	h.snapshots["internal-successor"] = successor
	if err := r.redriveAdmissions(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ = env.Repo.GetTriggerAdmission(ctx, eventID)
	if a.State != "released" || a.RunID != original {
		t.Fatalf("logical terminal failed original CAS: %+v", a)
	}
	for _, id := range h.calls {
		if id != original && id != "internal-successor" {
			t.Fatalf("followed unrelated execution %q", id)
		}
	}
}

func TestAdmissionUnverifiedSuccessorsRetainCapacity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*admissionHistoryRPC)
	}{
		{"retry", func(h *admissionHistoryRPC) {
			h.snapshots["internal-successor"].Start.Initiator = enumspb.CONTINUE_AS_NEW_INITIATOR_RETRY
		}},
		{"reset", func(h *admissionHistoryRPC) {
			h.snapshots["internal-successor"].Start.OriginalExecutionRunId = "reset-source"
		}},
		{"human followup", func(h *admissionHistoryRPC) {
			h.snapshots["internal-successor"].Start.ContinuedExecutionRunId = "unrelated-human-turn"
		}},
		{"history unavailable", func(h *admissionHistoryRPC) { h.unavailable = true }},
		{"hop bound", func(h *admissionHistoryRPC) {
			previous := "original-review-run"
			for i := 0; i < 65; i++ {
				next := fmt.Sprintf("hop-%d", i)
				prior := h.snapshots[previous]
				prior.Close = admissionChainCAN(next)
				prior.Running = false
				h.snapshots[previous] = prior
				h.snapshots[next] = runcontinuity.Snapshot{Start: admissionChainStart(h.root, next, "original-review-run", previous), Running: true}
				previous = next
			}
		}},
		{"cycle", func(h *admissionHistoryRPC) {
			h.snapshots["original-review-run"].Close.GetWorkflowExecutionContinuedAsNewEventAttributes().NewExecutionRunId = "original-review-run"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, r, h, id, original := admissionCANFixture(t)
			tt.mutate(h)
			successor := h.snapshots["internal-successor"]
			successor.Running = false
			successor.Close = &historypb.HistoryEvent{EventId: 20, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED}
			h.snapshots["internal-successor"] = successor
			if err := r.redriveAdmissions(context.Background()); err != nil {
				t.Fatal(err)
			}
			a, _ := env.Repo.GetTriggerAdmission(context.Background(), id)
			if a.State != "reserved" || a.RunID != original {
				t.Fatalf("unverified successor released/rewrote: %+v", a)
			}
		})
	}
}

func TestAdmissionMissingContinuityReaderRetainsCAN(t *testing.T) {
	env, r, _, id, _ := admissionCANFixture(t)
	r.WithContinuity(nil)
	if err := r.redriveAdmissions(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, _ := env.Repo.GetTriggerAdmission(context.Background(), id)
	if a.State != "reserved" {
		t.Fatal("nil continuity reader released CAN")
	}
}
