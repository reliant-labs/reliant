// Copyright (c) 2025 Reliant Labs
package store

import (
	"reflect"
	"strings"

	"github.com/uptrace/bun/dialect/pgdialect"
)

// No query here selects a relation's columns with a star (`t.*`, `SELECT *`,
// `RETURNING *`). A star is expanded when the statement is PREPARED, and
// prepared statements outlive migrations: pgx caches them per connection, and
// pgbouncer keeps them prepared on its server connections across clients. A
// migration that adds a column to the relation changes the star's expansion,
// so every statement prepared before it fails with "cached plan must not
// change result type" until its connection is recycled — chats_with_activity
// gaining working_threads broke ListArchivedChats that way in prod
// (2026-10-22). A named column list keeps the result type the statement was
// prepared with. internal/architecture's TestNoStarSelects holds the line.
//
// The list is the model's, never hand-typed: a builder query spells it
// ColumnExpr("?TableColumns") / Returning("?TableColumns"), which bun expands
// from the query's model; raw SQL takes columnsOf.

// modelTables reads the bun schema of a model, as the query builder does.
var modelTables = pgdialect.New().Tables()

// columnsOf is T's columns — the fields of its generated bun model, which
// forge projects from the applied schema, scanonly fields excluded —
// qualified by alias: `t."id", t."title", …`. It is for raw SQL that scans
// into T (or a struct embedding it); see the note above.
func columnsOf[T any](alias string) string {
	var b strings.Builder
	for i, f := range modelTables.Get(reflect.TypeFor[T]()).Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(alias)
		b.WriteByte('.')
		b.WriteString(string(f.SQLName))
	}
	return b.String()
}
