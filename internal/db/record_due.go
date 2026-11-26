package db

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// What the record_due trigger's due scan reads (internal/triggers/recorddue).

// ListEnabledTriggersOfKind lists every enabled trigger of kind.
func (r *Repo) ListEnabledTriggersOfKind(ctx context.Context, kind core.TriggerKind) ([]*core.Trigger, error) {
	return store.ListEnabledTriggersOfKind(ctx, r.ORM(), kind)
}

// WithDuePRPipelines runs the due scan (store.ListDuePRPipelines: up to
// limit rows due at now, FOR UPDATE SKIP LOCKED) in a READ COMMITTED
// transaction and calls fn with the due rows and their PR records while the
// rows stay locked, so a concurrent scan skips them instead of acting on
// them too. fn is not handed the transaction: what it writes commits on its
// own, before the locks are released. A row whose record is gone is left
// out. With nothing due, the scan is the one indexed query and fn is not
// called.
func (r *Repo) WithDuePRPipelines(ctx context.Context, now time.Time, limit int, fn func([]core.DuePRPipeline) error) error {
	_, err := r.WithDuePRPipelinesAfter(ctx, now, limit, nil, fn)
	return err
}

// WithDuePRPipelinesAfter scans one page after the cursor and returns the last
// examined row, including rows whose PR record is gone. Nil means wrap around.
func (r *Repo) WithDuePRPipelinesAfter(ctx context.Context, now time.Time, limit int, after *core.PRPipeline, fn func([]core.DuePRPipeline) error) (*core.PRPipeline, error) {
	var next *core.PRPipeline
	err := r.RunTx(ctx, orm.ReadCommitted, func(tx context.Context) error {
		rows, err := store.ListDuePRPipelinesAfter(tx, r.ORM(), now, limit, after)
		if err != nil || len(rows) == 0 {
			return err
		}
		if len(rows) == limit {
			next = rows[len(rows)-1]
		}
		ids := make([]string, len(rows))
		for i, row := range rows {
			ids[i] = row.RecordID
		}
		recs, err := store.ListPRRecordsByID(tx, r.ORM(), ids)
		if err != nil {
			return err
		}
		byID := make(map[string]*core.PRRecord, len(recs))
		for _, rec := range recs {
			byID[rec.ID] = rec
		}
		due := make([]core.DuePRPipeline, 0, len(rows))
		for _, row := range rows {
			if rec := byID[row.RecordID]; rec != nil {
				due = append(due, core.DuePRPipeline{Row: row, Record: rec})
			}
		}
		return fn(due)
	})
	return next, err
}
