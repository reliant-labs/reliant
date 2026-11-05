package seedplan

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

func col(name string, typ schemadef.CanonicalType, notNull, pk bool) schemadef.Column {
	return schemadef.Column{Name: name, Type: typ, NotNull: notNull, IsPK: pk}
}

// patients + appointments, appointments.patient_id -> patients.id (NOT NULL).
func fkSchema() []schemadef.Table {
	patients := schemadef.Table{
		Name:   "patients",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("region", schemadef.TypeString, true, false),
			col("name", schemadef.TypeString, true, false),
			col("email", schemadef.TypeString, true, false),
			col("created_at", schemadef.TypeTime, true, false),
			col("updated_at", schemadef.TypeTime, true, false),
			col("deleted_at", schemadef.TypeTime, false, false),
		},
	}
	appointments := schemadef.Table{
		Name:   "appointments",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("region", schemadef.TypeString, true, false),
			col("patient_id", schemadef.TypeString, true, false),
			col("reason", schemadef.TypeString, true, false),
			col("scheduled_at", schemadef.TypeTime, false, false),
			col("created_at", schemadef.TypeTime, true, false),
			col("updated_at", schemadef.TypeTime, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{{Column: "patient_id", RefTable: "patients", RefColumn: "id"}},
	}
	return []schemadef.Table{appointments, patients} // deliberately reversed input order
}

func buildOrFail(t *testing.T, tables []schemadef.Table, cfg Config) *Plan {
	t.Helper()
	p, err := BuildPlan(tables, nil, cfg)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	return p
}

// Same schema + config renders byte-identically.
func TestRender_Deterministic(t *testing.T) {
	cfg := Config{Rows: 5, Salt: 7}
	a := buildOrFail(t, fkSchema(), cfg).Render()
	b := buildOrFail(t, fkSchema(), cfg).Render()
	if a != b {
		t.Fatalf("render not byte-identical across runs")
	}
	if !strings.Contains(a, "INSERT INTO \"patients\"") || !strings.Contains(a, "INSERT INTO \"appointments\"") {
		t.Fatalf("render missing expected tables:\n%s", a)
	}
}

// FK topology: parent (patients) is inserted before child (appointments)
// even though the input order was reversed.
func TestPlan_TopologicalOrder(t *testing.T) {
	p := buildOrFail(t, fkSchema(), DefaultConfig())
	order := p.Tables()
	pi, ai := indexOf(order, "patients"), indexOf(order, "appointments")
	if pi < 0 || ai < 0 || pi > ai {
		t.Fatalf("topo order wrong: %v (patients must precede appointments)", order)
	}
}

// Adding a column never reshuffles other columns' values (each cell hash is
// column-local).
func TestSynthesis_ColumnAddDoesNotReshuffle(t *testing.T) {
	cfg := Config{Rows: 8, Salt: 3}
	base := buildOrFail(t, fkSchema(), cfg)

	// Add a new column to patients.
	tables := fkSchema()
	for i := range tables {
		if tables[i].Name == "patients" {
			tables[i].Columns = append(tables[i].Columns, col("notes", schemadef.TypeString, false, false))
		}
	}
	widened := buildOrFail(t, tables, cfg)

	for _, name := range []string{"name", "email", "id", "created_at"} {
		if got, want := cellsFor(base, "patients", name), cellsFor(widened, "patients", name); !equal(got, want) {
			t.Fatalf("column %q reshuffled after adding a new column:\n before=%v\n after =%v", name, want, got)
		}
	}
}

// oneToOneSchema: orders (parent) + intakes (child) where intakes.order_id is
// a NOT NULL foreign key carrying a single-column UNIQUE constraint — a 1-1
// relationship. Mirrors the dogfood shape whose duplicate FK values violated
// the intakes_order_id_key unique constraint. Child columns are kept to id +
// order_id so the rendered tuple parses cleanly.
func oneToOneSchema() []schemadef.Table {
	orders := schemadef.Table{
		Name: "orders", PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("total_cents", schemadef.TypeInt, true, false),
		},
	}
	intakes := schemadef.Table{
		Name: "intakes", PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("order_id", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{{Column: "order_id", RefTable: "orders", RefColumn: "id"}},
		Indexes:     []schemadef.Index{{Name: "intakes_order_id_key", Unique: true, Columns: []string{"order_id"}}},
	}
	return []schemadef.Table{intakes, orders} // reversed input order
}

