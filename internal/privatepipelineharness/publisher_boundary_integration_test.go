//go:build integration

// Copyright (c) 2025 Reliant Labs
package privatepipelineharness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	_ "github.com/reliant-labs/reliant/internal/integrations/githubqueue"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/prhandoffadapter"
	"github.com/reliant-labs/reliant/internal/prreviewwait"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	adapter "github.com/reliant-labs/reliant/internal/reviewpublisheradapter"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
)

// External-provider credential fixture only; no review/intent/journal authority.
type providerCredential struct{}

func (providerCredential) ConnectionID() string      { return "private-github" }
func (providerCredential) Params() map[string]string { return nil }
func (providerCredential) Apply(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer ghp_futurechat_b_fixture_owner")
	return nil
}
func (providerCredential) Scrub(s string) string {
	return strings.ReplaceAll(s, "ghp_futurechat_b_fixture_owner", "[redacted]")
}

type providerCredentials struct{}

func (providerCredentials) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return providerCredential{}, nil
}

type cpAuthentication struct{ endpoint cpEndpoint }

func (a cpAuthentication) Apply(r *http.Request) error {
	r.Header.Set("X-Private-Fixture-Caller", "internal")
	r.Header.Set("X-Private-Fixture-Secret", a.endpoint.secret)
	return nil
}

// This boundary deliberately seeds TERMINAL SQL metadata as trusted fixture
// input. It is NOT a graph-completion/whole-pipeline proof. All source/intent/
// publication/wait-policy adapters, SQL stores, provider reads and CP HTTP are real.
func TestFutureChatBPublisherConfirmedInboxBoundary(t *testing.T) {
	runPublisherInbox(t, false, "approve", "success", false)
}

func TestFutureChatBActualReviewGraphToAuthorWait(t *testing.T) {
	runPublisherInbox(t, true, "approve", "success", false)
}

func TestFutureChatBChangesWakeSameAuthor(t *testing.T) {
	runPublisherInbox(t, true, "changes", "success", false)
}
func TestFutureChatBFailedGraphNeverApproves(t *testing.T) {
	runPublisherInbox(t, true, "approve", "failure", false)
}
func TestFutureChatBStaleHeadCannotPublish(t *testing.T) {
	runPublisherInbox(t, true, "approve", "success", true)
}

func TestFutureChatBRepairWithReviewerCAN(t *testing.T) {
	runPublisherInbox(t, true, "changes", "success", false, true)
}

