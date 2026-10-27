package reviewpublisheradapter

import (
	"context"
	"github.com/reliant-labs/forge/pkg/orm"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/prreviewwait"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/stretchr/testify/require"
)

type authorContinuity struct{}

func (authorContinuity) Resolve(context.Context, runcontinuity.Execution) (runcontinuity.Result, error) {
	e := runcontinuity.Execution{WorkflowID: "author", RunID: "author-run"}
	return runcontinuity.Result{Original: e, Current: e, Live: true}, nil
}

type committedSignal struct {
	t     *testing.T
	calls int
	db    orm.Context
}

func (s *committedSignal) SignalReview(ctx context.Context, _ runcontinuity.Execution, _ string) error {
	s.calls++
	var n int
	// Background context deliberately bypasses the caller's transaction binding.
	err := s.db.Bun().NewRaw(`SELECT count(*) FROM pr_review_wait_outcomes`).Scan(context.Background(), &n)
	require.NoError(s.t, err)
	require.Equal(s.t, 1, n, "signal must see committed inbox from separate pool query")
	return nil
}

func TestGuardedWaitRejectsAmbientTransaction(t *testing.T) {
	s, _, _ := publisherFixture(t, reviewpublisher.Approve)
	guarded := GuardedWaitStore{PRReviewWaitStore: store.PRReviewWaitStore{DB: s.DB}}
	err := s.DB.RunTx(context.Background(), orm.Serializable, func(ctx context.Context) error {
		_, err := guarded.Put(ctx, prreviewwait.Outcome{}, prreviewwait.Authority{})
		require.ErrorIs(t, err, reviewpublisher.ErrUnauthorized)
		_, err = guarded.Arm(ctx, prreviewwait.Request{}, runcontinuity.Result{}, prreviewwait.Authority{})
		require.ErrorIs(t, err, reviewpublisher.ErrUnauthorized)
		return nil
	})
	require.NoError(t, err)
}

func TestGuardedWaitSignalsOnlyAfterInboxCommit(t *testing.T) {
	s, id, _ := publisherFixture(t, reviewpublisher.Approve)
	ctx := context.Background()
	_, a, err := s.Publication(ctx, id)
	require.NoError(t, err)
	_, err = s.ClaimPublication(ctx, id)
	require.NoError(t, err)
	require.NoError(t, s.ConfirmPublication(ctx, id, store.ReviewCheckReceipt{RepositoryID: 42, HeadSHA: a.Binding.HeadSHA, Name: "ReliantReview", ExternalID: store.ReviewExternalID(a, id), CheckID: 901, CheckURL: "https://github.com/acme/repo/runs/901"}))
	guarded := GuardedWaitStore{PRReviewWaitStore: store.PRReviewWaitStore{DB: s.DB}}
	signal := &committedSignal{t: t, db: s.DB}
	wait, err := prreviewwait.New(prreviewwait.Deps{Store: guarded, Policy: WaitPolicy{Store: s}, Continuity: authorContinuity{}, Signals: signal})
	require.NoError(t, err)
	e := runcontinuity.Execution{WorkflowID: "author", RunID: "author-run"}
	_, err = wait.Arm(ctx, prreviewwait.Request{RequestKey: strings.Repeat("b", 64), OwnerID: a.OwnerID, ChatID: "author-chat", Execution: e, RepoID: 42, PR: 7, HeadSHA: a.Binding.HeadSHA})
	require.NoError(t, err)
	require.NoError(t, (WaitOutcome{Store: s, Wait: wait}).Deliver(ctx, id))
	require.Equal(t, 1, signal.calls)
}

func TestGuardedWaitPolicyAndConfirmedIntentAtomic(t *testing.T) {
	for _, mutation := range []string{"", "disable", "revision", "cancel", "uncertain", "head"} {
		t.Run(mutation, func(t *testing.T) {
			s, id, _ := publisherFixture(t, reviewpublisher.Approve)
			ctx := context.Background()
			p, a, err := s.Publication(ctx, id)
			require.NoError(t, err)
			_, err = s.ClaimPublication(ctx, id)
			require.NoError(t, err)
			require.NoError(t, s.ConfirmPublication(ctx, id, store.ReviewCheckReceipt{RepositoryID: 42, HeadSHA: a.Binding.HeadSHA, Name: "ReliantReview", ExternalID: store.ReviewExternalID(a, id), CheckID: 901, CheckURL: "https://github.com/acme/repo/runs/901"}))
			guarded := GuardedWaitStore{PRReviewWaitStore: store.PRReviewWaitStore{DB: s.DB}}
			authority := prreviewwait.Authority{ID: "policy", PolicyRevision: 1}
			e := runcontinuity.Execution{WorkflowID: "author", RunID: "author-run"}
			request := prreviewwait.Request{RequestKey: strings.Repeat("a", 64), OwnerID: a.OwnerID, ChatID: "author-chat", Execution: e, RepoID: 42, PR: 7, HeadSHA: a.Binding.HeadSHA}
			_, err = guarded.Arm(ctx, request, runcontinuity.Result{Original: e, Current: e, Live: true}, authority)
			require.NoError(t, err)
			statement := map[string]string{"disable": `UPDATE review_policies SET enabled=false`, "revision": `UPDATE review_policies SET revision=2`, "cancel": `UPDATE review_assignments SET cancelled=true`, "uncertain": `UPDATE review_publication_intents SET state='uncertain'`, "head": `UPDATE pr_handoff_bindings SET head_sha=repeat('c',40)`}[mutation]
			if statement != "" {
				_, err = s.DB.Bun().NewRaw(statement).Exec(ctx)
				require.NoError(t, err)
			}
			o := prreviewwait.Outcome{ID: "outcome", OwnerID: a.OwnerID, AuthorityID: "policy", BindingID: a.BindingID, HeadSHA: a.Binding.HeadSHA, ProviderRef: "reliant-check:" + a.ID + ":" + p.ID, Sequence: a.Binding.Generation, State: "approved"}
			_, err = guarded.Put(ctx, o, authority)
			var n int
			require.NoError(t, s.DB.Bun().NewRaw(`SELECT count(*) FROM pr_review_wait_outcomes`).Scan(ctx, &n))
			if mutation == "" {
				require.NoError(t, err)
				require.Equal(t, 1, n)
			} else {
				require.Error(t, err)
				require.Zero(t, n)
			}
		})
	}
}
