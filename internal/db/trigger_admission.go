package db

import (
	"context"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

func (r *Repo) EnqueueTriggerAdmission(ctx context.Context, ev *core.TriggerEvent, req core.TriggerAdmissionRequest) (out *core.TriggerEvent, created bool, err error) {
	err = r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var e error
		out, created, e = store.EnqueueTriggerAdmission(ctx, r.ORM(), ev, req)
		return e
	})
	return
}
func (r *Repo) GetAdmissionEvent(ctx context.Context, id string) (*core.TriggerEvent, error) {
	return store.GetAdmissionEvent(ctx, r.ORM(), id)
}
func (r *Repo) GetTriggerAdmission(ctx context.Context, id string) (*core.TriggerAdmission, error) {
	return store.GetTriggerAdmission(ctx, r.ORM(), id)
}
func (r *Repo) ReserveTriggerAdmission(ctx context.Context, id string, max int) error {
	return store.ReserveTriggerAdmission(ctx, r.ORM(), id, max)
}
func (r *Repo) BindTriggerAdmissionRun(ctx context.Context, id, runID string) error {
	return store.BindTriggerAdmissionRun(ctx, r.ORM(), id, runID)
}
func (r *Repo) ReleaseTriggerAdmission(ctx context.Context, id, chatID, runID string) (bool, error) {
	return store.ReleaseTriggerAdmission(ctx, r.ORM(), id, chatID, runID)
}
func (r *Repo) SettleOrphanQueuedAdmission(ctx context.Context, id string) error {
	return store.SettleOrphanQueuedAdmission(ctx, r.ORM(), id)
}
func (r *Repo) TriggerAdmissionHighWater(ctx context.Context) (int64, error) {
	return store.TriggerAdmissionHighWater(ctx, r.ORM())
}
func (r *Repo) ListTriggerAdmissions(ctx context.Context, after, through int64, limit int) ([]core.TriggerAdmission, error) {
	return store.ListTriggerAdmissions(ctx, r.ORM(), after, through, limit)
}
