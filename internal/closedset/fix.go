package closedset

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

// Fix is the forward migration that brings a CHECK back to its set.
type Fix struct {
	// BackwardCompatible is true when the fix only widens the CHECK: every
	// row the previous release wrote still satisfies it. Reason says why,
	// either way — the text of the migration's compatibility declaration.
	BackwardCompatible bool
	Reason             string
	// SQL is the migration body: the CHECK replaced whole, and the
	// declaration re-issued (postgres drops a constraint's comment with it).
	SQL string
	// Migration is the whole file, compatibility declaration first, ready to
	// paste into a new `forge db migration new` file.
	Migration string
}

func newFix(compatible bool, reason, sql string) *Fix {
	decl := compat.BreaksRollbackDirective + " " + reason
	if compatible {
		decl = compat.Directive + " — " + reason
	}
	return &Fix{BackwardCompatible: compatible, Reason: reason, SQL: sql, Migration: decl + "\n\n" + sql}
}

// buildFix writes the migration that replaces c's CHECK with one listing
// want, keeping everything else the expression says verbatim.
func buildFix(c *constraint, d declaration, list List, want, missing, extra []string) *Fix {
	expr := c.expr[:list.Start] + renderMembers(list, want) + c.expr[list.End:]
	var (
		b          strings.Builder
		reason     string
		compatible = len(extra) == 0
	)
	if compatible {
		reason = fmt.Sprintf("widens %s to exactly %s (adds %s); every row the previous release wrote still satisfies it.",
			c.name, d.set, quoteAll(missing))
	} else {
		reason = fmt.Sprintf("narrows %s to exactly %s (drops %s); the previous release can still write them, and this CHECK refuses them.",
			c.name, d.set, quoteAll(extra))
	}
	fmt.Fprintf(&b, "-- %s on %s mirrors forge:closed-set %s.\n", c.name, c.table, d.set)
	b.WriteString("-- Replace it with a CHECK that lists exactly that set, in the set's order.\n")
	b.WriteString("-- DROP and ADD are one ALTER TABLE: one lock, and no instant\n")
	b.WriteString("-- without the constraint. postgres drops a constraint's comment with it, so the\n")
	b.WriteString("-- declaration is re-issued after.\n")
	if len(extra) > 0 {
		fmt.Fprintf(&b, "-- A row holding %s fails the ADD CONSTRAINT, and with it this migration:\n", quoteAll(extra))
		b.WriteString("-- rewrite or delete those rows first, above the ALTER TABLE.\n")
	}
	fmt.Fprintf(&b, "ALTER TABLE %s\n    DROP CONSTRAINT %s,\n    ADD CONSTRAINT %s CHECK (%s);\n", c.table, c.name, c.name, expr)
	b.WriteString(commentStatement(c, d))
	return newFix(compatible, reason, b.String())
}

// commentOnlyFix re-issues a declaration a re-created constraint lost.
func commentOnlyFix(c *constraint, d declaration) *Fix {
	return newFix(true,
		fmt.Sprintf("re-declares %s's closed set; a comment changes no row and nothing the previous release reads.", c.name),
		commentStatement(c, d))
}

func commentStatement(c *constraint, d declaration) string {
	return fmt.Sprintf("COMMENT ON CONSTRAINT %s ON %s IS\n    '%s';\n", c.name, c.table, d.comment)
}

// renderMembers spells want as the members of list, in list's layout: the
// same per-member cast, and one member per line when the CHECK put them on
// separate lines.
func renderMembers(list List, want []string) string {
	if list.ArrayLiteral {
		quoted := make([]string, len(want))
		for i, v := range want {
			quoted[i] = arrayLiteralMember(v)
		}
		return "'{" + strings.ReplaceAll(strings.Join(quoted, ","), "'", "''") + "}'"
	}
	members := make([]string, len(want))
	for i, v := range want {
		members[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'" + list.Cast
	}
	sep := ", "
	if list.Multiline {
		sep = ",\n" + list.Indent
	}
	return strings.Join(members, sep)
}

// arrayLiteralMember quotes a member of a postgres array literal when its
// text needs it.
func arrayLiteralMember(v string) string {
	if v == "" || strings.ContainsAny(v, `{},"\ `) || strings.EqualFold(v, "null") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
	}
	return v
}
