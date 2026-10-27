package reviewpublisheradapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
	relianttemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	servicepb "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
)

// CompletionInput is scheduled only by the approved graph, never a public tool.
// Execute ignores its result and runtime fields: it reloads the exact scheduled
// event from Temporal. Server workflow code owns that scheduling provenance.
type CompletionInput struct {
	Runtime      types.RuntimeContext `json:"runtime"`
	AssignmentID string               `json:"assignment_id"`
	Result       GraphResult          `json:"result"`
}

// TemporalEvidence reads exact histories with bounded pages, events, payloads and
// continuation chains. Service and namespace are trusted server configuration.
type TemporalEvidence struct {
	Service   runcontinuity.HistoryService
	Namespace string
	// Converter must share the server claim-check payload store. Nil uses the
	// standard flexible converter, which fails closed on offloaded references.
	Converter converter.DataConverter
}

// CompletionActivity records pending evidence, never whole-graph success from
// inside a running activity. Reconcile must observe logical terminal completion.
// Register this activity explicitly only on the private reviewer worker; it is
// intentionally absent from the public activity/tool catalogs and app roots.
type CompletionActivity struct {
	Store   AuthorityStore
	History TemporalEvidence
}

// Execute authenticates the activity's exact Temporal identity and scheduled
// node path; no completed flag or caller verdict grants publication authority.
func (a CompletionActivity) Execute(ctx context.Context, in CompletionInput) error {
	if a.Store == nil || !activity.IsActivity(ctx) {
		return reviewpublisher.ErrUnauthorized
	}
	info := activity.GetInfo(ctx)
	assignment, err := a.Store.LoadReviewAuthority(ctx, in.AssignmentID)
	if err != nil {
		return err
	}
	if info.WorkflowExecution.ID != assignment.Binding.Reviewer.RootID || info.ActivityType.Name != CompletionActivityName {
		return reviewpublisher.ErrUnauthorized
	}
	evidence, err := a.History.Verify(ctx, assignment, info.WorkflowExecution.RunID, info.ActivityID, 0, false)
	if err != nil {
		return err
	}
	return a.Store.RecordReviewEvidence(ctx, evidence)
}

// Reconcile loads the persisted exact activity identity and proves the whole
// logical execution's terminal outcome, including every verified CAN successor.
func (a CompletionActivity) Reconcile(ctx context.Context, id string) error {
	if a.Store == nil {
		return reviewpublisher.ErrUnauthorized
	}
	assignment, err := a.Store.LoadReviewAuthority(ctx, id)
	if err != nil {
		return err
	}
	if assignment.ActivityID == "" || assignment.CompletionRunID == "" {
		return reviewpublisher.ErrUnauthorized
	}
	evidence, err := a.History.Verify(ctx, assignment, assignment.CompletionRunID, assignment.ActivityID, 0, true)
	if err != nil {
		return err
	}
	if !evidence.Terminal {
		return reviewpublisher.ErrUnavailable
	}
	return a.Store.RecordReviewEvidence(ctx, evidence)
}

