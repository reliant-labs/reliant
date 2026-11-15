package seedplan

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// compositeMembers expands one composite constraint into the per-member
// entries schemadef reports for it.
func compositeMembers(name, refTable string, cols, refCols []string) []schemadef.ForeignKey {
	out := make([]schemadef.ForeignKey, len(cols))
	for i := range cols {
		out[i] = schemadef.ForeignKey{
			Column: cols[i], RefTable: refTable, RefColumn: refCols[i],
			KeyColumns: cols, RefKeyColumns: refCols, Name: name,
		}
	}
	return out
}

// scalarFKs lists the single-column references of a table as
// "column->table.column", in declaration order.
func scalarFKs(t schemadef.Table) []string {
	var out []string
	for _, fk := range t.ForeignKeys {
		if len(fk.KeyColumns) > 1 {
			continue
		}
		out = append(out, fk.Column+"->"+fk.RefTable+"."+fk.RefColumn)
	}
	return out
}

// A declined composite keeps only the member BuildPlan set NULL for THAT
// constraint. A sibling member that is NULL for an unrelated reason (here
// it is simply nullable, and forceNull may hold it NULL for another edge)
// is no reference to the composite's table: keeping it would be a false
// edge that fkByCol and placement read as real.
func TestDeclineCompositeFKs_KeepsOnlyTheSelectedMember(t *testing.T) {
	tb := schemadef.Table{Name: "child"}
	tb.ForeignKeys = compositeMembers("fk_child_parent", "parent", []string{"a", "b"}, []string{"x", "y"})
	byName := map[string]schemadef.Table{"child": tb}
	out := declineCompositeFKs([]schemadef.Table{tb}, byName, map[string]map[string]string{
		"child": {"fk_child_parent": "a"},
	})
	if got, want := scalarFKs(out[0]), []string{"a->parent.x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scalar FKs = %v, want %v (only the selected member)", got, want)
	}
	if len(out[0].ForeignKeys) != 1 {
		t.Fatalf("ForeignKeys = %+v, want the one retained member", out[0].ForeignKeys)
	}
}

// Two declined composites that share a member column: each keeps only its
// own selected member, and a column is never given two references — the
// first kept one stands, and an existing single-column reference on the
// column stands for any composite that selects it.
func TestDeclineCompositeFKs_OverlappingConstraintsAndExistingScalar(t *testing.T) {
	tb := schemadef.Table{Name: "child"}
	tb.ForeignKeys = append(tb.ForeignKeys, schemadef.ForeignKey{Column: "c", RefTable: "other", RefColumn: "id", Name: "fk_child_c"})
	tb.ForeignKeys = append(tb.ForeignKeys, compositeMembers("fk_one", "p1", []string{"a", "b"}, []string{"x", "y"})...)
	tb.ForeignKeys = append(tb.ForeignKeys, compositeMembers("fk_two", "p2", []string{"b", "a"}, []string{"x", "y"})...)
	tb.ForeignKeys = append(tb.ForeignKeys, compositeMembers("fk_three", "p3", []string{"c", "d"}, []string{"x", "y"})...)
	byName := map[string]schemadef.Table{"child": tb}
	out := declineCompositeFKs([]schemadef.Table{tb}, byName, map[string]map[string]string{
		"child": {"fk_one": "b", "fk_two": "b", "fk_three": "c"},
	})
	want := []string{"c->other.id", "b->p1.y"}
	if got := scalarFKs(out[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("scalar FKs = %v, want %v", got, want)
	}
	if len(out[0].ForeignKeys) != len(want) {
		t.Fatalf("ForeignKeys = %+v, want only %v", out[0].ForeignKeys, want)
	}
	if !reflect.DeepEqual(byName["child"], out[0]) {
		t.Fatal("byName and the returned slice disagree")
	}
}

// A composite BuildPlan did not decline is left whole.
func TestDeclineCompositeFKs_LeavesOtherCompositesWhole(t *testing.T) {
	tb := schemadef.Table{Name: "child"}
	tb.ForeignKeys = append(compositeMembers("fk_off", "p1", []string{"a", "b"}, []string{"x", "y"}),
		compositeMembers("fk_on", "p2", []string{"a", "c"}, []string{"x", "y"})...)
	byName := map[string]schemadef.Table{"child": tb}
	out := declineCompositeFKs([]schemadef.Table{tb}, byName, map[string]map[string]string{
		"child": {"fk_off": "b"},
	})
	var on int
	for _, fk := range out[0].ForeignKeys {
		if fk.Name == "fk_on" && len(fk.KeyColumns) == 2 {
			on++
		}
	}
	if on != 2 {
		t.Fatalf("fk_on members = %d, want 2 (untouched): %+v", on, out[0].ForeignKeys)
	}
	if got, want := scalarFKs(out[0]), []string{"b->p1.y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scalar FKs = %v, want %v", got, want)
	}
}
