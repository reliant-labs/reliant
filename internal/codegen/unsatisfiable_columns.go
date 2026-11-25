// File: internal/codegen/unsatisfiable_columns.go
//
// UNSATISFIABLE COLUMN — a NOT NULL column with no DB DEFAULT that ALSO
// carries `forge:read-only` is provably impossible to INSERT through the
// generated CRUD path: the
// marker's only mechanical effect is a WIRE-shape omission — it strips the
// field from the born Create request (correct: the client must not set
// it) — but nothing then FILLS the column. Bun writes the Go zero value,
// which either violates the NOT NULL/CHECK outright or (worse, for a
// foreign key) satisfies the Go type check and fails at the database as a
// constraint violation that reads like a caller-side bug.
//
// All three facts — NOT NULL, DEFAULT (or its absence), and the read-only
// marker — are things forge already knows at generate time from the joined
// wire+schema EntityDef (see BuildSchemaEntities), so this fires for every
// project regardless of whether anyone has ever heard of `forge:fill`.
//
// Declaring `forge:fill=<strategy>` on the column (a COMMENT ON COLUMN
// catalog marker, schemadef.ColumnMarkerFill) suppresses this: either forge
// fills it itself (`forge:fill=ulid`, see forge/pkg/crud's
// Repo.fillULIDColumns) or the author has stamped it in their own handler
// and is acknowledging the gap (`forge:fill=handler`).
//
// The `forge:owner` column of an authenticated Create needs neither: the
// owner-scoping wrapper handlers_crud.go is scaffolded with stamps it from
// the caller (`e.<Owner> = owner`), so an owner column is filled by the
// handler by definition — and is read-only by intent, since a create that
// took it off the wire would let anyone file a row under anyone's name. The
// caller passes that column as `stamped`. A PUBLIC Create gets no wrapper,
// so its owner column is held to the rule like any other. A handlers_crud.go
// that predates the owner declaration has no wrapper either; `forge project
// audit`'s unscoped_auth gate fails that rpc until it gets the one the
// audit prints.
package codegen

import (
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// UnsatisfiableColumn is one column forge can prove no generated Create can
// ever populate: NOT NULL, no DB DEFAULT, excluded from the wire by
// `forge:read-only`, and declaring no `forge:fill=` strategy.
type UnsatisfiableColumn struct {
	Entity string // "Customer"
	Table  string // "customers"
	Column string // "company_id"
}

// FindUnsatisfiableColumns reports every column of entity that is provably
// impossible to satisfy through the generated Create path.
//
// A column with no paired wire field at all (plain schema forge never put
// on the API) is out of scope: this check is specifically about the gap
// `forge:read-only` leaves, not about every column a Create request
// happens not to carry — an ordinary internal/denormalized column absent
// from the API is unaffected by any of this and is not a bug.
//
// stamped names the column the scaffolded Create wrapper assigns itself —
// the entity's forge:owner column when the Create is scoped — or "" for
// none; it is never reported.
func FindUnsatisfiableColumns(entity EntityDef, stamped string) []UnsatisfiableColumn {
	readOnly := make(map[string]bool, len(entity.Fields))
	for _, f := range entity.Fields {
		if f.ReadOnly {
			readOnly[f.Name] = true
		}
	}
	var out []UnsatisfiableColumn
	for _, col := range entity.Columns {
		if !readOnly[col.Name] {
			continue
		}
		if col.IsPK || col.IsGenerated || !col.NotNull {
			continue
		}
		if strings.TrimSpace(col.Default) != "" {
			continue
		}
		if col.FillStrategy != "" || col.Name == stamped {
			continue
		}
		out = append(out, UnsatisfiableColumn{Entity: entity.Name, Table: entity.TableName, Column: col.Name})
	}
	return out
}

// FillHandlerColumns names the entity's columns declaring
// `forge:fill=handler` — the exact set FindUnsatisfiableColumns would have
// flagged but for that declaration (NOT NULL, no DB DEFAULT,
// forge:read-only, not the PK, not GENERATED). Used to seed the
// handlers_crud.go Create scaffold's op.Entity wrapper (see
// ensureCRUDShimFile) with a named reminder of what the handler author
// still has to stamp. stamped (see FindUnsatisfiableColumns) is left out:
// the scoping wrapper already assigns it, so a reminder to would be a second
// assignment the author is told to write.
func FillHandlerColumns(entity EntityDef, stamped string) []string {
	readOnly := make(map[string]bool, len(entity.Fields))
	for _, f := range entity.Fields {
		if f.ReadOnly {
			readOnly[f.Name] = true
		}
	}
	var out []string
	for _, col := range entity.Columns {
		if !readOnly[col.Name] || col.IsPK || col.IsGenerated || !col.NotNull {
			continue
		}
		if strings.TrimSpace(col.Default) != "" {
			continue
		}
		if col.FillStrategy != schemadef.FillStrategyHandler || col.Name == stamped {
			continue
		}
		out = append(out, col.Name)
	}
	return out
}

// UnsatisfiableColumnsError renders a set of UnsatisfiableColumn findings as
// the error `forge generate` fails with. nil for an empty set. Every column
// is listed rather than just the first, so one run names every broken
// column instead of one per regenerate.
func UnsatisfiableColumnsError(cols []UnsatisfiableColumn) error {
	if len(cols) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d column(s) can never be populated by the generated Create path:\n", len(cols))
	for _, c := range cols {
		fmt.Fprintf(&b, "  - %s.%s (entity %s): NOT NULL, no DB DEFAULT, and forge:read-only — nothing assigns it, so every Create fails (NOT NULL / constraint violation)\n",
			c.Table, c.Column, c.Entity)
	}
	b.WriteString("Fix one of two ways: give the column a DB DEFAULT in a migration, or declare who fills it — " +
		"`COMMENT ON COLUMN <table>.<col> IS 'forge:fill=handler';` and stamp it yourself in the handler's op.Entity, " +
		"or `forge:fill=ulid` to have forge generate one at Create. A column already carrying another marker takes " +
		"both, space-separated, in its one comment (postgres keeps one per column): `IS 'forge:owner forge:fill=handler'`. " +
		"An owner column needs neither when its Create is authenticated: the scoping wrapper stamps it from the caller.")
	return errors.New(b.String())
}
