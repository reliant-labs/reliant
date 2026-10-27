package reviewpublisheradapter

import (
	"context"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
)

// Assignment is the concrete fresh-provider guard before the SQL assignment
// transaction. Inputs are references from trusted server admission, not a tool.
// SQL rechecks immutable handoff head and exact reserved subject in its tx; the
// external provider read cannot be made atomic with a database transaction.
type Assignment struct {
	Store store.ReviewAuthorityStore
	Heads reviewpublisher.HeadSource
}

// Assign loads the handoff/policy subject on the server, performs a fresh exact
// provider lookup, and only then enters the SQL authorization transaction.
func (a Assignment) Assign(ctx context.Context, in store.ReviewAssignmentRequest) (store.ReviewAuthority, error) {
	if a.Heads == nil {
		return store.ReviewAuthority{}, reviewpublisher.ErrDisabled
	}
	subject, err := a.Store.AssignmentSubject(ctx, in.PolicyID, in.BindingID)
	if err != nil {
		return store.ReviewAuthority{}, err
	}
	head, err := a.Heads.CurrentHead(ctx, subject.RepositoryID, subject.PullNumber)
	if err != nil {
		return store.ReviewAuthority{}, reviewpublisher.ErrUnavailable
	}
	if head != subject {
		return store.ReviewAuthority{}, reviewpublisher.ErrUnauthorized
	}
	in.ProviderHead = head
	return a.Store.AssignReview(ctx, in)
}
