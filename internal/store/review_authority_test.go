package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

func reviewFixture(t *testing.T) (*storetest.Env, store.ReviewAuthorityStore, store.ReviewAuthority) {
	t.Helper()
	if testing.Short() {
		t.Skip("private SQL authority proof; run without -short")
	}
	env := storetest.New(t)
	ctx := context.Background()
	s := store.ReviewAuthorityStore{DB: env.ORM}
	a := prhandoff.Author{OwnerID: storetest.SeedUserID, RootID: "author-root", ExecutionID: "author-run", ChatID: "author-chat", WorktreeID: "worktree"}
	repo := prhandoff.Repository{ID: 42, Name: "acme/repo"}
	sha := strings.Repeat("a", 40)
	handoff := store.PRHandoffStore{DB: env.ORM}
	p := prhandoff.Publication{ID: "publication", Author: a, Repo: repo, Branch: "fix", HeadSHA: sha}
	require.NoError(t, handoff.RecordPublication(ctx, p))
	require.NoError(t, handoff.Enroll(ctx, prhandoff.Binding{ID: "binding", Author: a, PublicationID: p.ID, Pull: prhandoff.Pull{Repo: repo, Number: 7, ObjectID: 700, HeadRepoID: 42, Branch: "fix", HeadSHA: sha, Open: true}}))
	enqueueAdmission(t, env, "review-event", store.ReviewAdmissionScope(storetest.SeedUserID, storetest.SeedProjectID, "trusted"), "github:42:pr:7", sha, 10)
	require.NoError(t, reserveAdmission(env, "review-event", 1))
	root := "review-root"
	require.NoError(t, env.Repo.CreateChat(ctx, &db.Chat{ID: root, Title: "private review", ProjectID: storetest.SeedProjectID, UserID: storetest.SeedUserID, WorkflowID: &root}))
	require.NoError(t, env.Repo.CreateWorkflow(ctx, &db.Workflow{ID: root, ChatID: root, WorkflowName: "trusted", Thread: root, Status: db.Active(), OwnerUserID: &a.OwnerID}))
	trigger := newStoreTrigger("review-trigger", "review", time.Now())
	trigger.Workflow = "trusted"
	trigger.Kind = "integration"
	trigger.Config = json.RawMessage(`{"integration":"github","event":"pull_request.opened"}`)
	require.NoError(t, store.InsertTrigger(ctx, env.ORM, trigger))
	_, err := env.SQL.ExecContext(ctx, `UPDATE trigger_events SET trigger_id='review-trigger',admission_workflow_id='review-root',admission_run_id='review-run',payload='{"integration":"github","event":"pull_request.opened","resource":"42","data":{"pull_request":{"number":7}}}'::jsonb WHERE id='review-event'`)
	require.NoError(t, err)
	raw, _ := json.Marshal(map[string]any{"root_ref": "trusted", "admission_key": store.ReviewAdmissionKeyExpression, "graphs": map[string]any{"trusted": map[string]any{"nodes": []any{}, "concurrency": map[string]any{"key": store.ReviewAdmissionKeyExpression}}}, "completion_node": "finish", "completion_activity": "ActivityPrivateReviewCompletion", "output_schema": "review-verdict-v1"})
	digest, err := store.DigestReviewSnapshot(raw)
	require.NoError(t, err)
	require.NoError(t, s.ProvisionReviewPolicy(ctx, store.ReviewPolicyConfig{ProjectID: storetest.SeedProjectID, WorkflowRef: "trusted", ID: "policy", OwnerID: storetest.SeedUserID, RepositoryID: 42, Revision: 1, Enabled: true, WorkflowDigest: digest, CompletionNode: "finish", CheckName: "ReliantReview", AuthorizedBy: "fixture-admin", GraphSnapshot: raw}))
	assigned, err := s.AssignReview(ctx, store.ReviewAssignmentRequest{ID: "assignment", PolicyID: "policy", BindingID: "binding", AdmissionEventID: "review-event", ProviderHead: reviewpublisher.Head{RepositoryID: 42, PullNumber: 7, SHA: strings.Repeat("a", 40)}})
	require.NoError(t, err)
	return env, s, assigned
}

