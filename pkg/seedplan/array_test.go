package seedplan

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// arrayCol is a NOT NULL array column of the given declared type.
func arrayCol(name, decl string) schemadef.Column {
	c := schemadef.Column{Name: name, DeclType: decl, NotNull: true}
	c.Type, c.IsArray, c.TypeKnown = schemadef.MapDeclaredType(decl)
	return c
}

// arrayChecksTable carries every array CHECK shape the planner reads, each
// spelled the way pg_get_constraintdef renders it (captured from postgres 16).
func arrayChecksTable() schemadef.Table {
	check := func(col, def string) schemadef.CheckConstraint {
		return schemadef.CheckConstraint{Name: "t_" + col + "_check", Columns: []string{col}, Def: def}
	}
	return schemadef.Table{
		Name:   "t",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			arrayCol("scopes", "TEXT[]"),
			arrayCol("roles", "VARCHAR[]"),
			arrayCol("levels", "INT8[]"),
			arrayCol("owners", "UUID[]"),
			arrayCol("colors", "TEXT[]"),
			arrayCol("counts", "INT4[]"),
			arrayCol("one", "TEXT[]"),
			arrayCol("flags", "BOOL[]"),
			arrayCol("tags", "TEXT[]"),
		},
		Checks: []schemadef.CheckConstraint{
			// reliant's access_tokens.scopes, the shape that surfaced this.
			check("scopes", `CHECK (((cardinality(scopes) > 0) AND (scopes <@ ARRAY['deploy:read'::text, 'deploy:write'::text, 'it''s'::text])))`),
			check("roles", `CHECK ((roles <@ ARRAY['admin'::character varying, 'member'::character varying]))`),
			check("levels", `CHECK (((cardinality(levels) >= 2) AND (levels <@ ARRAY[(10)::bigint, (20)::bigint, (30)::bigint])))`),
			check("owners", `CHECK ((owners <@ ARRAY['5eed0000-0000-5000-8000-000000000001'::uuid, '5eed0000-0000-5000-8000-000000000002'::uuid]))`),
			check("colors", `CHECK ((colors <@ '{red,"dark blue"}'::text[]))`),
			check("counts", `CHECK ((array_length(counts, 1) >= 3))`),
			check("one", `CHECK ((cardinality(one) <= 1))`),
			check("flags", `CHECK ((flags <@ ARRAY[true]))`),
		},
	}
}

func TestArrayShapeOf_ReadsContainedByAndCardinality(t *testing.T) {
	tb := arrayChecksTable()
	cases := map[string]arrayShape{
		"scopes": {members: []string{"deploy:read", "deploy:write", "it's"}, minLen: 1},
		"roles":  {members: []string{"admin", "member"}},
		"levels": {members: []string{"10", "20", "30"}, minLen: 2},
		"owners": {members: []string{"5eed0000-0000-5000-8000-000000000001", "5eed0000-0000-5000-8000-000000000002"}},
		"colors": {members: []string{"red", "dark blue"}},
		"counts": {minLen: 3},
		"one":    {maxLen: 1, hasMax: true},
		"flags":  {members: []string{"true"}},
		"tags":   {},
	}
	for _, col := range tb.Columns[1:] {
		want, ok := cases[col.Name]
		if !ok {
			t.Fatalf("no expectation for %s", col.Name)
		}
		if got := arrayShapeOf(tb, col); !reflect.DeepEqual(got, want) {
			t.Errorf("arrayShapeOf(%s) = %+v, want %+v", col.Name, got, want)
		}
	}
}

// A contained-by array whose members are not all constants is not a set the
// planner can draw from: reading part of it would "satisfy" a CHECK it never
// read, so the column is never given members from a partial intersection.
func TestArrayShapeOf_DeclinesANonConstantSet(t *testing.T) {
	for _, def := range []string{
		`CHECK ((tags <@ ARRAY['a'::text, NULL::text]))`,
		`CHECK ((tags <@ ARRAY['a'::text, lower('B'::text)]))`,
		`CHECK ((other <@ ARRAY['a'::text]))`, // another column's set
	} {
		tb := schemadef.Table{Name: "t", Columns: []schemadef.Column{arrayCol("tags", "TEXT[]")},
			Checks: []schemadef.CheckConstraint{{Name: "c", Columns: []string{"tags"}, Def: def}}}
		if got := arrayShapeOf(tb, tb.Columns[0]); got.members != nil {
			t.Errorf("%s: members = %q, want none", def, got.members)
		}
	}
}

func TestArrayShapeOfRejectsPartialIntersections(t *testing.T) {
	check := func(name, body string) schemadef.CheckConstraint {
		return schemadef.CheckConstraint{Name: name, Columns: []string{"tags"}, Def: body}
	}
	for _, tc := range []struct {
		name   string
		checks []schemadef.CheckConstraint
	}{
		{"across checks", []schemadef.CheckConstraint{
			check("constant", `CHECK (tags <@ ARRAY['a', 'b'])`),
			check("unknown", `CHECK (tags <@ ARRAY[lower('B')])`),
		}},
		{"in one check", []schemadef.CheckConstraint{
			check("unknown", `CHECK (tags <@ ARRAY[lower('B')] AND tags <@ ARRAY['a', 'b'])`),
		}},
		{"dynamic right side", []schemadef.CheckConstraint{
			check("unknown", `CHECK (tags <@ ARRAY['a', 'b'] AND tags <@ string_to_array('b,c', ','))`),
		}},
		{"literal prefix of an expression", []schemadef.CheckConstraint{
			check("unknown", `CHECK (tags <@ ARRAY['a', 'b'] AND tags <@ (ARRAY['b'] || string_to_array('c,d', ',')))`),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tb := arrayChecksTable()
			tb.Columns = []schemadef.Column{tb.Columns[0], arrayCol("tags", "TEXT[]")}
			tb.Checks = tc.checks
			shape := arrayShapeOf(tb, tb.Columns[1])
			if shape.unknownSet != "unknown" || shape.members != nil {
				t.Fatalf("partial intersection was used: %+v", shape)
			}
			p := planFor(t, tb, 3)
			if err := p.Validate(); err == nil || !strings.Contains(err.Error(), `t.tags: cannot read the contained-by set in CHECK "unknown"`) {
				t.Fatalf("Validate() = %v; want refusal naming the unreadable CHECK", err)
			}
		})
	}
}

