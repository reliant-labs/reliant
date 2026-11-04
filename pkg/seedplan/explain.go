package seedplan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Why a rejected seed row explains itself.
//
// Both halves of a forge project's data are forge's: the migration that
// declares the constraint and the synthesizer that has to satisfy it. When
// they disagree, postgres reports the disagreement in the only vocabulary it
// has — `new row for relation "prescriptions" violates check constraint
// "prescriptions_validity_window"` — which leaves the reader to work out
// which of forty columns forge got wrong, and against which rule.
//
// forge already knows all of it: it holds the introspected constraint
// definition, the columns the constraint spans, and the literal it planned
// for each of them. Printing them turns a five-minute bisect into a glance.
// A measured real-workflow run spent roughly a third of its scaffold budget
// on exactly this class of repair.

// pgViolationRE reads postgres's integrity-violation messages, whichever
// driver relays them:
//
//	new row for relation "prescriptions" violates check constraint "prescriptions_validity_window"
//	duplicate key value violates unique constraint "patients_email_key"
//	insert or update on table "orders" violates foreign key constraint "orders_patient_id_fkey"
//	null value in column "enrolled_on" of relation "patients" violates not-null constraint
var (
	pgViolationRE = regexp.MustCompile(`violates (check|unique|foreign key|not-null) constraint(?: "([^"]+)")?`)
	pgColumnRE    = regexp.MustCompile(`column "([^"]+)"`)
)

// explainInsertFailure wraps a rejected table INSERT with everything forge
// knows about the disagreement. A failure it cannot decode degrades to the
// plain driver error — never to silence.
func (p *Plan) explainInsertFailure(tp tablePlan, err error) error {
	table := tp.table.Name
	msg := err.Error()
	m := pgViolationRE.FindStringSubmatch(msg)
	if m == nil {
		return fmt.Errorf("seed %s: %w", table, err)
	}
	kind, name := m[1], m[2]

	var b strings.Builder
	fmt.Fprintf(&b, "seed %s: %v", table, err)
	fmt.Fprintf(&b, "\n\nforge generated both this schema and this data, and they disagree.\n")
	if name != "" {
		fmt.Fprintf(&b, "  constraint: %s (%s)\n", name, kind)
	} else {
		fmt.Fprintf(&b, "  constraint: %s\n", kind)
	}
	cols := p.constraintColumns(table, name)
	if len(cols) == 0 {
		if cm := pgColumnRE.FindStringSubmatch(msg); cm != nil {
			cols = []string{cm[1]}
		}
	}
	if def := p.constraintDef(table, name); def != "" {
		fmt.Fprintf(&b, "  definition: %s\n", def)
	}
	if len(cols) == 0 {
		return fmt.Errorf("%s", b.String())
	}
	sort.Strings(cols)
	fmt.Fprintf(&b, "  columns:    %s\n", strings.Join(cols, ", "))
	b.WriteString("  values forge planned:\n")
	for row := 0; row < 3 && row < tp.n; row++ {
		cells := make([]string, 0, len(cols))
		for _, c := range cols {
			v, ok := p.SeedValue(table, c, row)
			if !ok {
				cells = append(cells, c+" = NULL")
				continue
			}
			cells = append(cells, fmt.Sprintf("%s = %q", c, v))
		}
		fmt.Fprintf(&b, "    row %d: %s\n", row, strings.Join(cells, ", "))
	}
	return fmt.Errorf("%s", b.String())
}

// constraintDef returns the introspected definition of a named constraint.
func (p *Plan) constraintDef(table, name string) string {
	t, ok := p.byName[table]
	if !ok || name == "" {
		return ""
	}
	for _, ck := range t.Checks {
		if ck.Name == name {
			return ck.Def
		}
	}
	for _, fk := range t.ForeignKeys {
		if fk.Name == name {
			return fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)", fk.Column, fk.RefTable, fk.RefColumn)
		}
	}
	for _, ix := range t.Indexes {
		if ix.Name == name && ix.Unique {
			// Columns is only the bare-column subset of an expression
			// index, so rendering it as the whole key would print a
			// constraint the table does not have (or, for an
			// all-expression index, an empty one).
			if ix.Expression {
				return "UNIQUE (expression)"
			}
			return fmt.Sprintf("UNIQUE (%s)", strings.Join(ix.Columns, ", "))
		}
	}
	return ""
}

// constraintColumns returns the columns a named constraint spans.
func (p *Plan) constraintColumns(table, name string) []string {
	t, ok := p.byName[table]
	if !ok || name == "" {
		return nil
	}
	for _, ck := range t.Checks {
		if ck.Name == name {
			return append([]string(nil), ck.Columns...)
		}
	}
	for _, ix := range t.Indexes {
		if ix.Name == name {
			return append([]string(nil), ix.Columns...)
		}
	}
	for _, fk := range t.ForeignKeys {
		if fk.Name == name {
			return []string{fk.Column}
		}
	}
	return nil
}