func TestReviewAuthoritySQLWrongAdmissionSubjectAndWorkflow(t *testing.T) {
	for _, tc := range []struct{ name, statement string }{
		{"wrong PR", `UPDATE trigger_events SET payload=jsonb_set(payload,'{data,pull_request,number}','8') WHERE id='review-event'`},
		{"wrong repository", `UPDATE trigger_events SET payload=jsonb_set(payload,'{resource}','"43"') WHERE id='review-event'`},
		{"wrong key", `UPDATE trigger_events SET admission_key='github:42:pr:8' WHERE id='review-event'`},
		{"wrong scope", `UPDATE trigger_events SET admission_scope='unrelated' WHERE id='review-event'`},
		{"wrong workflow", `UPDATE workflows SET workflow_name='author-controlled' WHERE id='review-root'`},
		{"wrong trigger source", `UPDATE triggers SET config='{"integration":"http"}'::jsonb WHERE id='review-trigger'`},
		{"generic webhook", `UPDATE trigger_events SET kind='webhook' WHERE id='review-event'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, s, _ := reviewFixture(t)
			ctx := context.Background()
			_, err := env.SQL.ExecContext(ctx, `DELETE FROM review_assignments`)
			require.NoError(t, err)
			_, err = env.SQL.ExecContext(ctx, tc.statement)
			require.NoError(t, err)
			_, err = s.AssignReview(ctx, store.ReviewAssignmentRequest{ID: "new", PolicyID: "policy", BindingID: "binding", AdmissionEventID: "review-event", ProviderHead: reviewpublisher.Head{RepositoryID: 42, PullNumber: 7, SHA: strings.Repeat("a", 40)}})
			require.ErrorIs(t, err, reviewpublisher.ErrUnauthorized)
		})
	}
}

func TestReviewAuthoritySQLIntentAndAtomicDenials(t *testing.T) {
	env, s, a := reviewFixture(t)
	ctx := context.Background()
	evidence := store.ReviewEvidence{AssignmentID: a.ID, Digest: "sha256:" + strings.Repeat("b", 64), ActivityID: "completion", RunID: "review-run", Verdict: reviewpublisher.Approve}
	require.NoError(t, s.RecordReviewEvidence(ctx, evidence))
	intent := reviewpublisher.Intent{AssignmentID: a.ID, Binding: a.Binding, CheckName: "ReliantReview", Conclusion: "success", Summary: "Review approved.", DetailsURL: "https://app.example.test/workflows/runs/review-root"}
	_, err := s.PrepareIntent(ctx, intent)
	require.ErrorIs(t, err, reviewpublisher.ErrUnauthorized, "pending activity is not terminal graph approval")
	evidence.Terminal = true
	evidence.GraphSucceeded = true
	require.NoError(t, s.RecordReviewEvidence(ctx, evidence))
	first, err := s.PrepareIntent(ctx, intent)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.PrepareIntent(ctx, intent)
			if err != nil {
				t.Error(err)
				return
			}
			if r.IntentID != first.IntentID || !r.Coalesced {
				t.Errorf("duplicate receipt %#v", r)
			}
		}()
	}
	wg.Wait()
	changed := intent
	changed.Summary = "rewritten"
	_, err = s.PrepareIntent(ctx, changed)
	require.ErrorIs(t, err, reviewpublisher.ErrUnauthorized)
	claimed, err := s.ClaimPublication(ctx, first.IntentID)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = s.ClaimPublication(ctx, first.IntentID)
	require.NoError(t, err)
	require.False(t, claimed)
	require.NoError(t, s.UncertainPublication(ctx, first.IntentID))
	claimed, err = s.ClaimPublication(ctx, first.IntentID)
	require.NoError(t, err)
	require.False(t, claimed, "uncertain must never reset create")
	for _, bad := range []store.ReviewCheckReceipt{
		{RepositoryID: 43, HeadSHA: a.Binding.HeadSHA, Name: "ReliantReview", ExternalID: store.ReviewExternalID(a, first.IntentID), CheckID: 901, CheckURL: "https://github.com/acme/repo/runs/901"},
		{RepositoryID: 42, HeadSHA: strings.Repeat("c", 40), Name: "ReliantReview", ExternalID: store.ReviewExternalID(a, first.IntentID), CheckID: 901, CheckURL: "https://github.com/acme/repo/runs/901"},
		{RepositoryID: 42, HeadSHA: a.Binding.HeadSHA, Name: "Other", ExternalID: store.ReviewExternalID(a, first.IntentID), CheckID: 901, CheckURL: "https://github.com/acme/repo/runs/901"},
		{RepositoryID: 42, HeadSHA: a.Binding.HeadSHA, Name: "ReliantReview", ExternalID: "another-intent", CheckID: 901, CheckURL: "https://github.com/acme/repo/runs/901"},
		{RepositoryID: 42, HeadSHA: a.Binding.HeadSHA, Name: "ReliantReview", ExternalID: store.ReviewExternalID(a, first.IntentID), CheckID: 901, CheckURL: "https://evil.test/runs/901"},
	} {
		require.ErrorIs(t, s.ConfirmPublication(ctx, first.IntentID, bad), reviewpublisher.ErrUnauthorized)
	}
	require.NoError(t, s.ConfirmPublication(ctx, first.IntentID, store.ReviewCheckReceipt{RepositoryID: 42, HeadSHA: a.Binding.HeadSHA, Name: "ReliantReview", ExternalID: store.ReviewExternalID(a, first.IntentID), CheckID: 901, CheckURL: "https://github.com/acme/repo/runs/901"}))
	require.NoError(t, s.ConfirmPublication(ctx, first.IntentID, store.ReviewCheckReceipt{RepositoryID: 42, HeadSHA: a.Binding.HeadSHA, Name: "ReliantReview", ExternalID: store.ReviewExternalID(a, first.IntentID), CheckID: 901, CheckURL: "https://github.com/acme/repo/runs/901"}))
	require.ErrorIs(t, s.ConfirmPublication(ctx, first.IntentID, store.ReviewCheckReceipt{RepositoryID: 42, HeadSHA: a.Binding.HeadSHA, Name: "ReliantReview", ExternalID: store.ReviewExternalID(a, first.IntentID), CheckID: 902, CheckURL: "https://github.com/acme/repo/runs/902"}), reviewpublisher.ErrUnauthorized)
	_, err = env.SQL.ExecContext(ctx, `UPDATE review_policies SET revision=2 WHERE id='policy'`)
	require.NoError(t, err)
	_, err = s.PrepareIntent(ctx, intent)
	require.ErrorIs(t, err, reviewpublisher.ErrUnauthorized)
}

func TestReviewAuthoritySQLCheckReceiptCannotBeReusedAcrossIntents(t *testing.T) {
	env, s, a := reviewFixture(t)
	ctx := context.Background()
	require.NoError(t, s.RecordReviewEvidence(ctx, store.ReviewEvidence{AssignmentID: a.ID, Digest: "sha256:" + strings.Repeat("b", 64), ActivityID: "finish", RunID: "review-run", Verdict: reviewpublisher.Approve, Terminal: true, GraphSucceeded: true}))
	first, err := s.PrepareIntent(ctx, reviewpublisher.Intent{AssignmentID: a.ID, Binding: a.Binding, CheckName: "ReliantReview", Conclusion: "success"})
	require.NoError(t, err)
	_, err = s.ClaimPublication(ctx, first.IntentID)
	require.NoError(t, err)
	receipt := store.ReviewCheckReceipt{RepositoryID: 42, HeadSHA: a.Binding.HeadSHA, Name: "ReliantReview", ExternalID: store.ReviewExternalID(a, first.IntentID), CheckID: 901, CheckURL: "https://github.com/acme/repo/runs/901"}
	require.NoError(t, s.ConfirmPublication(ctx, first.IntentID, receipt))
	// Copy fixture state into another assignment/intent to exercise the storage
	// uniqueness invariant; this is an adversarial DB fixture, not trusted flow.
	_, err = env.SQL.ExecContext(ctx, `INSERT INTO trigger_events (id,user_id,kind,dedupe_key,occurred_at,payload,outcome,outcome_detail) SELECT 'other-event',user_id,kind,'other',occurred_at,payload,outcome,outcome_detail FROM trigger_events WHERE id='review-event'`)
	require.NoError(t, err)
	_, err = env.SQL.ExecContext(ctx, `INSERT INTO review_assignments SELECT 'other-assignment',policy_id,binding_id,'other-event',owner_id,repository_id,pr_number,repeat('c',40),author_root_id,author_run_id,'other-reviewer','other-run',policy_revision,workflow_digest,graph_snapshot,completion_node,2,cancelled,superseded,completion_state,verdict,evidence_digest,completion_activity_id,completion_run_id,graph_succeeded,created_at,updated_at FROM review_assignments WHERE id='assignment'`)
	require.NoError(t, err)
	_, err = env.SQL.ExecContext(ctx, `INSERT INTO review_publication_intents(id,assignment_id,repository_id,payload,payload_digest,state,check_id,check_url) SELECT 'other-intent','other-assignment',repository_id,payload,payload_digest,'publishing',901,NULL FROM review_publication_intents WHERE id=$1`, first.IntentID)
	require.Error(t, err, "database must reject the same provider check identity for another intent")
}

func TestReviewAuthoritySQLDriftAndSpoofing(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"cancel", `UPDATE review_assignments SET cancelled=true WHERE id='assignment'`},
		{"new head", `UPDATE pr_handoff_bindings SET head_sha=repeat('c',40) WHERE id='binding'`},
		{"owner changed", `UPDATE trigger_events SET admission_run_id='other-run' WHERE id='review-event'`},
		{"superseded", `UPDATE trigger_events SET admission_state='superseded' WHERE id='review-event'`},
		{"disabled", `UPDATE review_policies SET enabled=false WHERE id='policy'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, s, a := reviewFixture(t)
			ctx := context.Background()
			_, err := env.SQL.ExecContext(ctx, tc.sql)
			require.NoError(t, err)
			err = s.RecordReviewEvidence(ctx, store.ReviewEvidence{AssignmentID: a.ID, Digest: "sha256:" + strings.Repeat("b", 64), ActivityID: "finish", RunID: "review-run", Verdict: reviewpublisher.Approve, Terminal: true, GraphSucceeded: true})
			require.ErrorIs(t, err, reviewpublisher.ErrUnauthorized)
		})
	}
}

func TestReviewAuthoritySQLConflictAndSameHeadCoalesces(t *testing.T) {
	_, s, a := reviewFixture(t)
	ctx := context.Background()
	same, err := s.AssignReview(ctx, store.ReviewAssignmentRequest{ID: "duplicate", PolicyID: "policy", BindingID: "binding", AdmissionEventID: "review-event", ProviderHead: reviewpublisher.Head{RepositoryID: 42, PullNumber: 7, SHA: strings.Repeat("a", 40)}})
	require.NoError(t, err)
	require.Equal(t, a.ID, same.ID)
	e := store.ReviewEvidence{AssignmentID: a.ID, Digest: "sha256:" + strings.Repeat("b", 64), ActivityID: "finish", RunID: "review-run", Verdict: reviewpublisher.Changes, Terminal: true}
	require.NoError(t, s.RecordReviewEvidence(ctx, e))
	require.NoError(t, s.RecordReviewEvidence(ctx, e))
	e.Verdict = reviewpublisher.Approve
	require.True(t, errors.Is(s.RecordReviewEvidence(ctx, e), reviewpublisher.ErrUnauthorized))
}
