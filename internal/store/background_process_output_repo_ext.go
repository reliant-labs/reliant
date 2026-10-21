// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for BackgroundProcessOutput. The generated background_process_output_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: backgroundProcessOutputRepo (a *crud.Repo[BackgroundProcessOutput]) and the
//     package-level GetBackgroundProcessOutputByID / ListBackgroundProcessOutput / CreateBackgroundProcessOutput / UpdateBackgroundProcessOutput / DeleteBackgroundProcessOutput
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     BackgroundProcessOutput struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindBackgroundProcessOutputBy(ctx context.Context, db orm.Context, /* args */) ([]*BackgroundProcessOutput, error) {
//		var rows []*BackgroundProcessOutput
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("process_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveBackgroundProcessOutputs(ctx context.Context, db orm.Context) ([]*BackgroundProcessOutput, error) {
//		return ListBackgroundProcessOutput(ctx, db, orm.WhereEq("process_id", true))
//	}
package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// BackgroundProcessOutputExtra carries fields on BackgroundProcessOutput that are NOT columns — computed
// values, transient state, anything derived rather than stored. BackgroundProcessOutput is
// embedded on BackgroundProcessOutputExtra (see background_process_output_orm.go), so a field added here is a field on
// every BackgroundProcessOutput value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type BackgroundProcessOutputExtra struct {
}

// Process output arrives in batches for as long as a process runs and is
// polled for as long as someone watches it, so both statements bind their
// values ($N through orm.Context's raw passthrough) rather than let bun
// format a new statement text per batch and per poll (internal/db/SCHEMA.md,
// rule 2).

// InsertBackgroundProcessOutput appends a batch of output lines in one
// statement: one unnest over four bound arrays, so the statement text is the
// same whatever the batch size.
func InsertBackgroundProcessOutput(ctx context.Context, db orm.Context, lines []core.BackgroundProcessOutputLine) error {
	if len(lines) == 0 {
		return nil
	}
	processIDs := make([]string, len(lines))
	seqs := make([]int64, len(lines))
	streams := make([]string, len(lines))
	texts := make([]string, len(lines))
	for i, l := range lines {
		processIDs[i], seqs[i], streams[i], texts[i] = l.ProcessID, l.Seq, l.Stream, l.Line
	}
	_, err := db.Exec(ctx, `
		INSERT INTO background_process_output (process_id, seq, stream, line)
		SELECT * FROM unnest($1::text[], $2::bigint[], $3::text[], $4::text[])`,
		processIDs, seqs, streams, texts)
	return err
}

// ReadBackgroundProcessOutput returns up to limit of the process's output
// lines after afterSeq, in order.
func ReadBackgroundProcessOutput(ctx context.Context, db orm.Context, processID string, afterSeq int64, limit int) ([]core.BackgroundProcessOutputLine, error) {
	rows, err := db.Query(ctx, `
		SELECT id, process_id, seq, stream, line, created_at
		FROM background_process_output
		WHERE process_id = $1 AND seq > $2
		ORDER BY seq ASC
		LIMIT $3`,
		processID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.BackgroundProcessOutputLine
	for rows.Next() {
		var l core.BackgroundProcessOutputLine
		if err := rows.Scan(&l.ID, &l.ProcessID, &l.Seq, &l.Stream, &l.Line, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
