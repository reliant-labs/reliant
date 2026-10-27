// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/prreviewwait"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

type authorityPolicy struct {
	mu       sync.Mutex
	chosen   prreviewwait.Authority
	subjects []prreviewwait.AuthoritySubject
	after    func()
}

func (p *authorityPolicy) Resolve(_ context.Context, s prreviewwait.AuthoritySubject) (prreviewwait.Authority, error) {
	p.mu.Lock()
	p.subjects = append(p.subjects, s)
	a, after := p.chosen, p.after
	p.after = nil
	p.mu.Unlock()
	if after != nil {
		after()
	}
	return a, nil
}

type authorityContinuity struct{ original runcontinuity.Execution }

func (c authorityContinuity) Resolve(_ context.Context, e runcontinuity.Execution) (runcontinuity.Result, error) {
	return runcontinuity.Result{Original: e, Current: c.original, Live: true}, nil
}

type authoritySignals struct{}

func (authoritySignals) SignalReview(context.Context, runcontinuity.Execution, string) error {
	return nil
}

func TestPRReviewWaitAuthorityIsolationAndConcurrentEnrollment(t *testing.T) {
	database := storetest.New(t)
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	original := runcontinuity.Execution{WorkflowID: "wf", RunID: "run"}
	author := prhandoff.Author{OwnerID: "owner", RootID: "wf", ExecutionID: "run", ChatID: "chat", WorktreeID: "wt"}
	repo := prhandoff.Repository{ID: 42, Name: "org/repo"}
	handoff := store.PRHandoffStore{DB: database.ORM}
	persistent := store.PRReviewWaitStore{DB: database.ORM}
	require.NoError(t, handoff.RecordPublication(ctx, prhandoff.Publication{ID: "pub", Author: author, Repo: repo, Branch: "fix", HeadSHA: sha}))
	binding := prhandoff.Binding{ID: "b", Author: author, PublicationID: "pub", Pull: prhandoff.Pull{Repo: repo, Number: 1, ObjectID: 11, HeadRepoID: 42, Branch: "fix", HeadSHA: sha, Open: true}}
	require.NoError(t, handoff.Enroll(ctx, binding))
	policy := &authorityPolicy{chosen: prreviewwait.Authority{ID: "server-review-assignment", PolicyRevision: 1}}
	svc, err := prreviewwait.New(prreviewwait.Deps{Store: persistent, Continuity: authorityContinuity{original}, Signals: authoritySignals{}, Policy: policy})
	require.NoError(t, err)
	outcome := prreviewwait.Outcome{OwnerID: "owner", AuthorityID: "github-review", BindingID: "b", HeadSHA: sha, ProviderRef: "review:999", Sequence: 999, State: "approved"}
	require.Error(t, svc.Publish(ctx, outcome))
	var rows int
	require.NoError(t, database.SQL.QueryRowContext(ctx, "SELECT count(*) FROM pr_review_wait_authorities").Scan(&rows))
	require.Zero(t, rows) // wrong caller cannot choose first authority
	outcome.AuthorityID = policy.chosen.ID
	outcome.ProviderRef = "assignment:2"
	outcome.Sequence = 2
	outcome.State = "changes"
	require.NoError(t, svc.Publish(ctx, outcome)) // pre-Arm registers through trusted policy
	request := prreviewwait.Request{RequestKey: strings.Repeat("a", 64), OwnerID: "owner", ChatID: "chat", Execution: original, RepoID: 42, PR: 1, HeadSHA: sha}
	sub, err := svc.Arm(ctx, request)
	require.NoError(t, err)
	latest, err := svc.Read(ctx, sub)
	require.NoError(t, err)
	require.EqualValues(t, 2, latest.Sequence)
	require.Equal(t, "changes", latest.State)
	require.Equal(t, policy.chosen.ID, latest.AuthorityID)
	require.NoError(t, svc.Publish(ctx, outcome))
	old := outcome
	old.ProviderRef = "assignment:1"
	old.Sequence = 1
	require.NoError(t, svc.Publish(ctx, old))
	latest, err = svc.Read(ctx, sub)
	require.NoError(t, err)
	require.EqualValues(t, 2, latest.Sequence)
	wrongOwner := outcome
	wrongOwner.OwnerID = "stranger"
	require.Error(t, svc.Publish(ctx, wrongOwner))
	policy.mu.Lock()
	policy.chosen = prreviewwait.Authority{ID: "github-review", PolicyRevision: 2}
	policy.mu.Unlock()
	outcome.AuthorityID = "github-review"
	outcome.ProviderRef = "review:999"
	outcome.Sequence = 999
	require.Error(t, svc.Publish(ctx, outcome))
	_, err = svc.Arm(ctx, request)
	require.Error(t, err) // same-head rotation refused
	require.Error(t, persistent.BindAuthority(ctx, binding, prreviewwait.Authority{ID: "server-review-assignment", PolicyRevision: 2}))
	// New head may adopt a new policy. Concurrent Arm/pre-Arm Publish serialize
	// to exactly one immutable authority, never sequence namespaces mixed.
	next := strings.Repeat("b", 40)
	require.NoError(t, handoff.RecordPublication(ctx, prhandoff.Publication{ID: "repair", Author: author, Repo: repo, Branch: "fix", HeadSHA: next, PreviousHeadSHA: sha}))
	request.HeadSHA = next
	request.RequestKey = strings.Repeat("b", 64)
	outcome.HeadSHA = next
	outcome.Sequence = 3
	outcome.ProviderRef = "review:3"
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() { <-start; _, err := svc.Arm(ctx, request); errs <- err }()
	go func() { <-start; errs <- svc.Publish(ctx, outcome) }()
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.NoError(t, database.SQL.QueryRowContext(ctx, "SELECT count(*) FROM pr_review_wait_authorities").Scan(&rows))
	require.Equal(t, 2, rows)
	stale := outcome
	stale.HeadSHA = sha
	require.Error(t, svc.Publish(ctx, stale))
	// Head changes after external policy resolution: transactional Put refuses
	// the stale snapshot instead of pinning/writing the wrong head.
	third := strings.Repeat("c", 40)
	policy.mu.Lock()
	policy.after = func() {
		require.NoError(t, handoff.RecordPublication(ctx, prhandoff.Publication{ID: "repair2", Author: author, Repo: repo, Branch: "fix", HeadSHA: third, PreviousHeadSHA: next}))
	}
	policy.mu.Unlock()
	outcome.Sequence = 4
	outcome.ProviderRef = "review:4"
	require.Error(t, svc.Publish(ctx, outcome))
	policy.mu.Lock()
	for _, subject := range policy.subjects {
		require.Equal(t, "owner", subject.OwnerID)
		require.EqualValues(t, 42, subject.RepositoryID)
		require.EqualValues(t, 1, subject.PR)
	}
	policy.mu.Unlock()
}
