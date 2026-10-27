package reviewpublisheradapter

import (
	"context"
	"strings"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/prreviewwait"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/reliant-labs/reliant/internal/store"
)

// GuardedWaitStore adds only private SQL policy/confirmed-intent CAS to the
// existing wait store's pure Arm/Put operations. Service.Publish signaling occurs
// AFTER these outer transactions commit; never wrap the signaling service in tx.
// Both stores MUST use this exact DB context/pool so nested RunTx joins correctly.
type GuardedWaitStore struct{ store.PRReviewWaitStore }

// Arm atomically rechecks current private policy before pinning wait authority.
func (s GuardedWaitStore) Arm(ctx context.Context, r prreviewwait.Request, p runcontinuity.Result, authority prreviewwait.Authority) (prreviewwait.Subscription, error) {
	var out prreviewwait.Subscription
	err := s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		if err := s.checkPolicy(ctx, r.OwnerID, r.RepoID, r.PR, r.HeadSHA, authority); err != nil {
			return err
		}
		var err error
		out, err = s.PRReviewWaitStore.Arm(ctx, r, p, authority)
		return err
	})
	return out, err
}

// Put verifies server-owned confirmed assignment+intent+policy in the SAME
// transaction as the inbox insert. A prefix alone never grants authority.
func (s GuardedWaitStore) Put(ctx context.Context, o prreviewwait.Outcome, authority prreviewwait.Authority) (prreviewwait.Subscription, error) {
	var out prreviewwait.Subscription
	err := s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		parts := strings.Split(o.ProviderRef, ":")
		if len(parts) != 3 || parts[0] != "reliant-check" || parts[1] == "" || parts[2] == "" {
			return reviewpublisher.ErrUnauthorized
		}
		p, a, err := (store.ReviewAuthorityStore{DB: s.DB}).Publication(ctx, parts[2])
		if err != nil {
			return err
		}
		if p.State != "confirmed" || p.CheckID <= 0 || a.ID != parts[1] || a.OwnerID != o.OwnerID || a.PolicyID != o.AuthorityID || a.BindingID != o.BindingID || a.Binding.HeadSHA != o.HeadSHA || a.Binding.Generation != o.Sequence {
			return reviewpublisher.ErrUnauthorized
		}
		expected := "unavailable"
		if a.Completion.Verdict == reviewpublisher.Changes {
			expected = "changes"
		} else if a.Completion.Verdict == reviewpublisher.Approve && a.Completion.GraphSucceeded && p.Intent.Conclusion == "success" {
			expected = "approved"
		}
		if o.State != expected {
			return reviewpublisher.ErrUnauthorized
		}
		if err = s.checkPolicy(ctx, a.OwnerID, a.Binding.RepositoryID, a.Binding.PullNumber, a.Binding.HeadSHA, authority); err != nil {
			return err
		}
		out, err = s.PRReviewWaitStore.Put(ctx, o, authority)
		return err
	})
	return out, err
}
func (s GuardedWaitStore) checkPolicy(ctx context.Context, owner string, repo, pr int64, head string, a prreviewwait.Authority) error {
	id, revision, err := (store.ReviewAuthorityStore{DB: s.DB}).ResolveReviewWaitPolicy(ctx, owner, repo, pr, head)
	if err != nil {
		return err
	}
	if id != a.ID || revision != a.PolicyRevision {
		return reviewpublisher.ErrUnauthorized
	}
	return nil
}

var _ prreviewwait.Store = GuardedWaitStore{}
