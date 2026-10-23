package seedplan

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// compositePKSchema models the shape a per-parent counter table takes:
// (company_id, kind) is the primary key, and company_id is ALSO a foreign key
// to companies.id. The column therefore wears both hats at once.
func compositePKSchema() []schemadef.Table {
	companies := schemadef.Table{
		Name:   "companies",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("name", schemadef.TypeString, true, false),
		},
	}
	counters := schemadef.Table{
		Name:   "document_counters",
		PKCols: []string{"company_id", "kind"},
		Columns: []schemadef.Column{
			col("company_id", schemadef.TypeString, true, true),
			col("kind", schemadef.TypeString, true, true),
			col("next_value", schemadef.TypeInt, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{
			{Column: "company_id", RefTable: "companies", RefColumn: "id"},
		},
	}
	return []schemadef.Table{counters, companies}
}

// A column that is BOTH a primary-key member and a foreign key must be seeded
// as the reference, not as a fresh synthetic key.
//
// cellLiteral tests col.IsPK before cp.fk, so such a column takes the
// pkLiteral branch, which derives a UUID from "<table>.<row>" and knows
// nothing about the parent. The value is well-typed and references nothing,
// so the INSERT fails the foreign key at runtime (SQLSTATE 23503) rather than
// at plan time — the seeder's usual contract is to resolve impossibilities
// before emitting a single statement.
// The assertion is that every value NAMES SOME SEEDED PARENT, not that child
// row i takes parent row i: fkParentRow distributes children across the
// available parents, so an identity mapping is not the contract. What the
// foreign key requires is only that the value exists in companies.id.
func TestCompositePK_ForeignKeyMemberResolvesToParent(t *testing.T) {
	const rows = 3
	cfg := Config{Rows: rows, Salt: 7}
	plan := buildOrFail(t, compositePKSchema(), cfg)

	seededCompanies := map[string]bool{}
	for i := range rows {
		v, ok := plan.SeedValue("companies", "id", i)
		if !ok {
			t.Fatalf("row %d: companies.id is not seeded", i)
		}
		seededCompanies[v] = true
	}

	for i := range rows {
		child, ok := plan.SeedValue("document_counters", "company_id", i)
		if !ok {
			t.Fatalf("row %d: document_counters.company_id is not seeded", i)
		}
		if !seededCompanies[child] {
			t.Errorf("row %d: company_id = %q, which is not any seeded companies.id\n"+
				"a PK column that is also an FK must resolve to a parent; a synthesized "+
				"key references no row and fails the constraint at INSERT", i, child)
		}
	}
}

// Members of one composite key must not collide with each other. Both columns
// here are TEXT, so a literal derived from table+row alone gives them the
// identical value — which also makes the pair non-unique across rows.
func TestCompositePK_MembersOfOneKeyAreDistinct(t *testing.T) {
	cfg := Config{Rows: 3, Salt: 7}
	plan := buildOrFail(t, compositePKSchema(), cfg)

	seen := map[string]int{}
	for i := range 3 {
		companyID, ok := plan.SeedValue("document_counters", "company_id", i)
		if !ok {
			t.Fatalf("row %d: company_id is not seeded", i)
		}
		kind, ok := plan.SeedValue("document_counters", "kind", i)
		if !ok {
			t.Fatalf("row %d: kind is not seeded", i)
		}
		if companyID == kind {
			t.Errorf("row %d: both key members carry %q — one row's members must differ", i, companyID)
		}
		pair := companyID + "\x00" + kind
		if prev, dup := seen[pair]; dup {
			t.Errorf("rows %d and %d share the composite key (%q, %q), violating PRIMARY KEY", prev, i, companyID, kind)
		}
		seen[pair] = i
	}
}

// The rendered SQL is the artifact that actually reaches postgres, so assert
// the resolved id appears there too — SeedValue agreeing while Render disagrees
// would still ship a broken seed.
func TestCompositePK_RenderedInsertReferencesSeededParent(t *testing.T) {
	cfg := Config{Rows: 2, Salt: 7}
	plan := buildOrFail(t, compositePKSchema(), cfg)

	parent, ok := plan.SeedValue("companies", "id", 0)
	if !ok {
		t.Fatal("companies.id row 0 is not seeded")
	}
	sql := plan.Render()
	counters := sql[strings.Index(sql, `INSERT INTO "document_counters"`):]
	if !strings.Contains(counters, parent) {
		t.Errorf("rendered document_counters INSERT does not reference the seeded company %q\n%s", parent, counters)
	}
}
