package seedplan

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Clear deletes rows whose primary keys match plan, without seeding again.
// When onlyTable is nonempty, it names exactly one table in the plan; key
// generation still uses the full plan because a key can contain a foreign key.
// A table without a primary key the plan writes is skipped and reported.
//
// A match is not proof of provenance: a user or application row with the
// same key (notably a small integer) is indistinguishable and is deleted too.
// No table is truncated. Deletes are child-first in one transaction and
// PostgreSQL's own ON DELETE actions still apply to referencing rows. A guard
// trigger (e.g. append-only) or a restrictive FK rolls back the whole clear.
func Clear(ctx context.Context, db *sql.DB, plan *Plan, onlyTable string) (deleted int64, skipped []string, err error) {
	if onlyTable != "" {
		matches := 0
		for _, tp := range plan.tables {
			if tp.table.Name == onlyTable {
				matches++
			}
		}
		if matches != 1 {
			return 0, nil, fmt.Errorf("clear: --table %q does not uniquely name a table in the seed plan", onlyTable)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for i := len(plan.tables) - 1; i >= 0; i-- {
		tp := plan.tables[i]
		if tp.n == 0 || (onlyTable != "" && tp.table.Name != onlyTable) {
			continue
		}
		stmt := plan.clearStatement(tp)
		if stmt == "" {
			skipped = append(skipped, tp.table.Name)
			continue
		}
		res, err := tx.ExecContext(ctx, stmt)
		if err != nil {
			return 0, nil, fmt.Errorf("clear %s (transaction rolled back): %w", tp.table.Name, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, nil, fmt.Errorf("count deleted rows in %s: %w", tp.table.Name, err)
		}
		deleted += n
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return deleted, skipped, nil
}

// clearStatement renders a key match with the same literals as the INSERT.
// An empty statement means the plan cannot identify its rows in this table.
func (p *Plan) clearStatement(tp tablePlan) string {
	if len(tp.table.PKCols) == 0 {
		return ""
	}
	byName := make(map[string]columnPlan, len(tp.cols))
	for _, cp := range tp.cols {
		byName[cp.col.Name] = cp
	}
	cols := make([]columnPlan, 0, len(tp.table.PKCols))
	for _, name := range tp.table.PKCols {
		cp, ok := byName[name]
		if !ok || cp.omit {
			return ""
		}
		cols = append(cols, cp)
	}
	names := make([]string, len(cols))
	for i, cp := range cols {
		names[i] = quoteIdent(cp.col.Name)
	}
	rows := make([]string, tp.n)
	for i := range rows {
		vals := make([]string, len(cols))
		for j, cp := range cols {
			vals[j] = p.cellLiteral(tp, cp, i, 0)
		}
		rows[i] = "(" + strings.Join(vals, ", ") + ")"
	}
	return fmt.Sprintf("DELETE FROM %s WHERE (%s) IN (%s)", QualifiedTable(tp.table), strings.Join(names, ", "), strings.Join(rows, ", "))
}
