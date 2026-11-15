package seedplan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

func writeVocab(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vocab.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A missing file and a fully-commented scaffold are both exact no-ops.
func TestLoadVocab_MissingAndEmptyAreNoOps(t *testing.T) {
	if v, err := LoadVocab(filepath.Join(t.TempDir(), "nope.yaml")); v != nil || err != nil {
		t.Fatalf("missing file = (%v, %v), want (nil, nil)", v, err)
	}
	path := writeVocab(t, "# just comments\n# columns:\n#   products.name: [A]\n")
	if v, err := LoadVocab(path); v != nil || err != nil {
		t.Fatalf("commented scaffold = (%v, %v), want (nil, nil)", v, err)
	}
}

// Named pools resolve into every referencing column; inline lists pass through.
func TestLoadVocab_ResolvesNamedPools(t *testing.T) {
	path := writeVocab(t, `
pools:
  peptide_names: [BPC-157, Semaglutide]
columns:
  products.name: {pool: peptide_names}
  compounds.name: {pool: peptide_names}
  brands.name: [VitalPep, PepCore Labs]
`)
	v, err := LoadVocab(path)
	if err != nil {
		t.Fatalf("LoadVocab: %v", err)
	}
	want := []string{"BPC-157", "Semaglutide"}
	if !equal(v.Columns["products.name"], want) || !equal(v.Columns["compounds.name"], want) {
		t.Errorf("named pool not shared: %v / %v", v.Columns["products.name"], v.Columns["compounds.name"])
	}
	if !equal(v.Columns["brands.name"], []string{"VitalPep", "PepCore Labs"}) {
		t.Errorf("inline list = %v", v.Columns["brands.name"])
	}
}

// A malformed file is a hard error naming the problem — silently seeding
// generic data over a file the author believes is active would be worse.
func TestLoadVocab_MalformedIsHardError(t *testing.T) {
	cases := []struct {
		name, content, wantErr string
	}{
		{"bad yaml", "columns: [:::", "parse seed vocab"},
		{"undefined pool", "columns:\n  products.name: {pool: nope}\n", `undefined pool "nope"`},
		{"scalar entry", "columns:\n  products.name: just-a-string\n", "a column entry must be a value list"},
		{"empty list", "columns:\n  products.name: []\n", "has no values"},
		{"key without table", "columns:\n  name: [A]\n", "must be table.column"},
		{"mapping without pool key", "columns:\n  products.name: {foo: bar}\n", "{pool: <name>}"},
		{"typo'd top-level key", "column:\n  products.name: [A]\n", "parse seed vocab"},
	}
	for _, c := range cases {
		_, err := LoadVocab(writeVocab(t, c.content))
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want mention of %q", c.name, err, c.wantErr)
		}
	}
}

// Vocab takes precedence for matched columns; every other column is
// byte-identical to the no-vocab plan (column-local draws never reshuffle),
// and the same (schema, config, vocab) renders byte-identically.
func TestVocab_PrecedenceAndNoReshuffle(t *testing.T) {
	cfg := Config{Rows: 10, Salt: 3}
	vocab := &Vocab{Columns: map[string][]string{
		"patients.name": {"BPC-157", "Semaglutide", "TB-500"},
	}}

	base := buildOrFail(t, fkSchema(), cfg)
	withVocab := buildOrFail(t, fkSchema(), cfg)
	if warns := withVocab.ApplyVocab(vocab); len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	allowed := map[string]bool{"'BPC-157'": true, "'Semaglutide'": true, "'TB-500'": true}
	for i, v := range cellsFor(withVocab, "patients", "name") {
		if !allowed[v] {
			t.Fatalf("patients.name row %d = %s, not from the vocab pool", i, v)
		}
	}
	// Unmatched columns keep built-in synthesis, values untouched.
	for _, colName := range []string{"email", "id", "created_at"} {
		if got, want := cellsFor(withVocab, "patients", colName), cellsFor(base, "patients", colName); !equal(got, want) {
			t.Fatalf("column %q reshuffled by vocab on another column:\n before=%v\n after =%v", colName, want, got)
		}
	}
	// Determinism: identical inputs render byte-identically.
	again := buildOrFail(t, fkSchema(), cfg)
	again.ApplyVocab(vocab)
	if withVocab.Render() != again.Render() {
		t.Fatal("same (schema, config, vocab) must render byte-identically")
	}
}

