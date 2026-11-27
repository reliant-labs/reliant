package db

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The merge queue scheduler's reads of PR records (internal/mergequeue).
// Each method is the internal/store query of the same name.

// GetQueuePRRecord returns the PR record with id, or nil.
func (r *Repo) GetQueuePRRecord(ctx context.Context, id string) (*core.PRRecord, error) {
	return store.GetQueuePRRecord(ctx, r.ORM(), id)
}

// ListQueueHeadChecks lists the checks recorded on one head of a PR record's
// repository.
func (r *Repo) ListQueueHeadChecks(ctx context.Context, recordID, head string) ([]core.PRRecordCheck, error) {
	return store.ListQueueHeadChecks(ctx, r.ORM(), recordID, head)
}

// ListStaleQueueEntries lists the repository's queued entries whose PR record
// no longer holds them (closed, merged, gone, or at another head).
func (r *Repo) ListStaleQueueEntries(ctx context.Context, repoID string) ([]core.StaleQueueEntry, error) {
	return store.ListStaleQueueEntries(ctx, r.ORM(), repoID)
}