// A UNIQUE foreign-key column (1-1 relationship) gets a DISTINCT parent value
// per child row. Pre-fix the hash-pick collided two children onto one parent,
// so the INSERT violated the unique constraint.
func TestPlan_UniqueFK_DistinctReferences(t *testing.T) {
	cfg := Config{Rows: 20, Salt: 1}
	p := buildOrFail(t, oneToOneSchema(), cfg)
	vals := cellsFor(p, "intakes", "order_id")
	if len(vals) != 20 {
		t.Fatalf("intakes row count = %d, want 20 (parent has 20 rows)", len(vals))
	}
	seen := map[string]bool{}
	for i, v := range vals {
		if seen[v] {
			t.Fatalf("intakes.order_id value %s repeats at row %d — a UNIQUE FK must reference a distinct parent per row", v, i)
		}
		seen[v] = true
	}
}

// A 1-1 child cannot seed more rows than the referenced parent has — each
// parent is referenced at most once, so the child count caps at the parent's.
func TestPlan_UniqueFK_CapsChildAtParentRowCount(t *testing.T) {
	cfg := Config{Rows: 20, Salt: 1, RowsPerTable: map[string]int{"orders": 5}}
	p := buildOrFail(t, oneToOneSchema(), cfg)
	vals := cellsFor(p, "intakes", "order_id")
	if len(vals) != 5 {
		t.Fatalf("intakes row count = %d, want 5 (capped at parent orders=5)", len(vals))
	}
	seen := map[string]bool{}
	for _, v := range vals {
		if seen[v] {
			t.Fatalf("capped 1-1 child still produced a duplicate FK value %s", v)
		}
		seen[v] = true
	}
}

// FK values are always real parent PKs (never a guess), and soft-delete rows
// seed live (deleted_at NULL).
func TestFKValues_AreValidParentPKs(t *testing.T) {
	cfg := Config{Rows: 5, Salt: 2}
	p := buildOrFail(t, fkSchema(), cfg)

	valid := map[string]bool{}
	patientPK := findPKCol(p, "patients")
	for i := 0; i < p.rowsOf["patients"]; i++ {
		valid[pkLiteral(p.cfg, "patients", patientPK, i, false)] = true
	}
	for i, v := range cellsFor(p, "appointments", "patient_id") {
		if v == "NULL" {
			t.Fatalf("appointments row %d has NULL patient_id (NOT NULL FK)", i)
		}
		if !valid[v] {
			t.Fatalf("appointments row %d patient_id %s is not a real patients PK", i, v)
		}
	}
	for _, v := range cellsFor(p, "patients", "deleted_at") {
		if v != "NULL" {
			t.Fatalf("deleted_at should seed NULL (rows live), got %s", v)
		}
	}
}

// A NOT NULL foreign-key cycle is a hard error naming the cycle.
func TestBuildPlan_NotNullCycleIsHardError(t *testing.T) {
	a := schemadef.Table{
		Name: "a", PKCols: []string{"id"},
		Columns:     []schemadef.Column{col("id", schemadef.TypeString, true, true), col("b_id", schemadef.TypeString, true, false)},
		ForeignKeys: []schemadef.ForeignKey{{Column: "b_id", RefTable: "b", RefColumn: "id"}},
	}
	b := schemadef.Table{
		Name: "b", PKCols: []string{"id"},
		Columns:     []schemadef.Column{col("id", schemadef.TypeString, true, true), col("a_id", schemadef.TypeString, true, false)},
		ForeignKeys: []schemadef.ForeignKey{{Column: "a_id", RefTable: "a", RefColumn: "id"}},
	}
	_, err := BuildPlan([]schemadef.Table{a, b}, nil, DefaultConfig())
	if err == nil {
		t.Fatal("expected a hard error for a NOT NULL FK cycle")
	}
	if !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Fatalf("cycle error must name the cycle, got: %v", err)
	}
}