// vocabSchema: one table exercising every validation class — an enum/CHECK
// pool, a char_length cap, a numeric range, and referential columns.
func vocabSchema() []schemadef.Table {
	return []schemadef.Table{
		{
			Name:   "products",
			PKCols: []string{"id"},
			Columns: []schemadef.Column{
				col("id", schemadef.TypeString, true, true),
				col("region", schemadef.TypeString, true, false),
				col("brand_id", schemadef.TypeString, false, false),
				col("name", schemadef.TypeString, true, false),
				col("status", schemadef.TypeString, true, false),
				{Name: "code", Type: schemadef.TypeString, NotNull: true, DeclType: "VARCHAR(5)"},
				col("rating", schemadef.TypeInt, true, false),
				col("currency", schemadef.TypeString, true, false),
				col("deleted_at", schemadef.TypeTime, false, false),
			},
			ForeignKeys: []schemadef.ForeignKey{{Column: "brand_id", RefTable: "brands", RefColumn: "id"}},
		},
		{
			Name:   "brands",
			PKCols: []string{"id"},
			Columns: []schemadef.Column{
				col("id", schemadef.TypeString, true, true),
				col("name", schemadef.TypeString, true, false),
			},
		},
	}
}

func vocabPlan(t *testing.T) *Plan {
	t.Helper()
	pools := EnumPools{"products": {"status": {"draft", "published"}}}
	p, err := BuildPlan(vocabSchema(), pools, Config{Rows: 8, Salt: 2})
	if err != nil {
		t.Fatal(err)
	}
	one, five := int64(1), int64(5)
	p.SetBounds(CheckBounds{"products": {"rating": {Min: &one, Max: &five}}})
	return p
}