// Verify authenticates immutable loaded graph snapshots and the reserved
// activity schedule, then derives verdict from that event and logical terminal
// outcome from the close result. It never fetches the latest run.
func (r TemporalEvidence) Verify(ctx context.Context, a store.ReviewAuthority, activityRun, activityID string, scheduledID int64, terminal bool) (store.ReviewEvidence, error) {
	denied := reviewpublisher.ErrUnauthorized
	if r.Service == nil || r.Namespace == "" || activityRun == "" || activityID == "" {
		return store.ReviewEvidence{}, denied
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	digest, err := store.DigestReviewSnapshot(a.GraphSnapshot)
	if err != nil || digest != a.Binding.WorkflowDigest {
		return store.ReviewEvidence{}, denied
	}
	var snapshot GraphSnapshot
	if json.Unmarshal(a.GraphSnapshot, &snapshot) != nil || snapshot.RootRef == "" || snapshot.WorkflowType == "" || len(snapshot.Graphs) == 0 || len(snapshot.Graphs) > 64 || snapshot.CompletionNode != a.CompletionNode || snapshot.CompletionActivity != CompletionActivityName || snapshot.OutputSchema != CompletionSchema {
		return store.ReviewEvidence{}, denied
	}
	if _, ok := snapshot.Graphs[snapshot.RootRef]; !ok {
		return store.ReviewEvidence{}, denied
	}
	original := runcontinuity.Execution{WorkflowID: a.Binding.Reviewer.RootID, RunID: a.Binding.Reviewer.RunID}
	chain, err := runcontinuity.Resolve(ctx, runcontinuity.TemporalReader{Service: r.Service, Namespace: r.Namespace}, original)
	if err != nil {
		return store.ReviewEvidence{}, err
	}
	runs := []runcontinuity.Execution{original}
	for _, edge := range chain.Edges {
		runs = append(runs, edge.To)
	}
	found := false
	observed := map[string]bool{}
	activityCompleted := false
	var scheduled *historypb.HistoryEvent
	var result GraphResult
	totalEvents, totalBytes := 0, 0
	var logicalResult GraphResult
	terminalOutcome := ""
	terminalStatusCount := 0
	for _, execution := range runs {
		events, err := r.history(ctx, execution, &totalEvents, &totalBytes)
		if err != nil {
			return store.ReviewEvidence{}, err
		}
		if len(events) == 0 || events[0].GetWorkflowExecutionStartedEventAttributes().GetWorkflowType().GetName() != snapshot.WorkflowType {
			return store.ReviewEvidence{}, denied
		}
		schedules := map[int64]*historypb.ActivityTaskScheduledEventAttributes{}
		for _, event := range events {
			if attr := event.GetActivityTaskScheduledEventAttributes(); attr != nil {
				schedules[event.GetEventId()] = attr
				if execution.RunID == activityRun && attr.GetActivityId() == activityID {
					if found || (scheduledID != 0 && scheduledID != event.GetEventId()) || attr.GetActivityType().GetName() != CompletionActivityName {
						return store.ReviewEvidence{}, denied
					}
					var input CompletionInput
					if r.decodeOne(attr.GetInput(), &input) != nil || input.AssignmentID != a.ID || input.Runtime.WorkflowID != a.Binding.Reviewer.RootID || input.Runtime.StepID != a.CompletionNode || input.Runtime.NodePath != a.CompletionNode || !validVerdict(input.Result.Verdict) {
						return store.ReviewEvidence{}, denied
					}
					result = input.Result
					scheduled = event
					found = true
				}
			}
			if attr := event.GetActivityTaskCompletedEventAttributes(); attr != nil {
				schedule := schedules[attr.GetScheduledEventId()]
				if schedule == nil {
					return store.ReviewEvidence{}, denied
				}
				if schedule.GetActivityType().GetName() == "ActivityLoadWorkflow" {
					var input struct {
						WorkflowName string `json:"workflow_name"`
					}
					var output struct {
						WorkflowJSON []byte `json:"workflow_json"`
					}
					if r.decodeOne(schedule.GetInput(), &input) != nil || r.decodeOne(attr.GetResult(), &output) != nil {
						return store.ReviewEvidence{}, denied
					}
					expected, ok := snapshot.Graphs[input.WorkflowName]
					if !ok || !sameJSON(expected, output.WorkflowJSON) {
						return store.ReviewEvidence{}, denied
					}
					observed[input.WorkflowName] = true
				}
				if execution.RunID == activityRun && schedule.GetActivityId() == activityID {
					activityCompleted = true
				}
				if execution == chain.Current && schedule.GetActivityType().GetName() == "WorkflowStatus" {
					var input struct {
						WorkflowID       string `json:"workflow_id"`
						ParentWorkflowID string `json:"parent_workflow_id"`
						Status           string `json:"status"`
						Outcome          string `json:"outcome"`
					}
					if r.decodeOne(schedule.GetInput(), &input) != nil {
						return store.ReviewEvidence{}, denied
					}
					if input.WorkflowID == a.Binding.Reviewer.RootID && input.ParentWorkflowID == "" && input.Status == "completed" {
						terminalOutcome = input.Outcome
						terminalStatusCount++
					}
				}
			}
			if attr := event.GetWorkflowExecutionCompletedEventAttributes(); attr != nil && execution == chain.Current {
				var output struct {
					Outputs struct {
						ReviewVerdict reviewpublisher.Verdict `json:"review_verdict"`
					} `json:"Outputs"`
				}
				if r.decodeOne(attr.GetResult(), &output) != nil {
					return store.ReviewEvidence{}, denied
				}
				logicalResult.Verdict = output.Outputs.ReviewVerdict
			}
		}
	}
	if !found || len(observed) != len(snapshot.Graphs) {
		return store.ReviewEvidence{}, denied
	}
	sum := sha256.Sum256(append([]byte(activityRun+"\n"), mustEventBytes(scheduled)...))
	evidence := store.ReviewEvidence{AssignmentID: a.ID, Digest: "sha256:" + hex.EncodeToString(sum[:]), ActivityID: activityID, RunID: activityRun, Verdict: result.Verdict}
	if !terminal {
		return evidence, nil
	}
	if chain.Live {
		return evidence, nil
	}
	evidence.Terminal = true
	// The trusted graph must explicitly return outcome success. A failed sibling,
	// failed/terminated execution or missing output cannot acquire success merely
	// because the reserved completion activity ran earlier.
	evidence.GraphSucceeded = chain.Terminal == enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED && activityCompleted && terminalStatusCount == 1 && terminalOutcome == "success" && logicalResult.Verdict == result.Verdict
	return evidence, nil
}

func (r TemporalEvidence) history(ctx context.Context, e runcontinuity.Execution, count, size *int) ([]*historypb.HistoryEvent, error) {
	var events []*historypb.HistoryEvent
	var token []byte
	seen := map[string]bool{}
	last := int64(0)
	for page := 0; page < 64; page++ {
		response, err := r.Service.GetWorkflowExecutionHistory(ctx, &servicepb.GetWorkflowExecutionHistoryRequest{Namespace: r.Namespace, Execution: &commonpb.WorkflowExecution{WorkflowId: e.WorkflowID, RunId: e.RunID}, MaximumPageSize: 256, HistoryEventFilterType: enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT, NextPageToken: token})
		if err != nil {
			return nil, err
		}
		if response == nil {
			return nil, reviewpublisher.ErrUnavailable
		}
		*size += proto.Size(response)
		if *size > 16<<20 {
			return nil, reviewpublisher.ErrUnauthorized
		}
		for _, event := range response.GetHistory().GetEvents() {
			*count++
			if *count > 20000 || event.GetEventId() <= last {
				return nil, reviewpublisher.ErrUnauthorized
			}
			last = event.GetEventId()
			events = append(events, event)
		}
		token = response.GetNextPageToken()
		if len(token) == 0 {
			return events, nil
		}
		if seen[string(token)] {
			return nil, reviewpublisher.ErrUnauthorized
		}
		seen[string(token)] = true
	}
	return nil, reviewpublisher.ErrUnauthorized
}

func (r TemporalEvidence) decodeOne(p *commonpb.Payloads, out any) error {
	if len(p.GetPayloads()) != 1 || len(p.GetPayloads()[0].GetData()) > 1<<20 {
		return reviewpublisher.ErrUnauthorized
	}
	dc := r.Converter
	if dc == nil {
		dc = relianttemporal.NewFlexibleDataConverter()
	}
	if err := dc.FromPayloads(p, out); err != nil {
		return err
	}
	decoded, err := json.Marshal(out)
	if err != nil || len(decoded) > 2<<20 {
		return reviewpublisher.ErrUnauthorized
	}
	return nil
}
func sameJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && bytes.Equal(canonicalJSON(x), canonicalJSON(y))
}
func canonicalJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func mustEventBytes(e *historypb.HistoryEvent) []byte {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(e)
	return b
}
func validVerdict(v reviewpublisher.Verdict) bool {
	return v == reviewpublisher.Approve || v == reviewpublisher.Changes || v == reviewpublisher.Incomplete
}