// A cycle with a nullable edge is broken (that FK forced NULL), no error.
func TestBuildPlan_NullableCycleIsBroken(t *testing.T) {
	a := schemadef.Table{
		Name: "a", PKCols: []string{"id"},
		Columns:     []schemadef.Column{col("id", schemadef.TypeString, true, true), col("b_id", schemadef.TypeString, false, false)}, // nullable
		ForeignKeys: []schemadef.ForeignKey{{Column: "b_id", RefTable: "b", RefColumn: "id"}},
	}
	b := schemadef.Table{
		Name: "b", PKCols: []string{"id"},
		Columns:     []schemadef.Column{col("id", schemadef.TypeString, true, true), col("a_id", schemadef.TypeString, true, false)},
		ForeignKeys: []schemadef.ForeignKey{{Column: "a_id", RefTable: "a", RefColumn: "id"}},
	}
	p, err := BuildPlan([]schemadef.Table{a, b}, nil, DefaultConfig())
	if err != nil {
		t.Fatalf("nullable cycle must be broken, not errored: %v", err)
	}
	// The broken edge (a.b_id) must be forced NULL in every row.
	for i, v := range cellsFor(p, "a", "b_id") {
		if v != "NULL" {
			t.Fatalf("broken cycle edge a.b_id row %d = %s, want NULL", i, v)
		}
	}
}

// Enum/CHECK pools are respected: a constrained column only draws allowed
// values.
func TestEnumPools_Respected(t *testing.T) {
	pools := EnumPools{"patients": {"status": {"draft", "published"}}}
	tables := fkSchema()
	for i := range tables {
		if tables[i].Name == "patients" {
			tables[i].Columns = append(tables[i].Columns, col("status", schemadef.TypeString, true, false))
		}
	}
	p, err := BuildPlan(tables, pools, Config{Rows: 12, Salt: 4})
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"'draft'": true, "'published'": true}
	for i, v := range cellsFor(p, "patients", "status") {
		if !allowed[v] {
			t.Fatalf("status row %d = %s, not in the CHECK pool", i, v)
		}
	}
}

