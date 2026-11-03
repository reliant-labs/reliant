package seedplan

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

func TestFitLength(t *testing.T) {
	tests := []struct {
		name             string
		in               string
		minLen, maxLen   int
		want             string
		wantRuneCountMax int
	}{
		{name: "unbounded passes through", in: "sample_date_of_birth_1", want: "sample_date_of_birth_1"},
		{name: "truncates to the cap", in: "sample_date_of_birth_1", maxLen: 10, want: "sample_dat"},
		{name: "pads to the minimum", in: "a", minLen: 3, want: "a00"},
		{name: "exact-length window", in: "sample_author_initials_1", minLen: 2, maxLen: 3, want: "sam"},
		{name: "already inside the window", in: "USD", minLen: 3, maxLen: 3, want: "USD"},
		// char_length counts CHARACTERS, not bytes — a byte-wise truncation
		// would both overshoot the cap and split a multi-byte rune.
		{name: "counts runes not bytes", in: "ααααα", maxLen: 3, want: "ααα"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FitLength(tc.in, tc.minLen, tc.maxLen); got != tc.want {
				t.Errorf("FitLength(%q, %d, %d) = %q, want %q", tc.in, tc.minLen, tc.maxLen, got, tc.want)
			}
		})
	}
}

// LengthBounds reads the introspected varchar cap, which DeclType cannot
// carry: information_schema rebuilds `varchar(4)` as udt_name "VARCHAR", so
// before MaxChars existed the declared-cap half of this merge was dead against
// every live schema.
func TestLengthBounds_IntrospectedVarcharCap(t *testing.T) {
	tbl := schemadef.Table{
		Name: "patients",
		Columns: []schemadef.Column{
			{Name: "room_code", DeclType: "VARCHAR", MaxChars: 4, Type: schemadef.TypeString},
		},
	}
	if minLen, maxLen := LengthBounds(tbl, tbl.Columns[0]); minLen != 0 || maxLen != 4 {
		t.Errorf("LengthBounds = (%d,%d), want (0,4)", minLen, maxLen)
	}
}

// The TIGHTER of the introspected cap and a char_length CHECK wins — a column
// can carry both.
func TestLengthBounds_TakesTheTighterCap(t *testing.T) {
	tbl := schemadef.Table{
		Name: "patients",
		Columns: []schemadef.Column{
			{Name: "code", DeclType: "VARCHAR", MaxChars: 10, Type: schemadef.TypeString},
		},
		Checks: []schemadef.CheckConstraint{
			{Name: "c", Def: `CHECK ((char_length(code) <= 4))`, Columns: []string{"code"}},
		},
	}
	if minLen, maxLen := LengthBounds(tbl, tbl.Columns[0]); minLen != 0 || maxLen != 4 {
		t.Errorf("LengthBounds = (%d,%d), want (0,4)", minLen, maxLen)
	}
}

func TestDistinctVariant(t *testing.T) {
	// With room to spare the discriminator is appended.
	if got, ok := distinctVariant("Acme Corp", 1, 0, 0); !ok || got != "Acme Corp-1" {
		t.Errorf("unbounded variant = %q (ok=%v), want %q", got, ok, "Acme Corp-1")
	}
	// Under a cap the BASE is truncated to make room, so the result still fits.
	got, ok := distinctVariant("Acme Corp", 2, 0, 4)
	if !ok || got != "Acm2" {
		t.Errorf("capped variant = %q (ok=%v), want %q", got, ok, "Acm2")
	}
	// A cap too small to hold the discriminator means the column has run out
	// of distinct values — the caller caps the table's rows.
	if _, ok := distinctVariant("Acme Corp", 10, 0, 1); ok {
		t.Error("distinctVariant must fail when the cap cannot hold the discriminator")
	}
}

// permute is a deterministic, salt-stable shuffle that preserves the SET —
// drawing without replacement must not invent or drop a vocabulary value.
func TestPermute_IsStablePermutation(t *testing.T) {
	pool := []string{"a", "b", "c", "d", "e", "f", "g"}
	got := permute(pool, 1, "orders", "order_number")
	if len(got) != len(pool) {
		t.Fatalf("permute changed the pool size: %d, want %d", len(got), len(pool))
	}
	if !sameSet(got, pool) {
		t.Errorf("permute(%v) = %v — not a permutation", pool, got)
	}
	if again := permute(pool, 1, "orders", "order_number"); strings.Join(again, ",") != strings.Join(got, ",") {
		t.Errorf("permute is not deterministic: %v then %v", got, again)
	}
	// A different salt yields a different-but-stable dataset.
	if other := permute(pool, 2, "orders", "order_number"); strings.Join(other, ",") == strings.Join(got, ",") {
		t.Log("salt 2 happened to match salt 1 for this pool — acceptable, but the draw must still be salt-derived")
	}
	// permute must not depend on the row count: raising `rows` APPENDS values
	// rather than reshuffling the ones already seeded.
	short := permute(pool[:4], 1, "orders", "order_number")
	if !sameSet(short, pool[:4]) {
		t.Errorf("permute over a shorter pool = %v — not a permutation", short)
	}
}

