package reviewpublisheradapter

import (
	"context"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
)

// SQL implements the core's ReviewSource and IntentStore over canonical SQL.
// Construction is explicit; an empty policy table never enables review authority.
type SQL struct{ Store store.ReviewAuthorityStore }

// LoadReview projects a live assignment; it never treats generic completed status
// or a model action as a structured verdict.
func (s SQL) LoadReview(ctx context.Context, id string) (reviewpublisher.Review, error) {
	a, err := s.Store.LoadReviewAuthority(ctx, id)
	if err != nil {
		return reviewpublisher.Review{}, err
	}
	return reviewpublisher.Review{AssignmentID: a.ID, Policy: a.Policy, Binding: a.Binding, Author: a.Author,
		CurrentGeneration: a.CurrentGeneration, Cancelled: a.Cancelled, Superseded: a.Superseded, Completion: a.Completion}, nil
}

// PrepareIntent delegates to the serializable authority and dedupe transaction.
func (s SQL) PrepareIntent(ctx context.Context, in reviewpublisher.Intent) (reviewpublisher.Receipt, error) {
	return s.Store.PrepareIntent(ctx, in)
}
