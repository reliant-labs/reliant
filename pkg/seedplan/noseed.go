package seedplan

import (
	"fmt"
	"sort"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// ExcludeNoSeed removes from tables every table the development dataset must
// not write: each one declaring `COMMENT ON TABLE t IS 'forge:no-seed'`
// (schemadef.TableMarkerNoSeed), and every table that cannot hold a row
// without one — a NOT NULL foreign key into an excluded table, transitively.
// A NULLABLE reference into an excluded table excludes nothing: the plan
// writes it NULL, as it does any reference to a table it does not seed.
//
// warnings names each table left out only because of what it references, and
// why; a table that declares the marker itself is left out in silence, since
// its author asked. The result keeps the input's table order.
func ExcludeNoSeed(tables []schemadef.Table) (kept []schemadef.Table, warnings []string) {
	kept, _, warnings = excludeNoSeed(tables)
	return kept, warnings
}

// excludeNoSeed is ExcludeNoSeed plus the names it left out, in input order.
func excludeNoSeed(tables []schemadef.Table) (kept []schemadef.Table, left []string, warnings []string) {
	excluded := map[string]string{} // table -> "" (marked) or the reason
	for _, t := range tables {
		if t.NoSeed() {
			excluded[t.Name] = ""
		}
	}
	if len(excluded) == 0 {
		return tables, nil, nil
	}
	for changed := true; changed; {
		changed = false
		for _, t := range tables {
			if _, out := excluded[t.Name]; out {
				continue
			}
			if fk, ok := requiredRefInto(t, excluded); ok {
				excluded[t.Name] = fmt.Sprintf("its NOT NULL foreign key %s references %s", fk.Column, fk.RefTable)
				changed = true
			}
		}
	}
	kept = make([]schemadef.Table, 0, len(tables))
	for _, t := range tables {
		reason, out := excluded[t.Name]
		if !out {
			kept = append(kept, t)
			continue
		}
		left = append(left, t.Name)
		if reason != "" {
			warnings = append(warnings, fmt.Sprintf("seed plan: %s not seeded: %s, which is not seeded (%s)", t.Name, reason, schemadef.TableMarkerNoSeed))
		}
	}
	sort.Strings(warnings)
	return kept, left, warnings
}

// NoSeedTables names the tables the development dataset left out because
// they declare `forge:no-seed` or cannot exist without one (ExcludeNoSeed),
// in the schema's table order. Empty for a plan that is not the development dataset.
func (p *Plan) NoSeedTables() []string { return append([]string(nil), p.noSeed...) }

// requiredRefInto returns t's first NOT NULL foreign key (in column order)
// whose parent is in excluded. A self-reference never counts: it cannot be
// what excludes the table.
func requiredRefInto(t schemadef.Table, excluded map[string]string) (schemadef.ForeignKey, bool) {
	columns := make(map[string]schemadef.Column, len(t.Columns))
	for _, c := range t.Columns {
		columns[c.Name] = c
	}
	for _, fk := range t.ForeignKeys {
		if fk.RefTable == t.Name || !columns[fk.Column].NotNull ||
			(len(fk.KeyColumns) > 1 && compositeNullableMember(t, fk, columns) != "") {
			continue
		}
		if _, out := excluded[fk.RefTable]; out {
			return fk, true
		}
	}
	return schemadef.ForeignKey{}, false
}
