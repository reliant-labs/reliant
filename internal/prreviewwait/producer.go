// Copyright (c) 2025 Reliant Labs
package prreviewwait

import (
	"context"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"strconv"
)

// ReviewReader is implemented by the existing authenticated provider adapter,
// including its explicit stable-reviewer-ID policy. Not a public agent claim.
type ReviewReader interface {
	Review(context.Context, string, prhandoff.Repository, int64, string) (prhandoff.Completion, error)
}

// TrustedReviewProducer translates an authenticated provider review receipt.
// Owner/repo/PR come from trusted event admission, never raw comment text.
// It is deliberately not constructed by any app root in this stage.
type TrustedReviewProducer struct {
	Reader   ReviewReader
	Bindings interface {
		Binding(context.Context, string, int64, int64) (prhandoff.Binding, error)
	}
	Wait Service
}

// Deliver refetches authorized provider state for the exact full source head.
// GitHub review numeric IDs provide monotonic review creation ordering; later
// dismissal/check/merge-queue adapters need their own authoritative revision.
func (p TrustedReviewProducer) Deliver(ctx context.Context, owner string, repoID, number int64, reviewID string) error {
	if p.Reader == nil || p.Bindings == nil || p.Wait == nil {
		return prhandoff.ErrRefused
	}
	b, err := p.Bindings.Binding(ctx, owner, repoID, number)
	if err != nil {
		return err
	}
	completion, err := p.Reader.Review(ctx, owner, b.Pull.Repo, number, reviewID)
	if err != nil {
		return err
	}
	sequence, err := strconv.ParseInt(completion.ReviewID, 10, 64)
	if err != nil || sequence <= 0 {
		return prhandoff.ErrRefused
	}
	pull := completion.Pull
	if completion.ReviewID != reviewID || completion.HeadSHA != b.Pull.HeadSHA || pull.HeadSHA != b.Pull.HeadSHA || pull.Repo.ID != repoID || pull.Number != number || pull.ObjectID != b.Pull.ObjectID || pull.HeadRepoID != b.Pull.HeadRepoID || pull.Branch != b.Pull.Branch || !pull.Open {
		return prhandoff.ErrRefused
	}
	state := ""
	switch completion.Verdict {
	case "approved":
		state = "approved"
	case "changes_requested":
		state = "changes"
	default:
		return prhandoff.ErrRefused
	}
	return p.Wait.Publish(ctx, Outcome{BindingID: b.ID, HeadSHA: b.Pull.HeadSHA, ProviderRef: "github-review:" + reviewID, Sequence: sequence, State: state})
}
