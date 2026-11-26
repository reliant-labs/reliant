package dbanalyze

import (
	"strings"
	"testing"
)

// A table read in full under a join is unserved even when the catalog holds
// an index leading with the join column: the plan, not the catalog, says
// whether an index serves the join.
func TestClassifyJoinReadInFullIsUnservedDespiteAnIndexOnTheJoinKey(t *testing.T) {
	plan := []byte(`[{"Plan": {"Node Type": "Hash Join", "Hash Cond": "(o.customer_id = c.id)", "Plans": [
	  {"Node Type": "Seq Scan", "Relation Name": "orders", "Schema": "public", "Alias": "o", "Disabled": true},
	  {"Node Type": "Hash", "Plans": [
	    {"Node Type": "Index Scan", "Relation Name": "customers", "Schema": "public", "Alias": "c",
	     "Index Name": "customers_org", "Index Cond": "(c.org_id = $1)"}]}]}}]`)
	root, err := parsePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	rels := map[string]*relInfo{
		"orders":    {table: "orders", indexes: map[string]indexInfo{"orders_customer": {}}},
		"customers": {table: "customers", indexes: map[string]indexInfo{"customers_org": {}}},
	}
	got, err := classify(root, root, func(_, name string) (*relInfo, error) { return rels[name], nil })
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"orders": AccessUnserved + "/" + UnservedJoin, "customers": AccessServed + "/" + ServedByIndexCond}
	if len(got) != len(want) {
		t.Fatalf("access = %+v, want %v", got, want)
	}
	for _, c := range got {
		if v := c.Verdict + "/" + c.How; v != want[c.Table] {
			t.Errorf("%s: %s, want %s", c.Table, v, want[c.Table])
		}
	}
}

// A Join Filter that joins two relations and also filters one of them
// restricts the statement: a table read in full to be joined is unserved.
func TestClassifyJoinFilterCarryingARestrictionMarksTheFullReadUnserved(t *testing.T) {
	plan := []byte(`[{"Plan": {"Node Type": "Nested Loop", "Join Type": "Left",
	  "Join Filter": "((o.customer_id = c.id) AND (o.status = 'paid'::text))", "Plans": [
	  {"Node Type": "Seq Scan", "Relation Name": "orders", "Schema": "public", "Alias": "o", "Disabled": true},
	  {"Node Type": "Materialize", "Plans": [
	    {"Node Type": "Seq Scan", "Relation Name": "customers", "Schema": "public", "Alias": "c", "Disabled": true}]}]}}]`)
	root, err := parsePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	rels := map[string]*relInfo{
		"orders":    {table: "orders", indexes: map[string]indexInfo{}},
		"customers": {table: "customers", indexes: map[string]indexInfo{}},
	}
	got, err := classify(root, root, func(_, name string) (*relInfo, error) { return rels[name], nil })
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"orders":    AccessUnserved + "/" + UnservedJoin + "/customer_id",
		"customers": AccessUnserved + "/" + UnservedJoin + "/id",
	}
	if len(got) != len(want) {
		t.Fatalf("access = %+v, want %v", got, want)
	}
	for _, c := range got {
		if v := c.Verdict + "/" + c.How + "/" + c.Detail; v != want[c.Table] {
			t.Errorf("%s: %s, want %s", c.Table, v, want[c.Table])
		}
	}
}

// A subquery's filter restricts the subquery, not the query around it: an
// outer join that nothing filters stays unfiltered when a SubPlan or InitPlan
// below it filters a table of its own.
func TestClassifySubqueryFilterDoesNotRestrictTheOuterJoin(t *testing.T) {
	for _, parent := range []string{"SubPlan", "InitPlan"} {
		t.Run(parent, func(t *testing.T) {
			plan := []byte(`[{"Plan": {"Node Type": "Hash Join", "Hash Cond": "(o.customer_id = c.id)", "Plans": [
			  {"Node Type": "Seq Scan", "Relation Name": "orders", "Schema": "public", "Alias": "o", "Disabled": true},
			  {"Node Type": "Hash", "Plans": [
			    {"Node Type": "Seq Scan", "Relation Name": "customers", "Schema": "public", "Alias": "c", "Disabled": true}]},
			  {"Node Type": "Aggregate", "Parent Relationship": "` + parent + `", "Subplan Name": "` + parent + ` 1", "Plans": [
			    {"Node Type": "Seq Scan", "Relation Name": "items", "Schema": "public", "Alias": "i", "Disabled": true,
			     "Filter": "(i.kind = 'x'::text)"}]}]}}]`)
			root, err := parsePlan(plan)
			if err != nil {
				t.Fatal(err)
			}
			rels := map[string]*relInfo{
				"orders":    {table: "orders", indexes: map[string]indexInfo{"orders_customer": {}}},
				"customers": {table: "customers", indexes: map[string]indexInfo{}},
				"items":     {table: "items", indexes: map[string]indexInfo{}},
			}
			got, err := classify(root, root, func(_, name string) (*relInfo, error) { return rels[name], nil })
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{
				"orders":    AccessUnfiltered + "/",
				"customers": AccessUnfiltered + "/",
				"items":     AccessUnserved + "/" + UnservedFilter,
			}
			if len(got) != len(want) {
				t.Fatalf("access = %+v, want %v", got, want)
			}
			for _, c := range got {
				if v := c.Verdict + "/" + c.How; v != want[c.Table] {
					t.Errorf("%s: %s, want %s", c.Table, v, want[c.Table])
				}
			}
		})
	}
}

func TestConjunctsSplitTopLevelAnds(t *testing.T) {
	for in, want := range map[string][]string{
		"(o.id = c.id)": {"(o.id = c.id)"},
		"((o.customer_id = c.id) AND (o.status = 'paid'::text))": {"(o.customer_id = c.id)", "(o.status = 'paid'::text)"},
		"((o.a = c.a) OR (o.b = 'x AND y'::text))":               {"((o.a = c.a) OR (o.b = 'x AND y'::text))"},
		"((a = 1) AND ((b = 2) AND (c = 3)))":                    {"(a = 1)", "(b = 2)", "(c = 3)"},
		"((a)::text = 'it''s AND'::text)":                        {"((a)::text = 'it''s AND'::text)"},
		// A doubled quote closes and reopens the literal, so the text after
		// it, " AND " and alias references included, stays inside it.
		"((o.note = 'it''s AND o.customer_id = c.id'::text) AND (o.id = 1))": {"(o.note = 'it''s AND o.customer_id = c.id'::text)", "(o.id = 1)"},
		"(o.note = 'it''s AND o.customer_id = c.id'::text)":                  {"(o.note = 'it''s AND o.customer_id = c.id'::text)"},
		"((o.\"we\"\"ird AND x\" = 1) AND (o.id = 1))":                       {"(o.\"we\"\"ird AND x\" = 1)", "(o.id = 1)"},
		"('a'')' = o.x)": {"('a'')' = o.x)"},
	} {
		got := conjuncts(in)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("conjuncts(%q) = %q, want %q", in, got, want)
		}
	}
}
