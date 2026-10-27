package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

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
	enqueueAdmission(t, env, "review-event", "private-review", "repo42/pr7", sha, 10)
	require.NoError(t, reserveAdmission(env, "review-event", 1))
	_, err := env.SQL.ExecContext(ctx, `UPDATE trigger_events SET admission_workflow_id='review-root',admission_run_id='review-run' WHERE id='review-event'`)
	require.NoError(t, err)
	raw := json.RawMessage(`{"root_ref":"trusted","graphs":{"trusted":{"nodes":[]}},"completion_node":"finish","completion_activity":"ActivityPrivateReviewCompletion","output_schema":"review-verdict-v1"}`)
	digest, err := store.DigestReviewSnapshot(raw)
	require.NoError(t, err)
	require.NoError(t, s.ProvisionReviewPolicy(ctx, store.ReviewPolicyConfig{ID: "policy", OwnerID: storetest.SeedUserID, RepositoryID: 42, Revision: 1, Enabled: true, WorkflowDigest: digest, CompletionNode: "finish", CheckName: "ReliantReview", AuthorizedBy: "fixture-admin", GraphSnapshot: raw}))
	assigned, err := s.AssignReview(ctx, store.ReviewAssignmentRequest{ID: "assignment", PolicyID: "policy", BindingID: "binding", AdmissionEventID: "review-event"})
	require.NoError(t, err)
	return env, s, assigned
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
	require.NoError(t, s.ConfirmPublication(ctx, first.IntentID, 901, "https://github.com/acme/repo/runs/901"))
	require.NoError(t, s.ConfirmPublication(ctx, first.IntentID, 901, "https://github.com/acme/repo/runs/901"))
	require.ErrorIs(t, s.ConfirmPublication(ctx, first.IntentID, 902, "https://github.com/acme/repo/runs/902"), reviewpublisher.ErrUnauthorized)
	_, err = env.SQL.ExecContext(ctx, `UPDATE review_policies SET revision=2 WHERE id='policy'`)
	require.NoError(t, err)
	_, err = s.PrepareIntent(ctx, intent)
	require.ErrorIs(t, err, reviewpublisher.ErrUnauthorized)
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
	same, err := s.AssignReview(ctx, store.ReviewAssignmentRequest{ID: "duplicate", PolicyID: "policy", BindingID: "binding", AdmissionEventID: "review-event"})
	require.NoError(t, err)
	require.Equal(t, a.ID, same.ID)
	e := store.ReviewEvidence{AssignmentID: a.ID, Digest: "sha256:" + strings.Repeat("b", 64), ActivityID: "finish", RunID: "review-run", Verdict: reviewpublisher.Changes, Terminal: true}
	require.NoError(t, s.RecordReviewEvidence(ctx, e))
	require.NoError(t, s.RecordReviewEvidence(ctx, e))
	e.Verdict = reviewpublisher.Approve
	require.True(t, errors.Is(s.RecordReviewEvidence(ctx, e), reviewpublisher.ErrUnauthorized))
}
