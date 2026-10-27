package triggers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
)

func reviewAdmissionEvent(t *testing.T, env *storetest.Env, id, key string, trigger *string) *core.TriggerEvent {
	t.Helper()
	now := time.Now().UTC()
	event, _, err := env.Repo.EnqueueTriggerAdmission(context.Background(), &core.TriggerEvent{ID: id, UserID: storetest.SeedUserID, TriggerID: trigger, Kind: core.TriggerEventKindIntegration, DedupeKey: id, OccurredAt: now, CreatedAt: now, Outcome: core.TriggerEventPending, Payload: map[string]any{}}, core.TriggerAdmissionRequest{Scope: "review-scope", Key: key, PendingLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func reviewReserve(t *testing.T, env *storetest.Env, id string) {
	t.Helper()
	if err := env.Repo.RunTx(context.Background(), orm.ReadCommitted, func(ctx context.Context) error { return env.Repo.ReserveTriggerAdmission(ctx, id, 1) }); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionDeletedActivationQueuedHeadSettles(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	trigger := &core.Trigger{ID: "deleted-activation", UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, Name: "deleted", Kind: core.TriggerKindWebhook, Enabled: true, Workflow: "review", NoMachine: true, CreatedAt: now, UpdatedAt: now}
	if err := env.Repo.CreateTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	head := reviewAdmissionEvent(t, env, "orphan-head", "pr1", &trigger.ID)
	replacement := reviewAdmissionEvent(t, env, "replacement-activation", "pr2", nil)
	if _, err := env.SQL.ExecContext(ctx, `DELETE FROM triggers WHERE id=$1`, trigger.ID); err != nil {
		t.Fatal(err)
	}
	before, err := env.Repo.GetAdmissionEvent(ctx, head.ID)
	if err != nil || before.TriggerID != nil {
		t.Fatalf("FK did not clear: %+v %v", before, err)
	}
	a, err := env.Repo.GetTriggerAdmission(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRedriver(env.Repo, &admissionTemporalProbe{}, "")
	if err = r.startAdmissionFire(ctx, env.Repo, *a); err != nil {
		t.Fatal(err)
	}
	after, _ := env.Repo.GetAdmissionEvent(ctx, head.ID)
	a, _ = env.Repo.GetTriggerAdmission(ctx, head.ID)
	if after.Outcome != core.TriggerEventFailed || a.State == "queued" {
		t.Fatalf("orphan still owns FIFO: %+v %+v", after, a)
	}
	reviewReserve(t, env, replacement.ID)
	// A stale queued snapshot must not settle a reservation that won after
	// the snapshot, even while the activation/root metadata is now absent.
	if err := env.Repo.SettleOrphanQueuedAdmission(ctx, replacement.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := env.Repo.GetTriggerAdmission(ctx, replacement.ID)
	if current.State != "reserved" {
		t.Fatal("orphan settlement released a reservation")
	}
}

func TestAdmissionHardDeletedChatStillReleasesExactTemporalRoot(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	ev := reviewAdmissionEvent(t, env, "deleted-chat-event", "pr", nil)
	reviewReserve(t, env, ev.ID)
	root, run := "immutable-original-root", "original-run"
	if err := env.Repo.CreateChat(ctx, &db.Chat{ID: root, Title: "review", ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID}); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.UpdateTriggerEventOutcome(ctx, ev.ID, core.TriggerEventLaunched, "", &root); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.BindTriggerAdmissionRun(ctx, ev.ID, run); err != nil {
		t.Fatal(err)
	}
	if _, err := env.SQL.ExecContext(ctx, `DELETE FROM chats WHERE id=$1`, root); err != nil {
		t.Fatal(err)
	}
	event, _ := env.Repo.GetAdmissionEvent(ctx, ev.ID)
	if event.ChatID != nil {
		t.Fatal("hard delete did not clear chat FK")
	}
	a, _ := env.Repo.GetTriggerAdmission(ctx, ev.ID)
	if a.ChatID != root {
		t.Fatal("immutable Temporal root lost with chat")
	}
	probe := &admissionTemporalProbe{desc: &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: root, RunId: run}, Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING}}}
	r := NewRedriver(env.Repo, probe, "").WithContinuity(singleAdmissionHistory{probe})
	if err := r.redriveAdmissions(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ = env.Repo.GetTriggerAdmission(ctx, ev.ID)
	if a.State != "reserved" {
		t.Fatal("hard delete freed live run")
	}
	probe.desc.WorkflowExecutionInfo.Status = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
	for i := 0; i < 2; i++ {
		if err := r.redriveAdmissions(ctx); err != nil {
			t.Fatal(err)
		}
	}
	a, _ = env.Repo.GetTriggerAdmission(ctx, ev.ID)
	if a.State != "released" {
		t.Fatal("deleted chat leaked permit")
	}
	for i := range probe.roots {
		if probe.roots[i] != root || probe.runs[i] != run {
			t.Fatal("wrong execution described")
		}
	}
}

func TestAdmissionHighWaterSweepRevisitsOldRowsUnderContinuousIntake(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	reserved := reviewAdmissionEvent(t, env, "old-reserved", "reserved", nil)
	reviewReserve(t, env, reserved.ID)
	root, run := "high-water-root", "run"
	if err := env.Repo.CreateChat(ctx, &db.Chat{ID: root, Title: "review", ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID}); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.UpdateTriggerEventOutcome(ctx, reserved.ID, core.TriggerEventLaunched, "", &root); err != nil {
		t.Fatal(err)
	}
	if err := env.Repo.BindTriggerAdmissionRun(ctx, reserved.ID, run); err != nil {
		t.Fatal(err)
	}
	oldQueued := reviewAdmissionEvent(t, env, "old-queued", "queued", nil)
	for i := 0; i < 4; i++ {
		reviewAdmissionEvent(t, env, fmt.Sprint("initial", i), fmt.Sprint("initial", i), nil)
	}
	probe := &admissionTemporalProbe{desc: &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: root, RunId: run}, Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING}}}
	r := NewRedriver(env.Repo, probe, "").WithContinuity(singleAdmissionHistory{probe})
	r.admissionBatch = 1
	if err := r.redriveAdmissions(ctx); err != nil {
		t.Fatal(err)
	}
	// Replace after its age was passed: replacement keeps the old ordinal.
	replacement := reviewAdmissionEvent(t, env, "newest-queued-head", "queued", nil)
	prior, _ := env.Repo.GetTriggerAdmission(ctx, oldQueued.ID)
	current, _ := env.Repo.GetTriggerAdmission(ctx, replacement.ID)
	if prior.Order != current.Order {
		t.Fatal("replacement did not preserve FIFO age")
	}
	probe.desc.WorkflowExecutionInfo.Status = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED
	for pass := 0; pass < 16; pass++ {
		reviewAdmissionEvent(t, env, fmt.Sprint("continuous", pass), fmt.Sprint("continuous", pass), nil)
		if err := r.redriveAdmissions(ctx); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := env.Repo.GetTriggerAdmission(ctx, reserved.ID)
	if a.State != "released" {
		t.Fatal("continuous new ordinals starved old terminal reservation")
	}
	event, _ := env.Repo.GetAdmissionEvent(ctx, replacement.ID)
	if event.Outcome != core.TriggerEventFailed {
		t.Fatal("old queued row was never serviced")
	}
	if len(probe.roots) < 2 {
		t.Fatal("sweep did not wrap")
	}
}