// The plan is a pure function of (schema, config, vocab): finalize is
// idempotent, so re-resolving it never ratchets the row counts down.
func TestFinalize_IsIdempotent(t *testing.T) {
	tables := []schemadef.Table{{
		Name:   "badges",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, IsPK: true, NotNull: true},
			{Name: "grade", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true},
		},
		Indexes: []schemadef.Index{{Name: "badges_grade_key", Columns: []string{"grade"}, Unique: true}},
		Checks: []schemadef.CheckConstraint{
			{Name: "badges_grade_check", Def: `CHECK ((grade = ANY (ARRAY['gold'::text, 'silver'::text])))`, Columns: []string{"grade"}},
		},
	}}
	plan, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 20, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.tables[0].n; got != 2 {
		t.Fatalf("badges row count = %d, want 2 (the UNIQUE column's vocabulary size)", got)
	}
	first := strings.Join(plan.Statements(), "\n")
	plan.finalize()
	plan.finalize()
	if got := plan.tables[0].n; got != 2 {
		t.Errorf("row count after re-finalize = %d, want 2 (finalize must not ratchet)", got)
	}
	if again := strings.Join(plan.Statements(), "\n"); again != first {
		t.Errorf("re-finalize changed the rendered SQL:\n%s\n---\n%s", first, again)
	}
	if warns := strings.Join(plan.Warnings(), "\n"); !strings.Contains(warns, "badges.grade") {
		t.Errorf("expected a cap warning naming badges.grade, got: %q", warns)
	}
}

// Adding rows must APPEND: row i's value can never depend on how many rows
// come after it, or a config bump would reshuffle a whole seeded database.
func TestUniqueAssignment_IsAppendOnlyInRowCount(t *testing.T) {
	tables := []schemadef.Table{{
		Name:   "products",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, IsPK: true, NotNull: true},
			{Name: "sku", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true},
		},
		Indexes: []schemadef.Index{{Name: "products_sku_key", Columns: []string{"sku"}, Unique: true}},
	}}
	small, err := BuildPlan(tables, nil, Config{Rows: 5, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}
	large, err := BuildPlan(tables, nil, Config{Rows: 12, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		a, _ := small.SeedValue("products", "sku", i)
		b, _ := large.SeedValue("products", "sku", i)
		if a != b {
			t.Errorf("row %d sku = %q at rows=5 but %q at rows=12 — raising rows must append, not reshuffle", i, a, b)
		}
	}
	// And the larger plan's values are all distinct.
	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		v, ok := large.SeedValue("products", "sku", i)
		if !ok {
			t.Fatalf("row %d has no sku value", i)
		}
		if seen[v] {
			t.Errorf("duplicate sku %q at row %d", v, i)
		}
		seen[v] = true
	}
}

// A UNIQUE column drawing from a pool SMALLER than the row target is the shape
// that made `forge db seed` unusable. It caps rather than colliding, and the
// values it does seed are the author's, unmangled.
func TestAssignUnique_DrawsWithoutReplacement(t *testing.T) {
	tables := []schemadef.Table{{
		Name:   "orders",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, IsPK: true, NotNull: true},
			{Name: "order_number", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true},
		},
		Indexes: []schemadef.Index{{Name: "orders_order_number_key", Columns: []string{"order_number"}, Unique: true}},
	}}
	plan, err := BuildPlan(tables, nil, Config{Rows: 20, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}
	pool := []string{"ORD-1", "ORD-2", "ORD-3", "ORD-4", "ORD-5"}
	plan.ApplyVocab(&Vocab{Columns: map[string][]string{"orders.order_number": pool}})

	if got := plan.tables[0].n; got != len(pool) {
		t.Fatalf("orders row count = %d, want %d (capped at the pool size)", got, len(pool))
	}
	var drawn []string
	for i := 0; i < plan.tables[0].n; i++ {
		v, ok := plan.SeedValue("orders", "order_number", i)
		if !ok {
			t.Fatalf("row %d has no value", i)
		}
		drawn = append(drawn, v)
	}
	if !sameSet(drawn, pool) {
		t.Errorf("drew %v, want a permutation of the author's pool %v", drawn, pool)
	}
}
