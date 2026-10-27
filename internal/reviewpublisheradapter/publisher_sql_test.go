package reviewpublisheradapter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

type fixedHead struct{ head reviewpublisher.Head }

func (h fixedHead) CurrentHead(context.Context, int64, int64) (reviewpublisher.Head, error) {
	return h.head, nil
}

type receiptBridge struct {
	creates, patches, reconciles int
	patchErr                     error
	last                         CheckRequest
}

func (b *receiptBridge) Publish(_ context.Context, in CheckRequest) (CheckReceipt, error) {
	b.last = in
	if in.CheckID == 0 {
		b.creates++
	} else {
		b.patches++
		if b.patchErr != nil {
			return CheckReceipt{}, b.patchErr
		}
	}
	return CheckReceipt{ID: 901, URL: "https://github.com/acme/repo/runs/901"}, nil
}
func (b *receiptBridge) Reconcile(context.Context, CheckRequest) (CheckReceipt, error) {
	b.reconciles++
	return CheckReceipt{ID: 901, URL: "https://github.com/acme/repo/runs/901"}, nil
}

// SQL fixture proves publisher state transitions, not completion provenance;
// exact-history tests exercise the trusted receiver separately.
func publisherFixture(t *testing.T, verdict reviewpublisher.Verdict) (store.ReviewAuthorityStore, string, fixedHead) {
	t.Helper()
	if testing.Short() {
		t.Skip("private PostgreSQL publisher state proof; run without -short")
	}
	e := storetest.New(t)
	ctx := context.Background()
	s := store.ReviewAuthorityStore{DB: e.ORM}
	sha := strings.Repeat("a", 40)
	author := prhandoff.Author{OwnerID: storetest.SeedUserID, RootID: "author", ExecutionID: "author-run", ChatID: "author-chat", WorktreeID: "wt"}
	repo := prhandoff.Repository{ID: 42, Name: "acme/repo"}
	h := store.PRHandoffStore{DB: e.ORM}
	require.NoError(t, h.RecordPublication(ctx, prhandoff.Publication{ID: "pub", Author: author, Repo: repo, Branch: "fix", HeadSHA: sha}))
	require.NoError(t, h.Enroll(ctx, prhandoff.Binding{ID: "binding", Author: author, PublicationID: "pub", Pull: prhandoff.Pull{Repo: repo, Number: 7, ObjectID: 70, HeadRepoID: 42, Branch: "fix", HeadSHA: sha, Open: true}}))
	root := "review-root"
	require.NoError(t, e.Repo.CreateChat(ctx, &db.Chat{ID: root, Title: "private", UserID: storetest.SeedUserID, ProjectID: storetest.SeedProjectID, WorkflowID: &root}))
	require.NoError(t, e.Repo.CreateWorkflow(ctx, &db.Workflow{ID: root, ChatID: root, WorkflowName: "trusted", Thread: root, Status: db.Active(), OwnerUserID: &author.OwnerID}))
	raw, _ := json.Marshal(map[string]any{"root_ref": "trusted", "admission_key": store.ReviewAdmissionKeyExpression, "graphs": map[string]any{"trusted": map[string]any{"concurrency": map[string]any{"key": store.ReviewAdmissionKeyExpression}}}})
	digest, err := store.DigestReviewSnapshot(raw)
	require.NoError(t, err)
	require.NoError(t, s.ProvisionReviewPolicy(ctx, store.ReviewPolicyConfig{ID: "policy", OwnerID: author.OwnerID, ProjectID: storetest.SeedProjectID, WorkflowRef: "trusted", RepositoryID: 42, Revision: 1, Enabled: true, WorkflowDigest: digest, GraphSnapshot: raw, CompletionNode: "finish", CheckName: "ReliantReview", AuthorizedBy: "admin-fixture"}))
	_, err = e.SQL.ExecContext(ctx, `INSERT INTO triggers(id,user_id,project_id,name,kind,enabled,workflow,presets,params,message,config,filter,daemon_id,created_at,updated_at) VALUES('trigger',$1,$2,'private','integration',true,'trusted','{}','{}','','{"integration":"github"}','true','daemon',now(),now())`, author.OwnerID, storetest.SeedProjectID)
	require.NoError(t, err)
	_, err = e.SQL.ExecContext(ctx, `INSERT INTO trigger_events(id,trigger_id,user_id,kind,dedupe_key,occurred_at,created_at,payload,outcome,outcome_detail,admission_scope,admission_key,admission_head,admission_state,admission_workflow_id,admission_run_id) VALUES('event','trigger',$1,'integration','event',now(),now(),'{"integration":"github","event":"pull_request.opened","resource":"42","data":{"pull_request":{"number":7}}}','pending','',$2,'github:42:pr:7',$3,'reserved','review-root','review-run')`, author.OwnerID, store.ReviewAdmissionScope(author.OwnerID, storetest.SeedProjectID, "trusted"), sha)
	require.NoError(t, err)
	head := reviewpublisher.Head{RepositoryID: 42, PullNumber: 7, SHA: sha}
	a, err := s.AssignReview(ctx, store.ReviewAssignmentRequest{ID: "assignment", PolicyID: "policy", BindingID: "binding", AdmissionEventID: "event", ProviderHead: head})
	require.NoError(t, err)
	require.NoError(t, s.RecordReviewEvidence(ctx, store.ReviewEvidence{AssignmentID: a.ID, Digest: "sha256:" + strings.Repeat("b", 64), ActivityID: "finish", RunID: "review-run", Verdict: verdict, Terminal: true, GraphSucceeded: true}))
	conclusion := "failure"
	if verdict == reviewpublisher.Approve {
		conclusion = "success"
	}
	receipt, err := s.PrepareIntent(ctx, reviewpublisher.Intent{AssignmentID: a.ID, Binding: a.Binding, CheckName: "ReliantReview", Conclusion: conclusion, Summary: "fixed", DetailsURL: "https://app.example.test/workflows/runs/review-root"})
	require.NoError(t, err)
	return s, receipt.IntentID, fixedHead{head}
}

