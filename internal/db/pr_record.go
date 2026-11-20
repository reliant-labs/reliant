package db

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// PR records (dev-docs/pr-records.md): pull requests mirrored from verified
// webhook deliveries.

// ApplyPRRecordChange applies one decoded event to the records, once: it
// returns false, changing nothing, when this (provider, delivery key, event
// index) was applied before. READ COMMITTED is correct because every write
// is one upsert whose conflict arm re-checks its guard against the row it
// locked (the snapshot's age, a check's run id), and the receipt's primary
// key serializes two workers applying the same event.
func (r *Repo) ApplyPRRecordChange(ctx context.Context, c core.PRRecordChange) (bool, error) {
	var applied bool
	err := r.RunTx(ctx, orm.ReadCommitted, func(tx context.Context) error {
		applied = false
		d := c.Delivery
		d.Outcome = core.PRDeliveryIgnored
		claimed, err := store.InsertPRRecordDelivery(tx, r.ORM(), d)
		if err != nil || !claimed {
			return err
		}
		applied = true
		if c.Snapshot != nil {
			res, err := store.ApplyPRRecordSnapshot(tx, r.ORM(), *c.Snapshot)
			if err != nil {
				return err
			}
			d.RecordID = res.RecordID
			d.Outcome = core.PRDeliveryStale
			if res.Applied {
				d.Outcome = core.PRDeliveryApplied
			}
			if c.Snapshot.HeadSHA != "" {
				if err := store.EnsurePRRecordHead(tx, r.ORM(), core.PRRecordHead{
					ID: c.HeadID, RecordID: res.RecordID, HeadSHA: c.Snapshot.HeadSHA,
					PreviousHeadSHA: c.PreviousHeadSHA, FirstSeenAt: c.Snapshot.ObservedAt,
				}); err != nil {
					return err
				}
			}
			// A same-second push that moved the head may have been preceded by
			// the push over it, which could not apply then: follow it.
			if res.Tied && c.Snapshot.Delta.HeadSHA != "" && res.HeadSHA == c.Snapshot.Delta.HeadSHA {
				if res.HeadSHA, err = store.AdvancePRRecordHead(tx, r.ORM(), res.RecordID); err != nil {
					return err
				}
			}
			if res.HeadSHA != "" {
				if err := store.SupersedePRRecordHeads(tx, r.ORM(), res.RecordID, res.HeadSHA, c.Snapshot.ObservedAt); err != nil {
					return err
				}
			}
			for _, rv := range c.Reviews {
				rv.RecordID = res.RecordID
				if err := store.InsertPRRecordReview(tx, r.ORM(), rv); err != nil {
					return err
				}
				d.Outcome = core.PRDeliveryApplied
			}
		}
		if c.Check != nil {
			if err := store.UpsertPRRecordCheck(tx, r.ORM(), *c.Check); err != nil {
				return err
			}
			d.Outcome = core.PRDeliveryApplied
		}
		if d.Outcome == core.PRDeliveryIgnored {
			return nil
		}
		return store.SettlePRRecordDelivery(tx, r.ORM(), d)
	})
	return applied && err == nil, err
}

// GetPRRecord returns one pull request's record, or nil.
func (r *Repo) GetPRRecord(ctx context.Context, provider, repository string, number int) (*core.PRRecord, error) {
	return store.GetPRRecord(ctx, r.ORM(), provider, repository, number)
}

// ListPRRecords lists one repository's records in (number, repository id) order.
func (r *Repo) ListPRRecords(ctx context.Context, f core.PRRecordFilter) ([]*core.PRRecord, error) {
	return store.ListPRRecords(ctx, r.ORM(), f)
}

// ListPRRecordRepositories lists the repository identities records were kept
// under a repository name.
func (r *Repo) ListPRRecordRepositories(ctx context.Context, provider, repository string) ([]core.PRRecordRepository, error) {
	return store.ListPRRecordRepositories(ctx, r.ORM(), provider, repository)
}

// ListPRRecordHeads lists a record's heads, oldest first.
func (r *Repo) ListPRRecordHeads(ctx context.Context, recordID string) ([]core.PRRecordHead, error) {
	return store.ListPRRecordHeads(ctx, r.ORM(), recordID)
}

// ListPRRecordReviews lists the review markers on the given commits of a record.
func (r *Repo) ListPRRecordReviews(ctx context.Context, recordID string, commits []string) ([]core.PRRecordReview, error) {
	return store.ListPRRecordReviews(ctx, r.ORM(), recordID, commits)
}

// ListPRRecordChecks lists the checks on the given commits of a repository.
func (r *Repo) ListPRRecordChecks(ctx context.Context, provider, repositoryID string, shas []string) ([]core.PRRecordCheck, error) {
	return store.ListPRRecordChecks(ctx, r.ORM(), provider, repositoryID, shas)
}

// PrunePRRecordDeliveries deletes delivery receipts applied before before.
func (r *Repo) PrunePRRecordDeliveries(ctx context.Context, before time.Time) (int64, error) {
	return store.PrunePRRecordDeliveries(ctx, r.ORM(), before)
}

// HasFreshIntegrationAccess reports whether the user's own provider
// credential recently reported (account, resource) as visible to them.
func (r *Repo) HasFreshIntegrationAccess(ctx context.Context, userID, integrationID, account, resource string, freshAfter time.Time) (bool, error) {
	return store.HasFreshIntegrationAccess(ctx, r.ORM(), userID, integrationID, account, resource, freshAfter)
}
