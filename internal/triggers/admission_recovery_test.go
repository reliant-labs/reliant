package triggers

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"
)

type admissionTemporalProbe struct {
	WorkflowStarter
	desc        *workflowservice.DescribeWorkflowExecutionResponse
	err         error
	roots, runs []string
}

func (p *admissionTemporalProbe) DescribeWorkflowExecution(_ context.Context, root, run string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	p.roots = append(p.roots, root)
	p.runs = append(p.runs, run)
	return p.desc, p.err
}

func TestAdmissionRecoveryUsesTemporalNotCompletionCache(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	event := &core.TriggerEvent{ID: "recovery-event", UserID: storetest.SeedUserID, Kind: core.TriggerEventKindIntegration, DedupeKey: "recovery", OccurredAt: now, CreatedAt: now, Outcome: core.TriggerEventPending, Payload: map[string]any{}}
	if _, _, err := env.Repo.EnqueueTriggerAdmission(ctx, event, core.TriggerAdmissionRequest{Scope: "scope", Key: "pr", PendingLimit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error { return env.Repo.ReserveTriggerAdmission(ctx, event.ID, 1) }); err != nil {
		t.Fatal(err)
	}
	root := "recovery-root"
	if err := env.Repo.CreateChat(ctx, &db.Chat{ID: root, UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, Title: "recovery", WorkflowID: &root}); err != nil {
		t.Fatal(err)
	}
	// Completion activity is a DB projection, NOT execution closure authority.
	if err := env.Repo.CreateWorkflow(ctx, &db.Workflow{ID: root, ChatID: root, WorkflowName: "wf", Thread: root, Status: core.Completed()}); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.UpdateTriggerEventOutcome(ctx, event.ID, core.TriggerEventLaunched, "", &root); err != nil {
		t.Fatal(err)
	}
	if _, err := env.SQL.ExecContext(ctx, `UPDATE trigger_events SET run_status='completed' WHERE id=$1`, event.ID); err != nil {
		t.Fatal(err)
	}
	memo, err := converter.GetDefaultDataConverter().ToPayload(event.ID)
	if err != nil {
		t.Fatal(err)
	}
	probe := &admissionTemporalProbe{desc: &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: root, RunId: "original-run"}, Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, Memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{"reliant_admission_event_id": memo}}}}}
	redriver := NewRedriver(env.Repo, probe, "")
	if err = redriver.redriveAdmissions(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := env.Repo.GetTriggerAdmission(ctx, event.ID)
	if err != nil || a.State != "reserved" || a.RunID != "original-run" {
		t.Fatalf("cache completion released or memo binding lost: %+v %v", a, err)
	}
	// Wrap the keyset cursor, then prove exact run describes are used.
	if err = redriver.redriveAdmissions(ctx); err != nil {
		t.Fatal(err)
	}
	probe.err = serviceerror.NewNotFound("transient retention/visibility uncertainty")
	if err = redriver.redriveAdmissions(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ = env.Repo.GetTriggerAdmission(ctx, event.ID)
	if a.State != "reserved" {
		t.Fatal("NotFound released")
	}
	probe.err = nil
	probe.desc.WorkflowExecutionInfo.Status = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
	if err = redriver.redriveAdmissions(ctx); err != nil {
		t.Fatal(err)
	} // wrap
	if err = redriver.redriveAdmissions(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ = env.Repo.GetTriggerAdmission(ctx, event.ID)
	if a.State != "released" {
		t.Fatal("confirmed exact terminal did not release")
	}
	if probe.runs[len(probe.runs)-1] != "original-run" {
		t.Fatal("known run not described exactly")
	}
}