func runPublisherInbox(t *testing.T, actualGraph bool, verdict, graphOutcome string, stale bool, canVariant ...bool) {
	forceCAN := len(canVariant) > 0 && canVariant[0]
	f := newResources(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	repo, err := db.NewRepoWithDriver(f.SQL, db.DriverPostgres)
	if err != nil {
		t.Fatal(err)
	}
	repairLoop := verdict == "changes" && actualGraph
	provider := &checkProvider{t: t}
	github := newGitHubBoundary(t, provider)
	endpoint := startCP(t, github)
	runner := httpaction.New(httpaction.Deps{})
	runner.Client().Transport = github
	cat := catalog.MustBuiltin()
	w := worker.New(f.Temporal, f.Queue, worker.Options{})
	w.RegisterWorkflow(continuationInfrastructure)
	w.RegisterActivityWithOptions(func(context.Context) error { return nil }, activity.RegisterOptions{Name: "ContinuationReady"})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	root := uuid.NewString()
	var authorRun client.WorkflowRun
	var authorWorker worker.Worker
	authorDefinition := "name: private-author\nentry: [wait]\nnodes:\n  - id: wait\n    type: await_pr_review\n    repo: acme/example\n    pr: 7\n    expected_head: " + strings.Repeat("a", 40) + "\noutputs:\n  review_state: \"{{ nodes.wait.state }}\"\n"
	worktreePath := t.TempDir()
	if repairLoop {
		authorDefinition = "name: private-author\nentry: [wait]\nnodes:\n  - id: wait\n    type: await_pr_review\n    repo: acme/example\n    pr: 7\n    expected_head: " + strings.Repeat("a", 40) + "\n  - id: repair\n    type: run\n    command: private-fixture-repair\n  - id: wait_repaired\n    type: await_pr_review\n    repo: acme/example\n    pr: 7\n    expected_head: \"{{ nodes.repair.head_sha }}\"\nedges:\n  - from: wait\n    default: repair\n  - from: repair\n    default: wait_repaired\noutputs:\n  review_state: \"{{ nodes.wait_repaired.state }}\"\n"
		if err := store.InsertWorktree(ctx, repo.ORM(), &core.Worktree{ID: "private-wt", Name: "private", Path: worktreePath, Branch: "fix", BaseBranch: "main", ProjectID: "test-project", CreatedAt: time.Now(), UpdatedAt: time.Now(), LastActive: time.Now()}); err != nil {
			t.Fatal(err)
		}
		remote := "https://github.com/acme/example.git"
		if err := store.InsertRepo(ctx, repo.ORM(), &core.Repo{ID: "private-repo", ProjectID: "test-project", Name: "example", RelativePath: "", RemoteURL: &remote, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if actualGraph {
		var wt *string
		if repairLoop {
			id := "private-wt"
			wt = &id
		}
		if err := repo.CreateChat(ctx, &db.Chat{ID: root, Title: "original author", UserID: endpoint.OwnerID, ProjectID: "test-project", WorkflowID: &root, WorktreeID: wt}); err != nil {
			t.Fatal(err)
		}
		authorQueue := f.Queue + "-author"
		authorRun, err = f.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: root, TaskQueue: authorQueue}, runtime.DynamicWorkflow, runtime.WorkflowInput{ChatID: root, WorkflowName: "private-author", Inputs: map[string]any{}, ExecContext: &runtime.ExecutionContext{ChatID: root, Thread: root, ThreadMode: "new", WorkflowName: "private-author"}})
		authorWorker = worker.New(f.Temporal, authorQueue, worker.Options{})
		registerSQLRuntime(t, authorWorker, repo, authorDefinition)
	} else {
		authorRun, err = f.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: root, TaskQueue: f.Queue}, continuationInfrastructure, true)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = f.Temporal.TerminateWorkflow(c, root, authorRun.GetRunID(), "owned boundary cleanup")
	})
	sha := strings.Repeat("a", 40)
	handoff := store.PRHandoffStore{DB: repo.ORM()}
	author := prhandoff.Author{OwnerID: endpoint.OwnerID, RootID: root, ExecutionID: authorRun.GetRunID(), ChatID: root, WorktreeID: "private-wt"}
	if err := handoff.RecordPublication(ctx, prhandoff.Publication{ID: "publication", Author: author, Repo: prhandoff.Repository{ID: 42, Name: "acme/example"}, Branch: "fix", HeadSHA: sha}); err != nil {
		t.Fatal(err)
	}
	if err := handoff.Enroll(ctx, prhandoff.Binding{ID: "binding", Author: author, PublicationID: "publication", Pull: prhandoff.Pull{Repo: prhandoff.Repository{ID: 42, Name: "acme/example"}, Number: 7, ObjectID: 700, HeadRepoID: 42, Branch: "fix", HeadSHA: sha, Open: true}}); err != nil {
		t.Fatal(err)
	}
	reviewer := "boundary-reviewer"
	if err := repo.CreateChat(ctx, &db.Chat{ID: reviewer, Title: "private", UserID: endpoint.OwnerID, ProjectID: "test-project", WorkflowID: &reviewer}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateWorkflow(ctx, &db.Workflow{ID: reviewer, ChatID: reviewer, WorkflowName: "trusted", Thread: reviewer, Status: db.Active(), OwnerUserID: &author.OwnerID}); err != nil {
		t.Fatal(err)
	}
	trigger := &core.Trigger{ID: "private-trigger", UserID: endpoint.OwnerID, ProjectID: "test-project", Name: "private", Kind: core.TriggerKind("integration"), Enabled: true, Workflow: "trusted", Config: json.RawMessage(`{"integration":"github"}`)}
	if err := store.InsertTrigger(ctx, repo.ORM(), trigger); err != nil {
		t.Fatal(err)
	}
	event := &core.TriggerEvent{ID: "private-event", TriggerID: &trigger.ID, UserID: endpoint.OwnerID, Kind: core.TriggerEventKind("integration"), DedupeKey: "private-event", OccurredAt: time.Now(), CreatedAt: time.Now(), Payload: map[string]any{"integration": "github", "event": "pull_request.opened", "resource": "42", "data": map[string]any{"pull_request": map[string]any{"number": 7}}}, Outcome: core.TriggerEventOutcome("pending")}
	if _, _, err := repo.EnqueueTriggerAdmission(ctx, event, core.TriggerAdmissionRequest{Scope: store.ReviewAdmissionScope(endpoint.OwnerID, "test-project", "trusted"), Key: "github:42:pr:7", Head: sha, PendingLimit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RunTx(ctx, orm.ReadCommitted, func(tx context.Context) error {
		if err := repo.ReserveTriggerAdmission(tx, event.ID, 1); err != nil {
			return err
		}
		return store.UpdateTriggerEventOutcome(tx, repo.ORM(), event.ID, core.TriggerEventOutcome("launched"), "private fixture", &reviewer)
	}); err != nil {
		t.Fatal(err)
	}
	state := store.ReviewAuthorityStore{DB: repo.ORM()}
	reviewRunID := "boundary-review-run"
	completionNode := "finish"
	var reviewerRun client.WorkflowRun
	var reviewerWorker worker.Worker
	var completion adapter.CompletionActivity
	snapshot, _ := json.Marshal(map[string]any{"root_ref": "trusted", "admission_key": store.ReviewAdmissionKeyExpression, "graphs": map[string]any{"trusted": map[string]any{"concurrency": map[string]any{"key": store.ReviewAdmissionKeyExpression}}}})
	if actualGraph {
		definition := "name: trusted\nconcurrency:\n  key: '" + store.ReviewAdmissionKeyExpression + "'\n  max_running: 1\nentry: [finish]\nnodes:\n  - id: finish\n    type: compute\n    outcome: " + graphOutcome + "\noutputs:\n  review_verdict: " + verdict + "\n"
		if repairLoop {
			definition = repairReviewerDefinition(forceCAN)
		}
		wf, err := wfyaml.ParseWorkflow([]byte(definition))
		if err != nil {
			t.Fatal(err)
		}
		graph, err := protojson.Marshal(wf)
		if err != nil {
			t.Fatal(err)
		}
		completionNode = adapter.ReviewCompletionPath
		snapshot, err = json.Marshal(adapter.GraphSnapshot{AdmissionKey: store.ReviewAdmissionKeyExpression, WorkflowType: "PrivateTrustedReviewer", RootRef: "trusted", Graphs: map[string]json.RawMessage{"trusted": graph}, YAML: map[string][]byte{"trusted": []byte(definition)}, CompletionNode: completionNode, CompletionActivity: adapter.CompletionActivityName, OutputSchema: adapter.CompletionSchema})
		if err != nil {
			t.Fatal(err)
		}
		reviewQueue := f.Queue + "-review"
		reviewerRun, err = f.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: reviewer, TaskQueue: reviewQueue}, "PrivateTrustedReviewer", runtime.WorkflowInput{ChatID: reviewer, WorkflowName: "trusted", Inputs: map[string]any{}, ExecContext: &runtime.ExecutionContext{ChatID: reviewer, Thread: reviewer, ThreadMode: "new", WorkflowName: "trusted"}})
		if err != nil {
			t.Fatal(err)
		}
		reviewRunID = reviewerRun.GetRunID()
		reviewerWorker = worker.New(f.Temporal, reviewQueue, worker.Options{})
		reviewerWorker.RegisterWorkflowWithOptions(adapter.PrivateReviewWorkflow, workflow.RegisterOptions{Name: "PrivateTrustedReviewer"})
		var canControl *reviewerCANControl
		reviewerWorker.RegisterActivityWithOptions(func(loadCtx context.Context, _ handlers.LoadWorkflowInput) (*handlers.LoadWorkflowOutput, error) {
			if canControl != nil {
				canControl.loaded(activity.GetInfo(loadCtx).WorkflowExecution.RunID)
			}
			return &handlers.LoadWorkflowOutput{YAML: []byte(definition), WorkflowJSON: graph, HasTemplates: strings.Contains(definition, "{{")}, nil
		}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
		reviewerWorker.RegisterActivityWithOptions(handlers.NewWorkflowStatusActivity(repo).Execute, activity.RegisterOptions{Name: "WorkflowStatus"})
		reviewerWorker.RegisterActivityWithOptions(handlers.NewWorkflowCheckpointActivity(repo).Execute, activity.RegisterOptions{Name: "WorkflowCheckpoint"})
		reviewerWorker.RegisterActivityWithOptions(handlers.NewCleanupActivity(repo).Execute, activity.RegisterOptions{Name: "Cleanup"})
		if repairLoop {
			if forceCAN {
				canControl = registerCANReviewModelBoundary(t, reviewerWorker, provider, reviewer, reviewRunID)
			} else {
				registerReviewModelBoundary(reviewerWorker, provider)
			}
		}
		completion = adapter.CompletionActivity{Store: state, History: adapter.TemporalEvidence{Service: f.Temporal.WorkflowService(), Namespace: f.Namespace}}
		reviewerWorker.RegisterActivityWithOptions(completion.Execute, activity.RegisterOptions{Name: adapter.CompletionActivityName})
		reviewerWorker.RegisterActivityWithOptions((adapter.ResolveAssignmentActivity{Store: state, History: adapter.TemporalEvidence{Service: f.Temporal.WorkflowService(), Namespace: f.Namespace}}).Execute, activity.RegisterOptions{Name: adapter.ResolveAssignmentActivityName})
	}
	if err := repo.BindTriggerAdmissionRun(ctx, event.ID, reviewRunID); err != nil {
		t.Fatal(err)
	}
	digest, err := store.DigestReviewSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.ProvisionReviewPolicy(ctx, store.ReviewPolicyConfig{ID: "policy", OwnerID: endpoint.OwnerID, ProjectID: "test-project", WorkflowRef: "trusted", RepositoryID: 42, Revision: 1, Enabled: true, WorkflowDigest: digest, GraphSnapshot: snapshot, CompletionNode: completionNode, CheckName: "ReliantReview", AuthorizedBy: "fixture-owner"}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"github/repo.get@1", "github/pr.get@1"} {
		resolved, err := cat.Resolve(ref)
		if err != nil {
			t.Fatal(err)
		}
		params := map[string]any{"owner": "acme", "repo": "example"}
		if ref == "github/pr.get@1" {
			params["pull_number"] = int64(7)
		}
		result, err := runner.RunAuthenticated(ctx, resolved.Manifest, resolved.Spec, params, providerCredentials{}, httpaction.CallSite{RunID: reviewer})
		if err != nil {
			t.Fatalf("actual catalog read %s: %v", ref, err)
		}
		if result == nil || result.IsError {
			t.Fatalf("actual catalog read %s: %#v", ref, result)
		}
	}
	heads := adapter.GitHub{OwnerID: endpoint.OwnerID, RepoName: "acme/example", Site: httpaction.CallSite{RunID: reviewer}, Catalog: cat, Runner: runner, Credentials: providerCredentials{}}
	a, err := (adapter.Assignment{Store: state, Heads: heads}).Assign(ctx, store.ReviewAssignmentRequest{ID: "assignment", PolicyID: "policy", BindingID: "binding", AdmissionEventID: event.ID})
	if err != nil {
		t.Fatal(err)
	}
	if actualGraph {
		if err := reviewerWorker.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reviewerWorker.Stop)
		var result runtime.WorkflowResult
		if err := reviewerRun.Get(ctx, &result); err != nil {
			t.Fatal(err)
		}
		if forceCAN {
			assertActualReviewCAN(t, ctx, f, reviewer, reviewRunID)
		}
		if err := completion.Reconcile(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
		a, err = state.LoadReviewAuthority(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !a.Completion.Final || a.Completion.GraphSucceeded != (graphOutcome == "success") || a.Completion.Verdict != reviewpublisher.Verdict(verdict) {
			t.Fatalf("actual graph history did not attest completion: %+v", a.Completion)
		}
	} else {
		// Boundary-only test's explicit fixture seed; full-graph path NEVER calls this.
		if err := state.RecordReviewEvidence(ctx, store.ReviewEvidence{AssignmentID: a.ID, Digest: "sha256:" + strings.Repeat("b", 64), ActivityID: "fixture-terminal", RunID: "boundary-review-run", Verdict: reviewpublisher.Approve, Terminal: true, GraphSucceeded: true}); err != nil {
			t.Fatal(err)
		}
	}
	coreService, err := reviewpublisher.New(reviewpublisher.Deps{Reviews: adapter.SQL{Store: state}, Heads: heads, Intents: adapter.SQL{Store: state}, RunOrigin: "https://app.reliant.test"})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := coreService.Prepare(ctx, reviewpublisher.PrepareInput{AssignmentID: a.ID, Binding: a.Binding})
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.externalID = store.ReviewExternalID(a, intent.IntentID)
	if verdict != "approve" || graphOutcome != "success" {
		provider.conclusion = "failure"
	}
	if stale {
		provider.head = strings.Repeat("b", 40)
	}
	provider.mu.Unlock()
	wait, err := prreviewwait.New(prreviewwait.Deps{Policy: adapter.WaitPolicy{Store: state}, Store: adapter.GuardedWaitStore{PRReviewWaitStore: store.PRReviewWaitStore{DB: repo.ORM()}}, Continuity: prreviewwait.ContinuityReader{Reader: runcontinuity.TemporalReader{Service: f.Temporal.WorkflowService(), Namespace: f.Namespace}}, Signals: prreviewwait.TemporalSignaler{Client: f.Temporal}})
	if err != nil {
		t.Fatal(err)
	}
	var sub prreviewwait.Subscription
	if actualGraph {
		providerReader := prhandoffadapter.GitHub{OwnerID: endpoint.OwnerID, Site: httpaction.CallSite{RunID: root}, Catalog: cat, Runner: runner, Credentials: providerCredentials{}}
		(prreviewwait.Activities{Service: wait, Identity: repo, GitHub: providerReader}).Register(authorWorker)
		if repairLoop {
			registerRepairBoundary(t, authorWorker, repo, f, provider, providerReader, handoff, root, authorRun.GetRunID(), worktreePath)
		}
		if err := authorWorker.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(authorWorker.Stop)
	} else {
		sub, err = wait.Arm(ctx, prreviewwait.Request{RequestKey: strings.Repeat("c", 64), OwnerID: endpoint.OwnerID, ChatID: root, Execution: runcontinuity.Execution{WorkflowID: root, RunID: authorRun.GetRunID()}, RepoID: 42, PR: 7, HeadSHA: sha})
		if err != nil {
			t.Fatal(err)
		}
		if err := wait.Ready(ctx, sub); err != nil {
			t.Fatal(err)
		}
	}
	bridge := adapter.BridgeHTTP{Origin: endpoint.URL, Client: &http.Client{Timeout: 10 * time.Second}, Auth: cpAuthentication{endpoint}}
	_, publishErr := (adapter.Publisher{Store: state, Heads: heads, Bridge: bridge}).Publish(ctx, intent.IntentID)
	if stale {
		if publishErr == nil {
			t.Fatal("stale head authorized check publication")
		}
		provider.mu.Lock()
		writes, queueWrites := provider.writes, provider.queueWrites
		provider.mu.Unlock()
		if writes != 0 || queueWrites != 0 {
			t.Fatal("stale head mutated provider")
		}
		return
	}
	if publishErr != nil {
		t.Fatal(publishErr)
	}
	if actualGraph {
		awaitArmedHead(t, ctx, f.SQL, sha)
	}
	deliveryErr := (adapter.WaitOutcome{Store: state, Wait: wait}).Deliver(ctx, intent.IntentID)
	if deliveryErr != nil {
		if graphOutcome == "success" || !errors.Is(deliveryErr, prreviewwait.ErrUnavailable) {
			t.Fatal(deliveryErr)
		}
		// Negative-only post-commit doorbell race: Publish.Put has already
		// committed. An author can consume the durable unavailable result and
		// finish before the subsequent exact-recipient history read. This is
		// NOT permission to ignore pre-publication or positive-path errors.
		assertConsumedFailureBeforeDoorbell(t, ctx, f, state, intent.IntentID, root, authorRun.GetRunID(), a.Binding.Generation)
	}
	if repairLoop {
		awaitArmedHead(t, ctx, f.SQL, strings.Repeat("b", 40))
		if _, err := (adapter.Publisher{Store: state, Heads: heads, Bridge: bridge}).Publish(ctx, intent.IntentID); err == nil {
			t.Fatal("old-head intent survived repaired publication")
		}
		a = runRepairedReview(t, ctx, f, repo, state, provider, heads, bridge, wait, endpoint.OwnerID, trigger, reviewer, reviewRunID, forceCAN)
		sha = strings.Repeat("b", 40)
	}
	if actualGraph {
		var authorResult runtime.WorkflowResult
		if err := authorRun.Get(ctx, &authorResult); err != nil {
			t.Fatal(err)
		}
		expectedState := "approved"
		if verdict == "changes" && !repairLoop {
			expectedState = "changes"
		} else if graphOutcome != "success" {
			expectedState = "unavailable"
		}
		if authorResult.Outputs["review_state"] != expectedState {
			t.Fatalf("real author wait did not consume approved result: %#v", authorResult.Outputs)
		}
		binding, err := handoff.Binding(ctx, endpoint.OwnerID, 42, 7)
		if err != nil {
			t.Fatal(err)
		}
		if binding.Author.RootID != root || binding.Author.ExecutionID != authorRun.GetRunID() {
			t.Fatal("original author was replaced")
		}
		var consumed int64
		if err := f.SQL.QueryRowContext(ctx, "SELECT consumed_sequence FROM pr_review_wait_subscriptions WHERE binding_id='binding' AND head_sha=$1", sha).Scan(&consumed); err != nil {
			t.Fatal(err)
		}
		if consumed != a.Binding.Generation {
			t.Fatalf("actual node consumed generation %d expected%d", consumed, a.Binding.Generation)
		}
		if expectedState != "approved" {
			provider.mu.Lock()
			queueWrites := provider.queueWrites
			provider.mu.Unlock()
			if queueWrites != 0 {
				t.Fatal("nonapproval enqueued")
			}
			return
		}
		queueAction, err := cat.Resolve("github/pr.enqueue@1")
		if err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			queued, err := runner.RunAuthenticated(ctx, queueAction.Manifest, queueAction.Spec, map[string]any{"owner": "acme", "repo": "example", "repository_id": int64(42), "pull_number": int64(7), "expectedHeadOid": sha}, providerCredentials{}, httpaction.CallSite{RunID: root, NodeID: "native-enqueue"})
			if err != nil {
				t.Fatal(err)
			}
			if queued == nil || queued.IsError {
				t.Fatalf("actual native enqueue failed: %#v", queued)
			}
		}
		provider.mu.Lock()
		queueWrites := provider.queueWrites
		provider.mu.Unlock()
		if queueWrites != 1 {
			t.Fatalf("native enqueue mutation count%d", queueWrites)
		}
	} else {
		outcome, err := wait.Read(ctx, sub)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.State != "approved" || outcome.AuthorityID != "policy" || outcome.HeadSHA != sha {
			t.Fatalf("wrong SQL-backed confirmed outcome: %+v", outcome)
		}
		if err := wait.Consume(ctx, sub, outcome); err != nil {
			t.Fatal(err)
		}
	}
	provider.mu.Lock()
	writes := provider.writes
	provider.mu.Unlock()
	wantWrites := 1
	if repairLoop {
		wantWrites = 2
	}
	if writes != wantWrites {
		t.Fatalf("check POST count %d", writes)
	}
}
