package codegen

import (
	"sort"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// EntityConstraintsFromTable projects an introspected table's NAMED
// constraints — the ones a service branches on when a write comes back as a
// driver error.
//
// The kinds are chosen by what a caller can DO with the violation, not by
// what the catalog happens to record:
//
//   - UNIQUE (23505) is a lost race. It is the whole reason forge tells you
//     to declare the constraint: the database is the only thing that can
//     arbitrate two concurrent writes, and the loser maps to AlreadyExists.
//   - CHECK (23514) is a declared domain invariant, so violating it is the
//     caller's bad input and maps to InvalidArgument.
//   - FOREIGN KEY (23503) means the parent row is absent or still
//     referenced, which is a caller-visible NotFound / FailedPrecondition
//     rather than a server fault.
//
// The PRIMARY KEY is deliberately excluded. A duplicate-PK insert is not a
// domain event any service branches on — pkg/crud generates the id — so a
// generated constant for it would be noise in every entity file forever.
// NOT NULL is excluded because postgres names no constraint for it at all
// (it reports the column instead), so orm.ConstraintName returns "" and
// there is nothing to compare against.
//
// A plain `CREATE UNIQUE INDEX` is included alongside a UNIQUE constraint
// even though the catalog does not consider it a constraint: postgres
// reports the INDEX name in the violation's constraint field, so from the
// error-handling side the two are indistinguishable, and omitting one would
// leave exactly the case that looks like the other unserved.
func EntityConstraintsFromTable(table schemadef.Table) []EntityConstraint {
	var out []EntityConstraint

	for _, ix := range table.Indexes {
		if !ix.Unique {
			continue
		}
		// Columns holds only the bare-column subset of an expression
		// index's key (schemadef.Index.Expression), so it must not be
		// published as the whole key — an under-described key reads as a
		// STRICTER constraint than the table has. The NAME is exact
		// either way, and the name is what the constant carries.
		cols := ix.Columns
		if ix.Expression {
			cols = nil
		}
		out = append(out, EntityConstraint{
			Name:    ix.Name,
			Kind:    string(config.ConstraintKindUnique),
			Columns: cols,
		})
	}

	for _, ck := range table.Checks {
		out = append(out, EntityConstraint{
			Name:    ck.Name,
			Kind:    string(config.ConstraintKindCheck),
			Columns: ck.Columns,
		})
	}

	// A composite FK is reported by introspection as one row per column
	// under a shared constraint name; the constant is per NAME, so the
	// columns are folded back together rather than emitting the name twice
	// (a duplicate const does not compile).
	fkCols := map[string][]string{}
	var fkOrder []string
	for _, fk := range table.ForeignKeys {
		if fk.Name == "" {
			continue
		}
		if _, seen := fkCols[fk.Name]; !seen {
			fkOrder = append(fkOrder, fk.Name)
		}
		fkCols[fk.Name] = append(fkCols[fk.Name], fk.Column)
	}
	for _, name := range fkOrder {
		out = append(out, EntityConstraint{
			Name:    name,
			Kind:    string(config.ConstraintKindForeignKey),
			Columns: fkCols[name],
		})
	}

	// Sorted by name so the generated file is stable across runs: the
	// catalog's row order is not guaranteed, and an unstable const block
	// would show up as spurious drift in every `forge generate` diff.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// EntityConstraintsToPlan converts introspected constraints to the plan
// shape the ORM generator consumes.
func EntityConstraintsToPlan(cs []EntityConstraint) []config.PlanEntityConstraint {
	if len(cs) == 0 {
		return nil
	}
	out := make([]config.PlanEntityConstraint, 0, len(cs))
	for _, c := range cs {
		out = append(out, config.PlanEntityConstraint{
			Name:    c.Name,
			Kind:    config.ConstraintKind(c.Kind),
			Columns: c.Columns,
		})
	}
	return out
}
