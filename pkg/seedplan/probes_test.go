package seedplan

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

func ptr(n int64) *int64 { return &n }

func TestProbeInt_FindsTheNearestUnusedValueInsideTheBound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lit   string
		used  map[string]bool
		bound NumBound
		want  string
		ok    bool
	}{
		{"free as it is", "5", nil, NumBound{}, "5", true},
		{"upward past the taken ones", "5", map[string]bool{"5": true, "6": true}, NumBound{}, "7", true},
		{"upward blocked by the max, downward finds room", "5", map[string]bool{"5": true}, NumBound{Max: ptr(5)}, "4", true},
		{"a value below the min is skipped on the way up", "0", map[string]bool{}, NumBound{Min: ptr(3), Max: ptr(9)}, "3", true},
		{"the bound is exhausted both ways", "5", map[string]bool{"4": true, "5": true}, NumBound{Min: ptr(4), Max: ptr(5)}, "", false},
		{"downward stops at the min", "3", map[string]bool{"3": true}, NumBound{Min: ptr(3), Max: ptr(3)}, "", false},
		{"downward skips what the max forbids", "9", map[string]bool{"9": true}, NumBound{Max: ptr(8), Min: ptr(1)}, "8", true},
		{"not a number: the literal is the only candidate", "'x'", nil, NumBound{}, "'x'", true},
		{"not a number and taken", "'x'", map[string]bool{"'x'": true}, NumBound{}, "'x'", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used := tc.used
			if used == nil {
				used = map[string]bool{}
			}
			got, ok := probeInt(tc.lit, used, 64, tc.bound)
			if got != tc.want || ok != tc.ok {
				t.Errorf("probeInt(%s) = %q, %v; want %q, %v", tc.lit, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestProbeStringFold_TriesTheValueThenNumberedVariants(t *testing.T) {
	ident := func(s string) string { return s }
	got, ok := probeStringFold("Acme", map[string]bool{}, 10, 0, 0, nil, ident)
	if !ok || got != "'Acme'" {
		t.Errorf("free: %q, %v", got, ok)
	}
	used := map[string]bool{"'Acme'": true, "'Acme-1'": true}
	if got, ok := probeStringFold("Acme", used, 10, 0, 0, nil, ident); !ok || got != "'Acme-2'" {
		t.Errorf("the next variant: %q, %v", got, ok)
	}
	// A pattern that rejects some variants skips them.
	digitsOnly := func(s string) bool { return strings.HasSuffix(s, "3") }
	if got, ok := probeStringFold("Acme", used, 10, 0, 0, digitsOnly, ident); !ok || got != "'Acme-3'" {
		t.Errorf("a pattern-admitted variant: %q, %v", got, ok)
	}
	// A pattern nothing satisfies exhausts the probe.
	if _, ok := probeStringFold("Acme", used, 5, 0, 0, func(string) bool { return false }, ident); ok {
		t.Error("a pattern admitting no variant must exhaust the probe")
	}
	// A cap that cannot hold a discriminator stops the probe.
	tight := map[string]bool{"'A'": true}
	for k := 1; k <= 9; k++ {
		tight["'"+string(rune('0'+k))+"'"] = true
	}
	if _, ok := probeStringFold("A", tight, 12, 0, 1, nil, ident); ok {
		t.Error("a one-character column with every digit taken has no further variant")
	}
	// The fold keys the index's comparison, not the rendered value.
	lower := strings.ToLower
	if got, ok := probeStringFold("ACME", map[string]bool{"'acme'": true}, 10, 0, 0, nil, lower); !ok || got != "'ACME-1'" {
		t.Errorf("a folded collision: %q, %v", got, ok)
	}
}

func TestProbeBytes_VariesThePayloadUntilItIsUnused(t *testing.T) {
	first, ok := probeBytes("blob", 0, map[string]bool{}, 10, 0, 0)
	if !ok || !strings.HasSuffix(first, "'::bytea") {
		t.Fatalf("first = %q, %v", first, ok)
	}
	second, ok := probeBytes("blob", 0, map[string]bool{first: true}, 10, 0, 0)
	if !ok || second == first {
		t.Errorf("a taken payload must give way to a variant: %q, %v", second, ok)
	}
	if _, ok := probeBytes("blob", 0, map[string]bool{first: true}, 0, 0, 0); ok {
		t.Error("no probe budget, no variant")
	}
}

func TestPatternAdmitter(t *testing.T) {
	col := schemadef.Column{Name: "code", Type: schemadef.TypeString}
	tbl := func(defs ...string) schemadef.Table {
		var cks []schemadef.CheckConstraint
		for i, d := range defs {
			cks = append(cks, schemadef.CheckConstraint{Name: "c" + string(rune('a'+i)), Def: d, Columns: []string{"code"}})
		}
		return schemadef.Table{Name: "t", Columns: []schemadef.Column{col}, Checks: cks}
	}
	if got := patternAdmitter(tbl(), col); got != nil {
		t.Error("a column with no pattern CHECK has no admitter")
	}
	admit := patternAdmitter(tbl(`CHECK ((code ~ '^[A-Z]{2}[0-9]+$'::text))`, `CHECK ((code ~ '5$'::text))`), col)
	if admit == nil {
		t.Fatal("want an admitter")
	}
	for s, want := range map[string]bool{"AB15": true, "AB16": false, "ab15": false} {
		if admit(s) != want {
			t.Errorf("admit(%q) = %v, want %v", s, !want, want)
		}
	}
	broken := patternAdmitter(tbl(`CHECK ((code ~ '([a-z'::text))`), col)
	if broken == nil || broken("anything") {
		t.Error("an unparseable pattern admits nothing, so a probe degrades to the honest cap")
	}
}

func uniqueTable(colType schemadef.CanonicalType, decl string, extra ...schemadef.CheckConstraint) schemadef.Table {
	return schemadef.Table{
		Name:   "things",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, IsPK: true, NotNull: true},
			{Name: "val", DeclType: decl, Type: colType, NotNull: true},
		},
		Indexes: []schemadef.Index{{Name: "things_val_key", Columns: []string{"val"}, Unique: true}},
		Checks:  extra,
	}
}

func TestAssignUnique_ProbesEachColumnKindToDistinctValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		table schemadef.Table
		bound *NumBound
		rows  int
		want  int // distinct values seeded
	}{
		{"strings get numbered variants", uniqueTable(schemadef.TypeString, "TEXT"), nil, 12, 12},
		{"integers walk upward", uniqueTable(schemadef.TypeInt, "INTEGER"), nil, 12, 12},
		{"a bounded integer caps the table at the range", uniqueTable(schemadef.TypeInt, "INTEGER"), &NumBound{Min: ptr(1), Max: ptr(4)}, 12, 4},
		{"bytes get distinct payloads", uniqueTable(schemadef.TypeBytes, "BYTEA"), nil, 8, 8},
		{"a one-document json column holds one row", uniqueTable(schemadef.TypeJSON, "JSONB"), nil, 5, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tables := []schemadef.Table{tc.table}
			p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: tc.rows, Salt: 3})
			if err != nil {
				t.Fatal(err)
			}
			if tc.bound != nil {
				p.SetBounds(CheckBounds{"things": {"val": *tc.bound}})
				p.finalize()
			}
			n := p.tables[0].n
			if n != tc.want {
				t.Fatalf("rows = %d, want %d", n, tc.want)
			}
			seen := map[string]bool{}
			tp := p.tables[0]
			for i := 0; i < n; i++ {
				// cellLiteral is the rendered SQL literal, so bytea payloads
				// (which SeedValue does not decode) are compared too.
				_, cp, _ := p.colPlan("things", "val")
				v := p.cellLiteral(tp, cp, i, 0)
				if seen[v] {
					t.Errorf("row %d repeats %s on a UNIQUE column", i, v)
				}
				seen[v] = true
			}
			if tc.want < tc.rows {
				found := false
				for _, w := range p.Warnings() {
					if strings.Contains(w, "things.val is UNIQUE") && strings.Contains(w, "capped") {
						found = true
					}
				}
				if !found {
					t.Errorf("a capped table must say so: %v", p.Warnings())
				}
			}
		})
	}
}

func TestAssignUnique_FoldedVocabularyCollapsesCaseVariants(t *testing.T) {
	tbl := uniqueTable(schemadef.TypeString, "TEXT")
	tbl.Indexes = []schemadef.Index{{Name: "things_val_lower", Columns: []string{"val"}, Unique: true, Expression: true,
		Keys: []schemadef.IndexKey{{Expr: "lower(val)", Column: "val", Fold: "lower"}}}}
	tables := []schemadef.Table{tbl}
	p, err := BuildPlan(tables, nil, Config{Rows: 6, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}
	p.ApplyVocab(&Vocab{Columns: map[string][]string{"things.val": {"Acme", "ACME", "acme", "Globex", "GLOBEX"}}})
	if n := p.tables[0].n; n != 2 {
		t.Fatalf("5 spellings of 2 names under UNIQUE (lower(val)) leave 2 rows, got %d", n)
	}
	var drawn []string
	for i := 0; i < 2; i++ {
		v, _ := p.SeedValue("things", "val", i)
		drawn = append(drawn, strings.ToLower(v))
	}
	if drawn[0] == drawn[1] {
		t.Errorf("the two rows fold to one key: %v", drawn)
	}
}