func TestSeedEnumChoices_DropsZeroValueSentinel(t *testing.T) {
	// Unit: sentinel detection + filtering, order preserved.
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"PRODUCT_STATUS_UNSPECIFIED", "PRODUCT_STATUS_DRAFT", "PRODUCT_STATUS_ACTIVE"},
			[]string{"PRODUCT_STATUS_DRAFT", "PRODUCT_STATUS_ACTIVE"}},
		{[]string{"SPECIES_UNSPECIFIED", "SPECIES_HUMAN"}, []string{"SPECIES_HUMAN"}},
		{[]string{"STATE_UNKNOWN", "STATE_OPEN"}, []string{"STATE_OPEN"}},
		{[]string{"draft", "published"}, []string{"draft", "published"}},                 // no sentinel → unchanged
		{[]string{"PRODUCT_STATUS_UNSPECIFIED"}, []string{"PRODUCT_STATUS_UNSPECIFIED"}}, // only sentinel → fallback
	}
	for _, c := range cases {
		if got := SeedEnumChoices(c.in); !equal(got, c.want) {
			t.Errorf("SeedEnumChoices(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	// _NONE / _INVALID are meaningful non-zero states, not the proto sentinel.
	if isEnumZeroSentinel("DISCOUNT_NONE") || isEnumZeroSentinel("SCAN_INVALID") {
		t.Error("_NONE/_INVALID must not be treated as the zero-value sentinel")
	}

	// Integration: seeded cells never carry the sentinel when real values exist.
	pools := EnumPools{"patients": {"status": {"STATUS_UNSPECIFIED", "STATUS_DRAFT", "STATUS_PUBLISHED"}}}
	tables := fkSchema()
	for i := range tables {
		if tables[i].Name == "patients" {
			tables[i].Columns = append(tables[i].Columns, col("status", schemadef.TypeString, true, false))
		}
	}
	p, err := BuildPlan(tables, pools, Config{Rows: 12, Salt: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range cellsFor(p, "patients", "status") {
		if v == "'STATUS_UNSPECIFIED'" {
			t.Fatalf("status row %d seeded the UNSPECIFIED sentinel", i)
		}
	}
}

// ── helpers ──

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// cellsFor renders the literal for one column across all rows of a table.
func cellsFor(p *Plan, table, column string) []string {
	tp, cp, ok := p.colPlan(table, column)
	if !ok {
		return nil
	}
	out := make([]string, tp.n)
	for i := 0; i < tp.n; i++ {
		out[i] = p.cellLiteral(tp, cp, i, 0)
	}
	return out
}

func findPKCol(p *Plan, table string) schemadef.Column {
	t := p.byName[table]
	for _, c := range t.Columns {
		if c.IsPK {
			return c
		}
	}
	return schemadef.Column{}
}

// Pure-schema pool/bound extraction: the generate-time (no live DB)
// siblings of IntrospectEnumPools / IntrospectCheckBounds, fed by
// schemadef's introspected CHECK constraints.
func TestPoolsAndBoundsFromTables(t *testing.T) {
	tables := []schemadef.Table{{
		Name: "products",
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("status", schemadef.TypeString, true, false),
			col("price_cents", schemadef.TypeInt, true, false),
			col("rating", schemadef.TypeInt, true, false),
		},
		Checks: []schemadef.CheckConstraint{
			// pg_get_constraintdef canonical spellings.
			{Name: "products_status_check", Def: `CHECK ((status = ANY (ARRAY['it''s'::text, 'B'::text])))`, Columns: []string{"status"}},
			{Name: "products_price_cents_check", Def: `CHECK ((price_cents >= 100))`, Columns: []string{"price_cents"}},
			{Name: "products_rating_check", Def: `CHECK (((rating >= 1) AND (rating <= 5)))`, Columns: []string{"rating"}},
			// Not a pool, not a bound — must be ignored.
			{Name: "products_id_check", Def: `CHECK ((id <> ''::text))`, Columns: []string{"id"}},
			// Multi-column checks are skipped entirely.
			{Name: "products_multi_check", Def: `CHECK ((price_cents > rating))`, Columns: []string{"price_cents", "rating"}},
		},
	}}

	pools := PoolsFromTables(tables)
	if got, want := pools["products"]["status"], []string{"it's", "B"}; !equal(got, want) {
		t.Errorf("status pool = %v, want %v (with '' unescaped)", got, want)
	}
	if _, ok := pools["products"]["id"]; ok {
		t.Error("the id-non-empty guard is not a vocabulary pool")
	}

	bounds := BoundsFromTables(tables)
	if b := bounds["products"]["price_cents"]; b.Min == nil || *b.Min != 100 || b.Max != nil {
		t.Errorf("price_cents bound = %+v, want Min=100, no Max", b)
	}
	if b := bounds["products"]["rating"]; b.Min == nil || *b.Min != 1 || b.Max == nil || *b.Max != 5 {
		t.Errorf("rating bound = %+v, want [1,5]", b)
	}
	if _, ok := bounds["products"]["status"]; ok {
		t.Error("an enum pool must not read as a numeric bound")
	}
}

// SeedValue exposes the raw (unquoted) seeded cell values — what generated
// tests embed to reference seeded parent rows.
func TestPlan_SeedValue(t *testing.T) {
	p := buildOrFail(t, fkSchema(), Config{Rows: 3, Salt: 0})

	id0, ok := p.SeedValue("patients", "id", 0)
	if !ok || id0 == "" {
		t.Fatalf("SeedValue(patients, id, 0) = %q, %v", id0, ok)
	}
	// Raw, not SQL-quoted, and identical to what the INSERT carries.
	if strings.Contains(id0, "'") {
		t.Errorf("SeedValue must be unquoted, got %q", id0)
	}
	if cells := cellsFor(p, "patients", "id"); cells[0] != "'"+id0+"'" {
		t.Errorf("SeedValue %q does not match rendered cell %s", id0, cells[0])
	}
	// The FK derivation and SeedValue agree by construction.
	if fkCells := cellsFor(p, "appointments", "patient_id"); len(fkCells) == 0 {
		t.Fatal("no appointment FK cells")
	}
	// Out-of-range rows and unknown cells are not ok.
	if _, ok := p.SeedValue("patients", "id", 99); ok {
		t.Error("row beyond the plan's count must not resolve")
	}
	if _, ok := p.SeedValue("nope", "id", 0); ok {
		t.Error("unknown table must not resolve")
	}
	// NULL cells (deleted_at seeds live) are not scalars.
	if _, ok := p.SeedValue("patients", "deleted_at", 0); ok {
		t.Error("a NULL cell must not resolve to a scalar")
	}
}
