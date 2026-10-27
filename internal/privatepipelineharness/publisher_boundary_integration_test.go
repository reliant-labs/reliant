//go:build integration

// Copyright (c) 2025 Reliant Labs
package privatepipelineharness

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/prreviewwait"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	adapter "github.com/reliant-labs/reliant/internal/reviewpublisheradapter"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
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
	f := newResources(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	repo, err := db.NewRepoWithDriver(f.SQL, db.DriverPostgres)
	if err != nil {
		t.Fatal(err)
	}
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
	authorRun, err := f.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: root, TaskQueue: f.Queue}, continuationInfrastructure, true)
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
	if err := repo.BindTriggerAdmissionRun(ctx, event.ID, "boundary-review-run"); err != nil {
		t.Fatal(err)
	}
	state := store.ReviewAuthorityStore{DB: repo.ORM()}
	snapshot, _ := json.Marshal(map[string]any{"root_ref": "trusted", "admission_key": store.ReviewAdmissionKeyExpression, "graphs": map[string]any{"trusted": map[string]any{"concurrency": map[string]any{"key": store.ReviewAdmissionKeyExpression}}}})
	digest, err := store.DigestReviewSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.ProvisionReviewPolicy(ctx, store.ReviewPolicyConfig{ID: "policy", OwnerID: endpoint.OwnerID, ProjectID: "test-project", WorkflowRef: "trusted", RepositoryID: 42, Revision: 1, Enabled: true, WorkflowDigest: digest, GraphSnapshot: snapshot, CompletionNode: "finish", CheckName: "ReliantReview", AuthorizedBy: "fixture-owner"}); err != nil {
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
	// Explicit fixture seed, not an attestation from a model or fake ReviewSource.
	if err := state.RecordReviewEvidence(ctx, store.ReviewEvidence{AssignmentID: a.ID, Digest: "sha256:" + strings.Repeat("b", 64), ActivityID: "fixture-terminal", RunID: "boundary-review-run", Verdict: reviewpublisher.Approve, Terminal: true, GraphSucceeded: true}); err != nil {
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
	provider.mu.Lock()
	provider.externalID = store.ReviewExternalID(a, intent.IntentID)
	provider.mu.Unlock()
	wait, err := prreviewwait.New(prreviewwait.Deps{Policy: adapter.WaitPolicy{Store: state}, Store: store.PRReviewWaitStore{DB: repo.ORM()}, Continuity: prreviewwait.ContinuityReader{Reader: runcontinuity.TemporalReader{Service: f.Temporal.WorkflowService(), Namespace: f.Namespace}}, Signals: prreviewwait.TemporalSignaler{Client: f.Temporal}})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := wait.Arm(ctx, prreviewwait.Request{RequestKey: strings.Repeat("c", 64), OwnerID: endpoint.OwnerID, ChatID: root, Execution: runcontinuity.Execution{WorkflowID: root, RunID: authorRun.GetRunID()}, RepoID: 42, PR: 7, HeadSHA: sha})
	if err != nil {
		t.Fatal(err)
	}
	if err := wait.Ready(ctx, sub); err != nil {
		t.Fatal(err)
	}
	bridge := adapter.BridgeHTTP{Origin: endpoint.URL, Client: &http.Client{Timeout: 10 * time.Second}, Auth: cpAuthentication{endpoint}}
	if _, err := (adapter.Publisher{Store: state, Heads: heads, Bridge: bridge}).Publish(ctx, intent.IntentID); err != nil {
		t.Fatal(err)
	}
	if err := (adapter.WaitOutcome{Store: state, Wait: wait}).Deliver(ctx, intent.IntentID); err != nil {
		t.Fatal(err)
	}
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
	provider.mu.Lock()
	writes := provider.writes
	provider.mu.Unlock()
	if writes != 1 {
		t.Fatalf("check POST count %d", writes)
	}
}
