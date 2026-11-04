package db

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

type WorktreeRecovery = core.WorktreeRecovery
type RecoveryCheckout = core.RecoveryCheckout

func (r *Repo) CreateWorktreeRecovery(ctx context.Context, v *WorktreeRecovery) error {
	return store.InsertWorktreeRecovery(ctx, r.ORM(), v)
}
func (r *Repo) GetWorktreeRecovery(ctx context.Context, owner, id string) (*WorktreeRecovery, error) {
	return store.GetWorktreeRecovery(ctx, r.ORM(), owner, id)
}
func (r *Repo) ListWorktreeRecoveries(ctx context.Context, owner, project string) ([]*WorktreeRecovery, error) {
	return store.ListWorktreeRecoveries(ctx, r.ORM(), owner, project)
}
func (r *Repo) ClaimWorktreeRecovery(ctx context.Context, owner, id string, generation int64) error {
	return store.ClaimWorktreeRecovery(ctx, r.ORM(), owner, id, generation)
}
func (r *Repo) RecordWorktreeRecoveryReceipt(ctx context.Context, owner, id, repo, receipt string) error {
	return store.RecordWorktreeRecoveryReceipt(ctx, r.ORM(), owner, id, repo, receipt)
}
func (r *Repo) AuthorizeWorktreeRecovery(ctx context.Context, owner, id string, until time.Time) error {
	return store.AuthorizeWorktreeRecovery(ctx, r.ORM(), owner, id, until)
}
func (r *Repo) CompleteWorktreeRecoveryCheckout(ctx context.Context, owner, id, repo, phase, detail string) error {
	return store.CompleteWorktreeRecoveryCheckout(ctx, r.ORM(), owner, id, repo, phase, detail)
}
func (r *Repo) CancelWorktreeRecovery(ctx context.Context, owner, id, detail string) error {
	return store.CancelWorktreeRecovery(ctx, r.ORM(), owner, id, detail)
}
func (r *Repo) BeginWorktreeRecoveryOperation(ctx context.Context, owner, id, operation, key, targetDaemonID string) error {
	return store.BeginWorktreeRecoveryOperation(ctx, r.ORM(), owner, id, operation, key, targetDaemonID)
}
func (r *Repo) RecordWorktreeActivity(ctx context.Context, id string) error {
	return store.RecordWorktreeActivity(ctx, r.ORM(), id)
}
func (r *Repo) ArchiveWorktreeForRecovery(ctx context.Context, owner, id string, generation int64) error {
	return store.ArchiveWorktreeForRecovery(ctx, r.ORM(), owner, id, generation)
}
