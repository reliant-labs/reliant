// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/prreviewwait"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

func TestPRReviewWaitAtomicIsolationAndCursor(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	original := runcontinuity.Execution{WorkflowID: "wf", RunID: "first"}
	author := prhandoff.Author{OwnerID: "owner", RootID: "wf", ExecutionID: "first", ChatID: "chat", WorktreeID: "wt"}
	repo := prhandoff.Repository{ID: 42, Name: "org/repo"}
	handoff := store.PRHandoffStore{DB: db.ORM}
	require.NoError(t, handoff.RecordPublication(ctx, prhandoff.Publication{ID: "pub", Author: author, Repo: repo, Branch: "fix", HeadSHA: sha}))
	require.NoError(t, handoff.Enroll(ctx, prhandoff.Binding{ID: "b", Author: author, PublicationID: "pub", Pull: prhandoff.Pull{Repo: repo, Number: 1, ObjectID: 11, HeadRepoID: 42, Branch: "fix", HeadSHA: sha, Open: true}}))
	s := store.PRReviewWaitStore{DB: db.ORM}
	request := prreviewwait.Request{OwnerID: "owner", ChatID: "chat", Execution: original, RepoID: 42, PR: 1, HeadSHA: sha}
	proof := runcontinuity.Result{Original: original, Current: original, Live: true}
	for _, change := range []func(*prreviewwait.Request){func(r *prreviewwait.Request) { r.OwnerID = "other" }, func(r *prreviewwait.Request) { r.PR = 2 }, func(r *prreviewwait.Request) { r.HeadSHA = strings.Repeat("b", 40) }, func(r *prreviewwait.Request) { r.Execution.RunID = "unverified" }} {
		bad := request
		change(&bad)
		_, err := s.Arm(ctx, bad, proof)
		require.Error(t, err)
	}
	// Outcome before Arm remains durable; out-of-order delivery cannot become latest.
	_, err := s.Put(ctx, prreviewwait.Outcome{ID: "new", BindingID: "b", HeadSHA: sha, ProviderRef: "review:2", Sequence: 2, State: "changes"})
	require.NoError(t, err)
	sub, err := s.Arm(ctx, request, proof)
	require.NoError(t, err)
	require.NoError(t, s.Ready(ctx, sub))
	_, err = s.Put(ctx, prreviewwait.Outcome{ID: "old", BindingID: "b", HeadSHA: sha, ProviderRef: "review:1", Sequence: 1, State: "approved"})
	require.NoError(t, err)
	outcome, err := s.Read(ctx, sub)
	require.NoError(t, err)
	require.EqualValues(t, 2, outcome.Sequence)
	require.NoError(t, s.Notified(ctx, sub, 2))
	var consumed int64
	require.NoError(t, db.SQL.QueryRowContext(ctx, "SELECT consumed_sequence FROM pr_review_wait_subscriptions").Scan(&consumed))
	require.Zero(t, consumed)
	// Move delivery cursor only with immutable proof; old run can no longer read.
	successor := runcontinuity.Execution{WorkflowID: "wf", RunID: "next"}
	proof.Current = successor
	proof.Edges = []runcontinuity.Edge{{From: original, To: successor}}
	advanced, err := s.Advance(ctx, sub, proof)
	require.NoError(t, err)
	_, err = s.Read(ctx, sub)
	require.Error(t, err)
	request.Execution = successor
	rearmed, err := s.Arm(ctx, request, proof)
	require.NoError(t, err)
	require.Equal(t, "next", rearmed.CurrentRunID)
	outcome, err = s.Read(ctx, advanced)
	require.NoError(t, err)
	require.NoError(t, s.Consume(ctx, advanced, outcome))
	require.NoError(t, s.Consume(ctx, advanced, outcome))
	// Repair publication moves the binding head; old-head inbox writes refuse.
	require.NoError(t, handoff.RecordPublication(ctx, prhandoff.Publication{ID: "repair", Author: author, Repo: repo, Branch: "fix", HeadSHA: strings.Repeat("b", 40), PreviousHeadSHA: sha}))
	_, err = s.Put(ctx, prreviewwait.Outcome{ID: "stale", BindingID: "b", HeadSHA: sha, ProviderRef: "review:3", Sequence: 3, State: "approved"})
	require.Error(t, err)
}