// Values that cannot satisfy the column's introspected constraints are
// skipped with a warning naming table.column and the constraint; the valid
// remainder still applies. A column whose vocab is entirely invalid falls
// back to built-in synthesis — the seed never fails on vocab problems.
func TestVocab_ConstraintValidationSkipsAndWarns(t *testing.T) {
	base := vocabPlan(t)
	p := vocabPlan(t)
	warns := p.ApplyVocab(&Vocab{Columns: map[string][]string{
		"products.status": {"draft", "bogus"},           // CHECK pool: bogus invalid
		"products.code":   {"AB123", "WAY-TOO-LONG"},    // varchar(5) cap
		"products.rating": {"3", "9", "x"},              // range CHECK [1,5]
		"products.name":   {""},                         // valid (empty string is insertable)
		"brands.name":     {"VitalPep", "PepCore Labs"}, // unconstrained — all valid
	}})

	wantWarns := []string{
		`products.status: value "bogus" is not in the column's CHECK/enum vocabulary`,
		`products.code: value "WAY-TOO-LONG" exceeds the 5-char cap`,
		`products.rating: value "9" is outside the CHECK range [1,5]`,
		`products.rating: value "x" is not an integer`,
	}
	for _, want := range wantWarns {
		found := false
		for _, w := range warns {
			if strings.Contains(w, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing warning %q in:\n%s", want, strings.Join(warns, "\n"))
		}
	}
	if !equal(p.VocabWarnings(), warns) {
		t.Error("VocabWarnings must expose the ApplyVocab warnings")
	}

	// Valid survivors drive the draw.
	for i, v := range cellsFor(p, "products", "status") {
		if v != "'draft'" {
			t.Fatalf("status row %d = %s, want 'draft' (only valid vocab value)", i, v)
		}
	}
	for i, v := range cellsFor(p, "products", "rating") {
		if v != "3" {
			t.Fatalf("rating row %d = %s, want 3 (only valid vocab value)", i, v)
		}
	}
	for i, v := range cellsFor(p, "products", "code") {
		if v != "'AB123'" {
			t.Fatalf("code row %d = %s, want 'AB123'", i, v)
		}
	}
	for i, v := range cellsFor(p, "brands", "name") {
		if v != "'VitalPep'" && v != "'PepCore Labs'" {
			t.Fatalf("brands.name row %d = %s, not from vocab", i, v)
		}
	}

	// Entirely-invalid vocab falls back to built-ins, with a warning.
	p2 := vocabPlan(t)
	warns2 := p2.ApplyVocab(&Vocab{Columns: map[string][]string{
		"products.rating": {"99", "100"},
	}})
	fellBack := false
	for _, w := range warns2 {
		if strings.Contains(w, "products.rating: no valid values remain") {
			fellBack = true
		}
	}
	if !fellBack {
		t.Errorf("expected a fallback warning, got: %v", warns2)
	}
	if got, want := cellsFor(p2, "products", "rating"), cellsFor(base, "products", "rating"); !equal(got, want) {
		t.Errorf("fully-invalid vocab must fall back to built-in synthesis:\n got=%v\nwant=%v", got, want)
	}
}

// PK / FK / soft-delete columns are the seeder's referential
// machinery — a vocab entry for them warns and is ignored, and unknown
// columns warn too.
func TestVocab_ReferentialColumnsNotOverridable(t *testing.T) {
	base := vocabPlan(t)
	p := vocabPlan(t)
	warns := p.ApplyVocab(&Vocab{Columns: map[string][]string{
		"products.id":         {"my-id"},
		"products.brand_id":   {"my-brand"},
		"products.deleted_at": {"2024-01-01"},
		"nosuch.column":       {"x"},
	}})
	wantWarns := []string{
		"products.id: primary-key column",
		"products.brand_id: foreign-key column",
		"products.deleted_at: managed soft-delete column",
		"nosuch.column: not a seedable column",
	}
	for _, want := range wantWarns {
		found := false
		for _, w := range warns {
			if strings.Contains(w, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing warning %q in:\n%s", want, strings.Join(warns, "\n"))
		}
	}
	// And the plan is untouched: byte-identical to the no-vocab render.
	if p.Render() != base.Render() {
		t.Error("ignored vocab entries must leave the plan byte-identical")
	}
}

// A CHECK vocabulary is a DECLARATION and the seeder draws from it. The
// column's name adds nothing on top of it.
//
// `currency` / `*_currency` used to pin to the constant USD — including
// inside a CHECK pool, where the name overrode the draw and picked USD out of
// {EUR, USD, GBP}. Which currency an app trades in is a decision the schema
// does not carry; a pooled column now draws from the pool it declares, an
// unpooled one gets the placeholder, and db/seeds/vocab.yaml is where a
// project states the answer.
func TestCheckVocabularyIsTheOnlyThingThatNarrowsAColumn(t *testing.T) {
	tables := []schemadef.Table{{
		Name:   "prices",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("currency", schemadef.TypeString, true, false),
			col("settle_currency", schemadef.TypeString, true, false),
			col("iso_currency", schemadef.TypeString, true, false),
		},
	}}
	pools := EnumPools{"prices": {
		"settle_currency": {"EUR", "USD", "GBP"},
		"iso_currency":    {"EUR", "GBP"},
	}}
	p, err := BuildPlan(tables, pools, Config{Rows: 10, Salt: 6})
	if err != nil {
		t.Fatal(err)
	}
	// No CHECK, no vocabulary: nothing declares what belongs here.
	for i, v := range cellsFor(p, "prices", "currency") {
		raw, ok := decodeScalarLiteral(v)
		if !ok || !strings.HasPrefix(raw, SyntheticStringPrefix) {
			t.Fatalf("currency row %d = %s, want the emitter's placeholder — nothing in the "+
				"schema says this column holds an ISO-4217 code", i, v)
		}
	}
	// Pooled columns draw from their own declared set, and the draw is not
	// pinned to one member: a name-driven preference for USD used to collapse
	// settle_currency to a single value.
	seen := map[string]bool{}
	allowed := map[string]bool{"'EUR'": true, "'USD'": true, "'GBP'": true}
	for i, v := range cellsFor(p, "prices", "settle_currency") {
		if !allowed[v] {
			t.Fatalf("settle_currency row %d = %s, must stay inside the CHECK pool", i, v)
		}
		seen[v] = true
	}
	if len(seen) < 2 {
		t.Errorf("settle_currency drew only %v across 10 rows — a three-value CHECK vocabulary "+
			"is not a one-value one", seen)
	}
	for i, v := range cellsFor(p, "prices", "iso_currency") {
		if v != "'EUR'" && v != "'GBP'" {
			t.Fatalf("iso_currency row %d = %s, must stay inside the CHECK pool", i, v)
		}
	}
	// Vocab is the declaration surface, and it wins over synthesis.
	if warns := p.ApplyVocab(&Vocab{Columns: map[string][]string{"prices.currency": {"EUR"}}}); len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	for i, v := range cellsFor(p, "prices", "currency") {
		if v != "'EUR'" {
			t.Fatalf("vocab-declared currency row %d = %s, want 'EUR'", i, v)
		}
	}
}

// A `name` column's SIBLINGS do not steer it either.
//
// The seeder used to classify a table as person-ish from the columns beside
// `name` — a `first_name`, a `date_of_birth`, or the weaker email+name pair —
// and switch its `name` column from a company pool to a person pool. That is
// the same inference as reading the table's name, only one hop away: a column
// carrying a person fact says the table has people IN it, not that its `name`
// column holds a person's name (an `agencies` row has a contact's birthdate
// and a firm's name). Nothing about a `name` column is derivable, so the
// value is the emitter's placeholder no matter what sits next to it, and
// db/seeds/vocab.yaml is where a project says otherwise.
func TestNameSynthesisIgnoresItsSiblingColumns(t *testing.T) {
	mk := func(name string, extra ...schemadef.Column) schemadef.Table {
		t := schemadef.Table{
			Name:   name,
			PKCols: []string{"id"},
			Columns: []schemadef.Column{
				col("id", schemadef.TypeString, true, true),
				col("name", schemadef.TypeString, true, false),
			},
		}
		t.Columns = append(t.Columns, extra...)
		return t
	}
	tables := []schemadef.Table{
		mk("patients", col("email", schemadef.TypeString, true, false)),
		mk("subjects", col("date_of_birth", schemadef.TypeTime, true, false)),
		mk("staff", col("first_name", schemadef.TypeString, true, false)),
		mk("companies", col("email", schemadef.TypeString, true, false)),
		mk("users", col("email", schemadef.TypeString, true, false)),
		mk("products"),
		mk("organizations"),
	}
	p, err := BuildPlan(tables, nil, Config{Rows: 6, Salt: 9})
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, tb := range tables {
		cells := cellsFor(p, tb.Name, "name")
		if len(cells) == 0 {
			t.Fatalf("%s.name seeded no cells — this test proves nothing", tb.Name)
		}
		for i, lit := range cells {
			raw, ok := decodeScalarLiteral(lit)
			if !ok {
				t.Fatalf("%s.name row %d = %s is not a scalar literal", tb.Name, i, lit)
			}
			if !strings.HasPrefix(raw, SyntheticStringPrefix) {
				t.Errorf("%s.name row %d = %q — the columns beside `name` decided what it holds",
					tb.Name, i, raw)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no name cells were checked — this test proves nothing")
	}
}

// LengthBounds merges the declared varchar cap with char_length CHECKs.
func TestLengthBounds(t *testing.T) {
	tb := schemadef.Table{
		Name: "products",
		Columns: []schemadef.Column{
			{Name: "code", DeclType: "VARCHAR(10)"},
			{Name: "sku", DeclType: "TEXT"},
		},
		Checks: []schemadef.CheckConstraint{
			{Name: "c1", Def: `CHECK ((char_length(code) >= 2))`, Columns: []string{"code"}},
			{Name: "c2", Def: `CHECK ((char_length(sku) = 4))`, Columns: []string{"sku"}},
		},
	}
	if lo, hi := LengthBounds(tb, tb.Columns[0]); lo != 2 || hi != 10 {
		t.Errorf("code bounds = [%d,%d], want [2,10]", lo, hi)
	}
	if lo, hi := LengthBounds(tb, tb.Columns[1]); lo != 4 || hi != 4 {
		t.Errorf("sku bounds = [%d,%d], want [4,4]", lo, hi)
	}
}

// A {min, max, step} entry expands to a numeric pool. Without it an
// unconstrained numeric column synthesizes as the ROW INDEX (i+1), so every
// money column seeds as 1,2,3… cents and two numeric columns on one table
// come out perfectly correlated.
func TestLoadVocab_NumericRange(t *testing.T) {
	path := writeVocab(t, `
columns:
  products.price_cents: {min: 1000, max: 1400, step: 100}
`)
	v, err := LoadVocab(path)
	if err != nil {
		t.Fatalf("LoadVocab: %v", err)
	}
	want := []string{"1000", "1100", "1200", "1300", "1400"}
	if !equal(v.Columns["products.price_cents"], want) {
		t.Errorf("range = %v, want %v", v.Columns["products.price_cents"], want)
	}
}

// A fractional range renders at the requested precision rather than
// truncating to whole units.
func TestLoadVocab_NumericRangeDecimals(t *testing.T) {
	path := writeVocab(t, `
columns:
  products.rating: {min: 1.0, max: 2.0, step: 0.5, decimals: 1}
`)
	v, err := LoadVocab(path)
	if err != nil {
		t.Fatalf("LoadVocab: %v", err)
	}
	want := []string{"1.0", "1.5", "2.0"}
	if !equal(v.Columns["products.rating"], want) {
		t.Errorf("range = %v, want %v", v.Columns["products.rating"], want)
	}
}

// Malformed ranges fail loudly at load rather than silently seeding the
// built-in ramp the author believes they overrode.
func TestLoadVocab_NumericRangeRejectsMalformed(t *testing.T) {
	for name, body := range map[string]string{
		"max below min":   `{min: 100, max: 10}`,
		"min without max": `{min: 100}`,
		"negative step":   `{min: 1, max: 10, step: -1}`,
		"range with pool": `{min: 1, max: 10, pool: prices}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeVocab(t, "columns:\n  products.price_cents: "+body+"\n")
			if _, err := LoadVocab(path); err == nil {
				t.Fatalf("%s: expected an error, got nil", name)
			}
		})
	}
}
