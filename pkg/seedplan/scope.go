package seedplan

import "github.com/reliant-labs/forge/pkg/schemadef"

// ScopeTables narrows a schema to the tables a scoped seed may write: every
// root that exists, plus the transitive closure of the tables they reference
// through a NOT NULL foreign key.
//
// Only NOT NULL references pull a parent in. A row cannot be inserted without
// its required parent, so leaving one out would make the plan unsatisfiable
// (BuildPlan refuses a NOT NULL reference to an unseeded table). A NULLABLE
// reference is different — BuildPlan writes it NULL — and following it would
// widen the seed into exactly the tables the scope exists to leave alone.
//
// Unknown roots are ignored: the caller's list may name an entity whose
// table a later migration has not created yet. The result keeps the input's
// table order.
func ScopeTables(tables []schemadef.Table, roots []string) []schemadef.Table {
	byName := make(map[string]schemadef.Table, len(tables))
	for _, t := range tables {
		byName[t.Name] = t
	}
	keep := map[string]bool{}
	queue := make([]string, 0, len(roots))
	for _, r := range roots {
		if _, ok := byName[r]; ok && !keep[r] {
			keep[r] = true
			queue = append(queue, r)
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		t := byName[name]
		notNull := make(map[string]bool, len(t.Columns))
		for _, c := range t.Columns {
			notNull[c.Name] = c.NotNull
		}
		for _, fk := range t.ForeignKeys {
			if !notNull[fk.Column] || keep[fk.RefTable] {
				continue
			}
			if _, ok := byName[fk.RefTable]; !ok {
				continue
			}
			keep[fk.RefTable] = true
			queue = append(queue, fk.RefTable)
		}
	}
	out := make([]schemadef.Table, 0, len(keep))
	for _, t := range tables {
		if keep[t.Name] {
			out = append(out, t)
		}
	}
	return out
}