func TestPublisherFoundRequiresExactTerminalPatch(t *testing.T) {
	for _, verdict := range []reviewpublisher.Verdict{reviewpublisher.Approve, reviewpublisher.Changes, reviewpublisher.Incomplete} {
		t.Run(string(verdict), func(t *testing.T) {
			s, id, head := publisherFixture(t, verdict)
			ctx := context.Background()
			claimed, err := s.ClaimPublication(ctx, id)
			require.NoError(t, err)
			require.True(t, claimed)
			require.NoError(t, s.UncertainPublication(ctx, id))
			bridge := &receiptBridge{}
			p := Publisher{Store: s, Heads: head, Bridge: bridge}
			got, err := p.Publish(ctx, id)
			require.NoError(t, err)
			require.Equal(t, int64(901), got.ID)
			require.Equal(t, 0, bridge.creates)
			require.Equal(t, 1, bridge.patches)
			require.Equal(t, 1, bridge.reconciles)
			require.Equal(t, int64(901), bridge.last.CheckID)
			want := "failure"
			if verdict == reviewpublisher.Approve {
				want = "success"
			}
			require.Equal(t, want, bridge.last.Conclusion)
		})
	}
}
func TestPublisherPatchFailureNeverConfirms(t *testing.T) {
	s, id, head := publisherFixture(t, reviewpublisher.Approve)
	ctx := context.Background()
	_, err := s.ClaimPublication(ctx, id)
	require.NoError(t, err)
	bridge := &receiptBridge{patchErr: errors.New("private provider failure")}
	_, err = (Publisher{Store: s, Heads: head, Bridge: bridge}).Publish(ctx, id)
	require.ErrorIs(t, err, reviewpublisher.ErrUnavailable)
	p, _, err := s.Publication(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "uncertain", p.State)
	require.Zero(t, p.CheckID)
	require.Equal(t, 0, bridge.creates)
}
