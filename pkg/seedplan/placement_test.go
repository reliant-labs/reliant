package seedplan

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

func TestOrderRanks_NumberTheColumnsOfAnOrderingChain(t *testing.T) {
	window := schemadef.Table{
		Name:    "windows",
		PKCols:  []string{"id"},
		Columns: []schemadef.Column{idCol(), timeCol("opened_at"), timeCol("closed_at"), timeCol("archived_at"), textCol("note")},
		Checks: []schemadef.CheckConstraint{
			check("w1", "CHECK ((closed_at > opened_at))", "closed_at", "opened_at"),
			check("w2", "CHECK ((archived_at > closed_at))", "archived_at", "closed_at"),
		},
	}
	ranks := OrderRanks(window)
	want := map[string]int{"opened_at": 0, "closed_at": 1, "archived_at": 2}
	if len(ranks) != len(want) {
		t.Fatalf("ranks = %v, want %v (a column outside every ordering has no rank)", ranks, want)
	}
	for col, r := range want {
		if ranks[col] != r {
			t.Errorf("%s ranks %d, want %d", col, ranks[col], r)
		}
	}
	if got := OrderRanks(schemadef.Table{Name: "plain", Columns: []schemadef.Column{idCol()}}); got != nil {
		t.Errorf("a table with no ordering CHECK has no ranks, got %v", got)
	}
}

func TestOrderGreater_ComparesInTheComponentsOwnType(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind schemadef.CanonicalType
		a, b string
		want bool
	}{
		{"later time wins", schemadef.TypeTime, "2026-02-01T00:00:00Z", "2026-01-01T00:00:00Z", true},
		{"earlier time loses", schemadef.TypeTime, "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z", false},
		{"an unparseable base loses to a parseable one", schemadef.TypeTime, "2026-01-01T00:00:00Z", "not a time", true},
		{"an unparseable candidate never wins", schemadef.TypeTime, "not a time", "2026-01-01T00:00:00Z", false},
		{"larger int wins", schemadef.TypeInt, "10", "9", true},
		{"ints compare numerically, not as text", schemadef.TypeInt, "9", "10", false},
		{"an unparseable int base loses", schemadef.TypeInt, "5", "x", true},
		{"an unparseable int never wins", schemadef.TypeInt, "x", "5", false},
		{"larger float wins", schemadef.TypeFloat, "2.5", "2.25", true},
		{"a smaller float loses", schemadef.TypeFloat, "1.5", "2.25", false},
		{"an unparseable float never wins", schemadef.TypeFloat, "x", "1", false},
		{"an unparseable float base loses", schemadef.TypeFloat, "1", "x", true},
		{"strings are not ordered", schemadef.TypeString, "b", "a", false},
	} {
		if got := orderGreater(tc.kind, tc.a, tc.b); got != tc.want {
			t.Errorf("%s: orderGreater(%s, %q, %q) = %v, want %v", tc.name, tc.kind, tc.a, tc.b, got, tc.want)
		}
	}
}

func TestUnionPlacement_ReturnsTheBranchTheRowRotatesTo(t *testing.T) {
	tbl := couponsTable()
	pools := PoolsFromTables([]schemadef.Table{tbl})

	if got := UnionPlacement(tbl, pools, -1); got != nil {
		t.Errorf("a negative row has no placement, got %v", got)
	}
	if got := UnionPlacement(schemadef.Table{Name: "plain", Columns: []schemadef.Column{idCol()}}, nil, 0); got != nil {
		t.Errorf("a table with no union has no placement, got %v", got)
	}

	row0, row1 := UnionPlacement(tbl, pools, 0), UnionPlacement(tbl, pools, 1)
	if row0 == nil || row1 == nil {
		t.Fatalf("both rows of a placeable union have a placement: %v %v", row0, row1)
	}
	if row0["kind"].Value == row1["kind"].Value {
		t.Errorf("consecutive rows take different branches, both are %q", row0["kind"].Value)
	}
	for i, row := range []map[string]UnionCell{row0, row1} {
		kind := row["kind"].Value
		amount, minutes := row["amount_cents"], row["compute_minutes"]
		switch kind {
		case "wallet_credit":
			if amount.Null || !amount.HasBound || minutes.Null != true {
				t.Errorf("row %d wallet_credit: amount %+v minutes %+v", i, amount, minutes)
			}
			if amount.Bound.Min == nil || *amount.Bound.Min != 1 {
				t.Errorf("row %d: amount_cents > 0 is a lower bound of 1, got %+v", i, amount.Bound)
			}
		case "compute_minutes":
			if minutes.Null || !minutes.HasBound || amount.Null != true {
				t.Errorf("row %d compute_minutes: amount %+v minutes %+v", i, amount, minutes)
			}
		default:
			t.Errorf("row %d placed kind %q outside the vocabulary", i, kind)
		}
	}
	if again := UnionPlacement(tbl, pools, 2); again["kind"].Value != row0["kind"].Value {
		t.Errorf("rotation wraps around: row 2 = %q, row 0 = %q", again["kind"].Value, row0["kind"].Value)
	}

	// The exported cell says a bare IS NOT NULL still demands a value.
	if c := exportUnionCell(unionCell{requireEdge: true}); !c.Present {
		t.Error("a required edge must be exported as Present")
	}
	if c := exportUnionCell(unionCell{null: true}); !c.Null || c.Present {
		t.Errorf("a null cell is Null and not Present: %+v", c)
	}
}

func TestMinimalUnionPlacement_PicksTheBranchACreateCanSatisfy(t *testing.T) {
	tbl := couponsTable()
	pools := PoolsFromTables([]schemadef.Table{tbl})
	got := MinimalUnionPlacement(tbl, pools)
	if got == nil {
		t.Fatal("a placeable union has a minimal placement")
	}
	kind := got["kind"].Value
	if kind != "wallet_credit" && kind != "compute_minutes" {
		t.Errorf("kind = %q", kind)
	}
	if again := MinimalUnionPlacement(tbl, pools); again["kind"].Value != kind {
		t.Errorf("the minimal placement is deterministic: %q then %q", kind, again["kind"].Value)
	}
	if got := MinimalUnionPlacement(schemadef.Table{Name: "plain", Columns: []schemadef.Column{idCol()}}, nil); got != nil {
		t.Errorf("a table with no union has no minimal placement, got %v", got)
	}
}
