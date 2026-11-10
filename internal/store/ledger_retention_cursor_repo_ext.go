// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for LedgerRetentionCursor: where each
// time-ordered retention pass resumes (internal/db/ledger_retention.go).
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// ReadLedgerRetentionCursor returns where the pass name resumes: everything at
// or before (at, id) is done. A pass that never ran resumes from the start
// (the zero time and "").
func ReadLedgerRetentionCursor(ctx context.Context, db orm.Context, name string) (at time.Time, id string, err error) {
	var rows []*LedgerRetentionCursor
	if err := db.Bun().NewSelect().Model(&rows).Where("name = ?", name).Scan(ctx); err != nil {
		return time.Time{}, "", fmt.Errorf("read retention cursor %s: %w", name, err)
	}
	if len(rows) == 0 {
		return time.Time{}, "", nil
	}
	return rows[0].ThroughAt, rows[0].ThroughID, nil
}

// advanceLedgerRetentionCursorSQL moves a cursor forward only: two replicas
// finishing out of order leave it at the further of the two.
const advanceLedgerRetentionCursorSQL = `
INSERT INTO ledger_retention_cursors AS c (name, through_at, through_id, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (name) DO UPDATE
SET through_at = EXCLUDED.through_at, through_id = EXCLUDED.through_id, updated_at = now()
WHERE (c.through_at, c.through_id) < (EXCLUDED.through_at, EXCLUDED.through_id)`

// AdvanceLedgerRetentionCursor records that the pass name is done through
// (at, id). It never moves the cursor back.
func AdvanceLedgerRetentionCursor(ctx context.Context, db orm.Context, name string, at time.Time, id string) error {
	if _, err := db.Exec(ctx, advanceLedgerRetentionCursorSQL, name, at.UTC(), id); err != nil {
		return fmt.Errorf("advance retention cursor %s: %w", name, err)
	}
	return nil
}

// LedgerRetentionCursorExtra carries fields on LedgerRetentionCursor that are NOT columns — computed
// values, transient state, anything derived rather than stored. LedgerRetentionCursor is
// embedded on LedgerRetentionCursorExtra (see ledger_retention_cursor_orm.go), so a field added here is a field on
// every LedgerRetentionCursor value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type LedgerRetentionCursorExtra struct {
}
