package db

import (
	"context"
	"encoding/json"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// The merge queue (queue_entries, queue_batches, main_health). Each method is
// the internal/store query of the same name; its doc there says what it
// locks and which errors it returns.

// EnqueueQueueEntry puts a PR head in the queue, idempotently per PR.
func (r *Repo) EnqueueQueueEntry(ctx context.Context, e core.QueueEntryNew) (*core.QueueEntry, bool, error) {
	return store.EnqueueQueueEntry(ctx, r.ORM(), e)
}

// ListAdmissibleQueueEntries lists the repository's waiting entries that pass
// the DB-only admission conditions, in pick order.
func (r *Repo) ListAdmissibleQueueEntries(ctx context.Context, repoID string, now time.Time, limit int) ([]*core.QueueEntry, error) {
	return store.ListAdmissibleQueueEntries(ctx, r.ORM(), repoID, now, limit)
}

// EjectQueueEntry takes an entry out of the queue for reason.
func (r *Repo) EjectQueueEntry(ctx context.Context, id string, reason core.QueueEjectReason) (*core.QueueEntry, error) {
	return store.EjectQueueEntry(ctx, r.ORM(), id, reason)
}

// MarkQueueEntryLanded records a batched entry's squash merge.
func (r *Repo) MarkQueueEntryLanded(ctx context.Context, id, landedSHA string) (*core.QueueEntry, error) {
	return store.MarkQueueEntryLanded(ctx, r.ORM(), id, landedSHA)
}

// RequeueQueueEntries puts the batch's batched entries back to waiting.
func (r *Repo) RequeueQueueEntries(ctx context.Context, batchID string) (int64, error) {
	return store.RequeueQueueEntries(ctx, r.ORM(), batchID)
}

// ListQueueBatchEntries lists the batch's entries in landing order.
func (r *Repo) ListQueueBatchEntries(ctx context.Context, batchID string) ([]*core.QueueEntry, error) {
	return store.ListQueueBatchEntries(ctx, r.ORM(), batchID)
}

// CreateQueueBatch starts a batch, building.
func (r *Repo) CreateQueueBatch(ctx context.Context, b core.QueueBatchNew) (*core.QueueBatch, error) {
	return store.CreateQueueBatch(ctx, r.ORM(), b)
}

// GetQueueBatch returns the batch with id, or nil.
func (r *Repo) GetQueueBatch(ctx context.Context, id string) (*core.QueueBatch, error) {
	return store.GetQueueBatch(ctx, r.ORM(), id)
}

// LatestQueueBatches lists the repository's newest batches.
func (r *Repo) LatestQueueBatches(ctx context.Context, repoID string, limit int) ([]*core.QueueBatch, error) {
	return store.LatestQueueBatches(ctx, r.ORM(), repoID, limit)
}

// AttachQueueEntries puts entries in a building batch, all or none.
func (r *Repo) AttachQueueEntries(ctx context.Context, batchID string, entryIDs []string) error {
	return store.AttachQueueEntries(ctx, r.ORM(), batchID, entryIDs)
}

// SetQueueBatchState moves a batch from one state to the next, if §3.2
// allows it and the batch is still in from.
func (r *Repo) SetQueueBatchState(ctx context.Context, id string, from, to core.QueueBatchState, u core.QueueBatchUpdate) (*core.QueueBatch, error) {
	return store.SetQueueBatchState(ctx, r.ORM(), id, from, to, u)
}

// MarkQueueBatchLanded records a passed batch as landed at landedSHA.
func (r *Repo) MarkQueueBatchLanded(ctx context.Context, id, landedSHA string) (*core.QueueBatch, error) {
	return store.MarkQueueBatchLanded(ctx, r.ORM(), id, landedSHA)
}

// PatchQueueBatchVerdict merges a JSON object into the batch's verdict_json.
func (r *Repo) PatchQueueBatchVerdict(ctx context.Context, id string, patch json.RawMessage) (*core.QueueBatch, error) {
	return store.PatchQueueBatchVerdict(ctx, r.ORM(), id, patch)
}

// InsertMainHealth records a verdict on a sha of main.
func (r *Repo) InsertMainHealth(ctx context.Context, h core.MainHealthRecord) (*core.MainHealthRecord, error) {
	return store.InsertMainHealth(ctx, r.ORM(), h)
}

// LatestMainHealth returns the repository's newest verdict on main, or nil.
func (r *Repo) LatestMainHealth(ctx context.Context, repoID string) (*core.MainHealthRecord, error) {
	return store.LatestMainHealth(ctx, r.ORM(), repoID)
}
