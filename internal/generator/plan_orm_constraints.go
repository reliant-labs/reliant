package generator

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// writeConstraintConstants emits the per-entity constraint-name constants:
// `<Entity>Constraint<Name>`, the sibling of the `<Entity>Field<Col>` block
// just above it.
//
// They exist because orm.ConstraintName(err) returns a string and a service
// has to compare it against SOMETHING. Without a generated constant the only
// option is a literal beside the branch, which is precisely the drift the
// column constants exist to prevent: rename the constraint in a migration
// and the build stays green while the branch silently stops matching. The
// constant makes the rename a compile error at every reader.
//
// The value is postgres's own name, carried verbatim from the catalog. Forge
// never derives it — postgres auto-names inline constraints (`_key`,
// `_check`, `_check1`), and a name forge computed that disagreed with the
// one postgres reports would be worse than no constant at all, because it
// reads as authoritative.
func writeConstraintConstants(b *strings.Builder, msgName string, constraints []config.PlanEntityConstraint) {
	if len(constraints) == 0 {
		return
	}

	names := constraintConstNames(msgName, constraints)

	fmt.Fprintf(b, "// %sConstraint* name the declared constraints on the %s table, for\n", msgName, msgName)
	b.WriteString("// callers classifying a write failure with forge/pkg/orm:\n")
	b.WriteString("//\n")
	fmt.Fprintf(b, "//\tif orm.ConstraintName(err) == %s { ... }\n", names[0])
	b.WriteString("//\n")
	b.WriteString("// The values are the names POSTGRES reports in a violation, read back\n")
	b.WriteString("// from the applied schema — so renaming a constraint in a migration is a\n")
	b.WriteString("// compile error here rather than a branch that silently stops matching.\n")
	b.WriteString("const (\n")
	for i, c := range constraints {
		fmt.Fprintf(b, "\t// %s is the %s.\n", names[i], constraintDescription(c))
		fmt.Fprintf(b, "\t%s = %q\n", names[i], c.Name)
	}
	b.WriteString(")\n\n")
}

// constraintDescription is the human half of a constraint constant's doc
// comment: what violating it MEANS, so a reader picking a sentinel does not
// have to open the migration to find out whether this is a lost race or bad
// input.
func constraintDescription(c config.PlanEntityConstraint) string {
	cols := strings.Join(c.Columns, ", ")
	switch c.Kind {
	case config.ConstraintKindUnique:
		if cols == "" {
			return "UNIQUE constraint " + c.Name + " (violation: a lost write race)"
		}
		return "UNIQUE constraint over (" + cols + ") (violation: a lost write race)"
	case config.ConstraintKindCheck:
		if cols == "" {
			return "CHECK constraint " + c.Name + " (violation: invalid input)"
		}
		return "CHECK constraint over (" + cols + ") (violation: invalid input)"
	case config.ConstraintKindForeignKey:
		if cols == "" {
			return "FOREIGN KEY constraint " + c.Name + " (violation: the referenced row is absent or still referenced)"
		}
		return "FOREIGN KEY constraint over (" + cols + ") (violation: the referenced row is absent or still referenced)"
	}
	return "constraint " + c.Name
}

// constraintConstNames picks the Go identifier for each constraint, returned
// positionally alongside the input.
//
// Postgres prefixes an auto-derived name with the table
// (`jobs_estimate_id_key`), and on the `Job` entity that would read as
// `JobConstraintJobsEstimateIDKey` — the table said twice. The prefix is
// stripped so the constant reads as a sibling of `JobFieldEstimateID`.
//
// Stripping can make two distinct names collide on one identifier
// (`jobs_status_key` and a hand-named `status_key` both reduce to
// `StatusKey`). Two consts of the same name do not compile, and silently
// picking a winner would publish one constraint's VALUE under the other's
// spelling — the exact failure this whole feature exists to prevent. So a
// colliding pair falls back to the FULL name on both sides, which postgres
// guarantees is unique within a table.
func constraintConstNames(msgName string, constraints []config.PlanEntityConstraint) []string {
	tablePrefix := naming.ToSnakeCase(naming.Pluralize(msgName)) + "_"

	// First pass: the preferred (stripped) identifier for each constraint,
	// and how many constraints want it.
	stripped := make([]string, len(constraints))
	want := map[string]int{}
	for i, c := range constraints {
		bare := strings.TrimPrefix(c.Name, tablePrefix)
		// A name that is ENTIRELY the prefix leaves nothing to name the
		// constant after, so it keeps the full name.
		if bare == "" {
			bare = c.Name
		}
		stripped[i] = constraintIdent(msgName, bare)
		want[stripped[i]]++
	}

	names := make([]string, len(constraints))
	for i, c := range constraints {
		if want[stripped[i]] > 1 {
			names[i] = constraintIdent(msgName, c.Name)
			continue
		}
		names[i] = stripped[i]
	}
	return names
}

// constraintIdent builds `<Entity>Constraint<PascalCase(name)>` using the
// SAME casing function the column constants and the entity struct's fields
// use — naming.ColumnGoName, which does not apply initialisms. So an
// estimate_id constraint reads `JobConstraintEstimateIdKey`, beside
// `JobFieldEstimateId` and the struct's `EstimateId`. Reaching for
// naming.ToPascalCase here would spell the same column `EstimateID` in the
// constraint constant and `EstimateId` three lines above it.
func constraintIdent(msgName, constraintName string) string {
	return msgName + "Constraint" + naming.ColumnGoName(constraintName)
}
