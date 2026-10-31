package db

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

func (r *Repo) EnqueueTriggerCapacity(ctx context.Context, ev *core.TriggerEvent, scope store.TriggerCapacityScope) error {
	return store.EnqueueTriggerCapacity(ctx, r.ORM(), ev, scope)
}
func (r *Repo) NextWaitingTriggerCapacityEvent(ctx context.Context, scope store.TriggerCapacityScope) (*core.TriggerEvent, error) {
	return store.NextWaitingTriggerCapacityEvent(ctx, r.ORM(), scope)
}
func (r *Repo) CanLaunchTriggerCapacity(ctx context.Context, id string, scope store.TriggerCapacityScope, n uint32) (bool, error) {
	return store.CanLaunchTriggerCapacity(ctx, r.ORM(), id, scope, n)
}
func (r *Repo) PrepareTriggerCapacityExecution(ctx context.Context, id, wid string) error {
	return store.PrepareTriggerCapacityExecution(ctx, r.ORM(), id, wid)
}
func (r *Repo) BindTriggerCapacityExecution(ctx context.Context, id, wid, rid string) error {
	return store.BindTriggerCapacityExecution(ctx, r.ORM(), id, wid, rid)
}
func (r *Repo) ListTriggerCapacityExecutions(ctx context.Context, scope store.TriggerCapacityScope) ([]store.TriggerCapacityExecution, error) {
	return store.ListTriggerCapacityExecutions(ctx, r.ORM(), scope)
}
func (r *Repo) ReleaseUnboundTriggerCapacity(ctx context.Context, e store.TriggerCapacityExecution, reason string) (bool, error) {
	return store.ReleaseUnboundTriggerCapacity(ctx, r.ORM(), e, reason)
}
func (r *Repo) FenceUnboundTriggerCapacityStart(ctx context.Context, e store.TriggerCapacityExecution) (bool, error) {
	return store.FenceUnboundTriggerCapacityStart(ctx, r.ORM(), e)
}
func (r *Repo) TriggerCapacityStartFenced(ctx context.Context, e store.TriggerCapacityExecution) (bool, error) {
	return store.TriggerCapacityStartFenced(ctx, r.ORM(), e)
}
func (r *Repo) ListTriggerCapacityExecutionBatch(ctx context.Context, limit int) ([]store.TriggerCapacityExecution, error) {
	return store.ListTriggerCapacityExecutionBatch(ctx, r.ORM(), limit)
}
func (r *Repo) ReleaseTriggerCapacityExecution(ctx context.Context, e store.TriggerCapacityExecution) error {
	return store.ReleaseTriggerCapacityExecution(ctx, r.ORM(), e)
}
func (r *Repo) ReleaseClosedTriggerCapacityExecution(ctx context.Context, e store.TriggerCapacityExecution) error {
	return store.ReleaseClosedTriggerCapacityExecution(ctx, r.ORM(), e)
}
