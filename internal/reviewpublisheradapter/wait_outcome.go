package reviewpublisheradapter

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/prreviewwait"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
)

// WaitPolicy resolves owner/admin-provisioned SQL authority for a binding-derived
// subject. There is exactly one private server-check producer namespace; never
// register the GitHub numeric-review producer alongside it for this binding/head.
type WaitPolicy struct{ Store store.ReviewAuthorityStore }

// Resolve returns persistent policy ID+revision, never a claimed provider prefix.
func (p WaitPolicy) Resolve(ctx context.Context, subject prreviewwait.AuthoritySubject) (prreviewwait.Authority, error) {
	id, revision, err := p.Store.ResolveReviewWaitPolicy(ctx, subject.OwnerID, subject.RepositoryID, subject.PR, subject.HeadSHA)
	if err != nil {
		return prreviewwait.Authority{}, err
	}
	return prreviewwait.Authority{ID: id, PolicyRevision: revision}, nil
}

// WaitOutcome delivers only confirmed server-assignment receipts to the existing
// durable author wait. Publish performs owner/head/authority/sequence SQL checks;
// an unavailable/unarmed author is an error, never permission to start another.
type WaitOutcome struct {
	Store store.ReviewAuthorityStore
	Wait  prreviewwait.Service
}

// Deliver emits no verdict-bearing signal. It persists through Wait.Publish then
// sends only that service's exact recipient doorbell; no fabricated GitHub review.
func (w WaitOutcome) Deliver(ctx context.Context, intentID string) error {
	if w.Wait == nil {
		return reviewpublisher.ErrDisabled
	}
	p, a, err := w.Store.Publication(ctx, intentID)
	if err != nil {
		return err
	}
	if p.State != "confirmed" || p.CheckID <= 0 {
		return reviewpublisher.ErrUnauthorized
	}
	state := "unavailable"
	switch a.Completion.Verdict {
	case reviewpublisher.Approve:
		if p.Intent.Conclusion == "success" && a.Completion.GraphSucceeded {
			state = "approved"
		}
	case reviewpublisher.Changes:
		state = "changes"
	}
	return w.Wait.Publish(ctx, prreviewwait.Outcome{OwnerID: a.OwnerID, AuthorityID: a.PolicyID, BindingID: a.BindingID, HeadSHA: a.Binding.HeadSHA, ProviderRef: fmt.Sprintf("reliant-check:%s:%s", a.ID, p.ID), Sequence: a.Binding.Generation, State: state})
}
