//go:build integration

package privatepipelineharness

import (
	"context"
	"database/sql"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/prhandoffadapter"
	"github.com/reliant-labs/reliant/internal/prreviewwait"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	adapter "github.com/reliant-labs/reliant/internal/reviewpublisheradapter"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
	"strings"
	"testing"
	"time"
)

func awaitArmedHead(t *testing.T, ctx context.Context, raw *sql.DB, sha string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state string
		err := raw.QueryRowContext(ctx, "SELECT state FROM pr_review_wait_subscriptions WHERE binding_id='binding' AND head_sha=$1", sha).Scan(&state)
		if err == nil && state == "armed" {
			return
		}
		if err != nil && err != sql.ErrNoRows {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("author did not park on exact-head subscription")
		case <-ticker.C:
		}
	}
}

func repairReviewerDefinition() string {
	return "name: trusted\nconcurrency:\n  key: '" + store.ReviewAdmissionKeyExpression + "'\n  max_running: 1\nentry: [review]\nnodes:\n  - id: review\n    type: call_llm\n    outcome: success\noutputs:\n  review_verdict: \"{{ nodes.review.response_text }}\"\n"
}
func registerReviewModelBoundary(w worker.Worker, p *checkProvider) {
	w.RegisterActivityWithOptions(func(context.Context, types.ActivityInput) (map[string]any, error) {
		p.mu.Lock()
		head := p.head
		p.mu.Unlock()
		verdict := "changes"
		if head == strings.Repeat("b", 40) {
			verdict = "approve"
		}
		return map[string]any{"response_text": verdict, "tool_calls": nil}, nil
	}, activity.RegisterOptions{Name: "CallLLM"})
	// External machine availability is outside this SQL/review fixture, no shell executes.
	w.RegisterActivityWithOptions(func(context.Context, map[string]any) (map[string]any, error) {
		return map[string]any{"daemon_available": true, "daemon_id": "private-fixture"}, nil
	}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
}

type executionDescriptions struct{ client.Client }

func (a executionDescriptions) DescribeWorkflowExecution(ctx context.Context, id, run string) (*client.WorkflowExecutionDescription, error) {
	return a.Client.DescribeWorkflow(ctx, id, run)
}

func registerRepairBoundary(t *testing.T, w worker.Worker, repo *db.Repo, f *resources, p *checkProvider, github prhandoffadapter.GitHub, handoff store.PRHandoffStore, root, runID, path string) {
	t.Helper()
	// External daemon availability is a declared fixture boundary: the Run
	// replacement below executes no shell, but real runtime still preflights it.
	w.RegisterActivityWithOptions(func(context.Context, map[string]any) (map[string]any, error) {
		return map[string]any{"daemon_available": true, "daemon_id": "private-fixture"}, nil
	}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
	authors := prhandoffadapter.Authors{Store: repo, Temporal: executionDescriptions{f.Temporal}}
	initial, err := prhandoff.New(prhandoff.Deps{Store: handoff, Authors: authors, GitHub: github})
	if err != nil {
		t.Fatal(err)
	}
	publisher := prhandoffadapter.ContinuationPublisher{Store: handoff, Bindings: handoff, Authors: authors, GitHub: github, Continuity: prreviewwait.ContinuityReader{Reader: runcontinuity.TemporalReader{Service: f.Temporal.WorkflowService(), Namespace: f.Namespace}}, Initial: initial}
	w.RegisterActivityWithOptions(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		info := activity.GetInfo(ctx)
		if info.WorkflowExecution.ID != root || info.WorkflowExecution.RunID != runID || in["command"] != "private-fixture-repair" {
			return nil, prhandoff.ErrRefused
		}
		var state string
		var consumed int64
		if err := f.SQL.QueryRowContext(ctx, "SELECT o.state,s.consumed_sequence FROM pr_review_wait_subscriptions s JOIN pr_review_wait_outcomes o ON o.binding_id=s.binding_id AND o.head_sha=s.head_sha WHERE s.binding_id='binding' AND s.head_sha=$1", strings.Repeat("a", 40)).Scan(&state, &consumed); err != nil {
			return nil, err
		}
		if state != "changes" || consumed != 1 {
			return nil, prhandoff.ErrRefused
		}
		p.mu.Lock()
		if p.queueWrites != 0 {
			p.mu.Unlock()
			return nil, prhandoff.ErrRefused
		}
		p.head = strings.Repeat("b", 40)
		p.mu.Unlock()
		site, err := prhandoffadapter.ActivitySite(ctx, root, root)
		if err != nil {
			return nil, err
		}
		err = publisher.Published(ctx, prhandoff.PublishInput{Site: site, Repository: "acme/example", WorktreePath: path, Branch: "fix", HeadSHA: strings.Repeat("b", 40), PreviousHeadSHA: strings.Repeat("a", 40)})
		if err != nil {
			return nil, err
		}
		return map[string]any{"head_sha": strings.Repeat("b", 40), "exit_code": 0, "stdout": ""}, nil
	}, activity.RegisterOptions{Name: "ExecuteRunStep"})
}

func runRepairedReview(t *testing.T, ctx context.Context, f *resources, repo *db.Repo, state store.ReviewAuthorityStore, p *checkProvider, heads adapter.GitHub, bridge adapter.BridgeHTTP, wait prreviewwait.Service, owner string, trigger *core.Trigger, oldReviewer, oldRun string) store.ReviewAuthority {
	t.Helper()
	if _, err := repo.ReleaseTriggerAdmission(ctx, "private-event", oldReviewer, oldRun); err != nil {
		t.Fatal(err)
	}
	root := "review-repaired"
	if err := repo.CreateChat(ctx, &db.Chat{ID: root, Title: "repair review", UserID: owner, ProjectID: "test-project", WorkflowID: &root}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateWorkflow(ctx, &db.Workflow{ID: root, ChatID: root, WorkflowName: "trusted", Thread: root, Status: db.Active(), OwnerUserID: &owner}); err != nil {
		t.Fatal(err)
	}
	ev := &core.TriggerEvent{ID: "repaired-event", TriggerID: &trigger.ID, UserID: owner, Kind: "integration", DedupeKey: "repaired-event", OccurredAt: time.Now(), CreatedAt: time.Now(), Outcome: "pending", Payload: map[string]any{"integration": "github", "event": "pull_request.synchronize", "resource": "42", "data": map[string]any{"pull_request": map[string]any{"number": 7}}}}
	if _, _, err := repo.EnqueueTriggerAdmission(ctx, ev, core.TriggerAdmissionRequest{Scope: store.ReviewAdmissionScope(owner, "test-project", "trusted"), Key: "github:42:pr:7", Head: strings.Repeat("b", 40), PendingLimit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RunTx(ctx, orm.ReadCommitted, func(tx context.Context) error {
		if err := repo.ReserveTriggerAdmission(tx, ev.ID, 1); err != nil {
			return err
		}
		return store.UpdateTriggerEventOutcome(tx, repo.ORM(), ev.ID, "launched", "private", &root)
	}); err != nil {
		t.Fatal(err)
	}
	queue := f.Queue + "-review-b"
	run, err := f.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: root, TaskQueue: queue}, "PrivateTrustedReviewer", runtime.WorkflowInput{ChatID: root, WorkflowName: "trusted", Inputs: map[string]any{}, ExecContext: &runtime.ExecutionContext{ChatID: root, Thread: root, ThreadMode: "new", WorkflowName: "trusted"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.BindTriggerAdmissionRun(ctx, ev.ID, run.GetRunID()); err != nil {
		t.Fatal(err)
	}
	heads.Site.RunID = root
	a, err := (adapter.Assignment{Store: state, Heads: heads}).Assign(ctx, store.ReviewAssignmentRequest{ID: "repaired-assignment", PolicyID: "policy", BindingID: "binding", AdmissionEventID: ev.ID})
	if err != nil {
		t.Fatal(err)
	}
	definition := repairReviewerDefinition()
	wf, err := wfyaml.ParseWorkflow([]byte(definition))
	if err != nil {
		t.Fatal(err)
	}
	graph, err := protojson.Marshal(wf)
	if err != nil {
		t.Fatal(err)
	}
	w := worker.New(f.Temporal, queue, worker.Options{})
	w.RegisterWorkflowWithOptions(adapter.PrivateReviewWorkflow, workflow.RegisterOptions{Name: "PrivateTrustedReviewer"})
	w.RegisterActivityWithOptions(func(context.Context, handlers.LoadWorkflowInput) (*handlers.LoadWorkflowOutput, error) {
		return &handlers.LoadWorkflowOutput{YAML: []byte(definition), WorkflowJSON: graph, HasTemplates: strings.Contains(definition, "{{")}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	w.RegisterActivityWithOptions(handlers.NewWorkflowStatusActivity(repo).Execute, activity.RegisterOptions{Name: "WorkflowStatus"})
	w.RegisterActivityWithOptions(handlers.NewWorkflowCheckpointActivity(repo).Execute, activity.RegisterOptions{Name: "WorkflowCheckpoint"})
	w.RegisterActivityWithOptions(handlers.NewCleanupActivity(repo).Execute, activity.RegisterOptions{Name: "Cleanup"})
	registerReviewModelBoundary(w, p)
	history := adapter.TemporalEvidence{Service: f.Temporal.WorkflowService(), Namespace: f.Namespace}
	completion := adapter.CompletionActivity{Store: state, History: history}
	w.RegisterActivityWithOptions(completion.Execute, activity.RegisterOptions{Name: adapter.CompletionActivityName})
	w.RegisterActivityWithOptions((adapter.ResolveAssignmentActivity{Store: state, History: history}).Execute, activity.RegisterOptions{Name: adapter.ResolveAssignmentActivityName})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	var result runtime.WorkflowResult
	if err := run.Get(ctx, &result); err != nil {
		t.Fatal(err)
	}
	if err := completion.Reconcile(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	coreService, err := reviewpublisher.New(reviewpublisher.Deps{Reviews: adapter.SQL{Store: state}, Heads: heads, Intents: adapter.SQL{Store: state}, RunOrigin: "https://app.reliant.test"})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := coreService.Prepare(ctx, reviewpublisher.PrepareInput{AssignmentID: a.ID, Binding: a.Binding})
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.externalID = store.ReviewExternalID(a, intent.IntentID)
	p.conclusion = "success"
	p.mu.Unlock()
	if _, err := (adapter.Publisher{Store: state, Heads: heads, Bridge: bridge}).Publish(ctx, intent.IntentID); err != nil {
		t.Fatal(err)
	}
	if err := (adapter.WaitOutcome{Store: state, Wait: wait}).Deliver(ctx, intent.IntentID); err != nil {
		t.Fatal(err)
	}
	return a
}
