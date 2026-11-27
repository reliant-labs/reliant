package store

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// What the record_due trigger's due scan reads (pipeline build plan task
// M4): the enabled activations of record_due triggers, and the PR records
// a due pr_pipeline row is about.

// ListEnabledTriggersOfKind lists every enabled trigger of kind, across all
// users, oldest first.
func ListEnabledTriggersOfKind(ctx context.Context, db orm.Context, kind core.TriggerKind) ([]*core.Trigger, error) {
	rows, err := ListTrigger(ctx, db,
		orm.WhereEq(TriggerFieldKind, string(kind)),
		orm.WhereEq(TriggerFieldEnabled, true),
		orm.WithOrderBy(TriggerFieldCreatedAt, orm.Asc),
		orm.WithOrderBy(TriggerFieldID, orm.Asc))
	if err != nil {
		return nil, fmt.Errorf("list enabled %s triggers: %w", kind, err)
	}
	return triggersToCore(rows)
}

// ListPRRecordsByID returns the PR records with the given ids, in no
// particular order; an id with no record is left out.
func ListPRRecordsByID(ctx context.Context, db orm.Context, ids []string) ([]*core.PRRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []prRecordRow
	if err := db.Bun().NewRaw(`SELECT `+prRecordColumns+` FROM pr_records WHERE id = ANY(?::text[])`,
		orm.Arg(ids)).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list pr records by id: %w", err)
	}
	out := make([]*core.PRRecord, 0, len(rows))
	for _, r := range rows {
		rec, err := r.core()
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}