func TestArrayShapeOfIntersectsEveryConstantSet(t *testing.T) {
	tb := arrayChecksTable()
	tb.Columns = []schemadef.Column{tb.Columns[0], arrayCol("tags", "TEXT[]")}
	tb.Checks = []schemadef.CheckConstraint{{Name: "both", Columns: []string{"tags"}, Def: `CHECK (tags <@ ARRAY['tags <@ literal', 'b'] AND tags <@ ARRAY['c', 'b'])`}}
	shape := arrayShapeOf(tb, tb.Columns[1])
	if !reflect.DeepEqual(shape.members, []string{"b"}) || shape.unknownSet != "" {
		t.Fatalf("all constant sets were not intersected: %+v", shape)
	}
}

// Every planned array literal satisfies the CHECK on its column: elements
// drawn from the contained-by set, element counts inside the cardinality
// bounds. The unconstrained column keeps the two elements it always had.
func TestArrayLiteral_SatisfiesItsCheck(t *testing.T) {
	const rows = 12
	p := planFor(t, arrayChecksTable(), rows)
	tb := arrayChecksTable()
	for _, col := range tb.Columns[1:] {
		shape := arrayShapeOf(tb, col)
		for i := 0; i < rows; i++ {
			_, cp, _ := p.colPlan("t", col.Name)
			lit := p.cellLiteral(p.tables[0], cp, i, 0)
			elems, ok := splitArrayLiteral(lit)
			if !ok {
				t.Fatalf("%s row %d: %s is not an ARRAY[...] literal", col.Name, i, lit)
			}
			if len(elems) == 0 {
				t.Fatalf("%s row %d: empty array %s — a NOT NULL array is seeded non-empty", col.Name, i, lit)
			}
			if len(elems) < shape.minLen || (shape.hasMax && len(elems) > shape.maxLen) {
				t.Errorf("%s row %d: %d element(s) in %s, outside [%d, %d]", col.Name, i, len(elems), lit, shape.minLen, shape.maxLen)
			}
			if shape.members == nil {
				continue
			}
			allowed := map[string]bool{}
			for _, m := range shape.members {
				allowed[arrayMemberLiteral(col, m)] = true
			}
			for _, e := range elems {
				if !allowed[e] {
					t.Errorf("%s row %d: element %s of %s is outside the declared set %q", col.Name, i, e, lit, shape.members)
				}
			}
		}
	}
	// The unconstrained column is byte-identical to what it always seeded.
	_, cp, _ := p.colPlan("t", "tags")
	if got, want := p.cellLiteral(p.tables[0], cp, 0, 0), "ARRAY['sample_tags_1', 'sample_tags_2']"; got != want {
		t.Errorf("unconstrained tags row 0 = %s, want %s", got, want)
	}
	_, cp, _ = p.colPlan("t", "owners")
	if got := p.cellLiteral(p.tables[0], cp, 0, 0); !strings.HasSuffix(got, "::uuid[]") {
		t.Errorf("uuid[] literal %s lost its ::uuid[] cast", got)
	}
}

// A drawn set varies across rows, so the seeded tokens do not all hold the
// same scope.
func TestArrayLiteral_DrawVariesAcrossRows(t *testing.T) {
	p := planFor(t, arrayChecksTable(), 20)
	_, cp, _ := p.colPlan("t", "scopes")
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		seen[p.cellLiteral(p.tables[0], cp, i, 0)] = true
	}
	if len(seen) < 2 {
		t.Errorf("scopes seeded one value on every row: %v", seen)
	}
}

// A rejected INSERT names the array forge planned. The explanation used to
// print `scopes = NULL` for any cell that was not a plain scalar, which read
// as "the planner left a NOT NULL column NULL" when the real defect was the
// array's elements.
func TestExplainInsertFailure_PrintsThePlannedArray(t *testing.T) {
	p := planFor(t, arrayChecksTable(), 2)
	err := p.explainInsertFailure(p.tables[0], errors.New(
		`ERROR: new row for relation "t" violates check constraint "t_counts_check" (SQLSTATE 23514)`))
	msg := err.Error()
	if !strings.Contains(msg, "row 0: counts = ARRAY[1, 2, 3]") {
		t.Errorf("explanation does not show the planned array:\n%s", msg)
	}
	if strings.Contains(msg, "= NULL") {
		t.Errorf("explanation reports a planned array as NULL:\n%s", msg)
	}
}

// splitArrayLiteral splits `ARRAY[a, b]` (with an optional cast) into its
// element literals. Test-only: the planner's own elements never hold a comma.
func splitArrayLiteral(lit string) ([]string, bool) {
	lit = strings.TrimSuffix(lit, "::uuid[]")
	if lit == "'{}'" {
		return nil, true
	}
	if !strings.HasPrefix(lit, "ARRAY[") || !strings.HasSuffix(lit, "]") {
		return nil, false
	}
	return strings.Split(lit[len("ARRAY["):len(lit)-1], ", "), true
}
