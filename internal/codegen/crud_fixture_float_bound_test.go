package codegen

import (
	"strconv"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// A DOUBLE PRECISION column bounded to a rate — CHECK (col >= 0 AND col <= 1)
// — is the shape every proportion has: a waste factor, a tax rate, a
// probability, a utilization ratio.
//
// The two create fixtures were derived as `f1` and `f1+1`, and only `f1` was
// clamped, so create #2 emitted 2 against a `<= 1` column. That is not a test
// that fails later: the guard proves the value wrong at GENERATE time, so the
// entity cannot be born at all, and the message sends the author to
// db/seeds/vocab.yaml, which does not feed this generator.
//
// Both values must satisfy the bound, and they must stay distinct — the
// lifecycle test asserts two rows differ, so collapsing both onto the max
// would trade a generate failure for a silently weaker test.
func TestFloatFixtures_RespectFractionalUpperBound(t *testing.T) {
	c := func(name string, typ schemadef.CanonicalType, notNull, pk bool) schemadef.Column {
		return schemadef.Column{Name: name, Type: typ, NotNull: notNull, IsPK: pk}
	}
	items := schemadef.Table{
		Name:   "catalog_items",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			c("id", schemadef.TypeString, true, true),
			c("waste_factor", schemadef.TypeFloat, true, false),
		},
		Checks: []schemadef.CheckConstraint{
			{
				Name:    "catalog_items_waste_factor_check",
				Def:     `CHECK ((waste_factor >= (0)::double precision))`,
				Columns: []string{"waste_factor"},
			},
			{
				Name:    "catalog_items_waste_factor_check1",
				Def:     `CHECK ((waste_factor <= (1)::double precision))`,
				Columns: []string{"waste_factor"},
			},
		},
	}

	fx := fixtureModel(t, []schemadef.Table{items}, "catalog_items")
	ent := EntityDef{TableName: "catalog_items"}

	v1, v2, ok := fx.deriveFieldFixture(
		ServiceDef{}, ent, "CreateCatalogItemRequest", "waste_factor", "float64", FieldKindScalar)
	if !ok {
		t.Fatal("no fixture derived for a bounded float column")
	}

	f1, err := strconv.ParseFloat(v1, 64)
	if err != nil {
		t.Fatalf("create #1 fixture %q is not a float: %v", v1, err)
	}
	f2, err := strconv.ParseFloat(v2, 64)
	if err != nil {
		t.Fatalf("create #2 fixture %q is not a float: %v", v2, err)
	}

	for _, tc := range []struct {
		label string
		got   float64
	}{{"create #1", f1}, {"create #2", f2}} {
		if tc.got < 0 || tc.got > 1 {
			t.Errorf("%s fixture = %v, outside the column's own CHECK (0 <= x <= 1)", tc.label, tc.got)
		}
	}

	if f1 == f2 {
		t.Errorf("both fixtures are %v; the lifecycle test needs two distinct rows", f1)
	}
}

// The same column, bounded only from below, must still produce two distinct
// ascending values — the unbounded-above path is the common case and must not
// regress while fixing the bounded one.
func TestFloatFixtures_UnboundedAboveStaysDistinct(t *testing.T) {
	c := func(name string, typ schemadef.CanonicalType, notNull, pk bool) schemadef.Column {
		return schemadef.Column{Name: name, Type: typ, NotNull: notNull, IsPK: pk}
	}
	tbl := schemadef.Table{
		Name:   "readings",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			c("id", schemadef.TypeString, true, true),
			c("celsius", schemadef.TypeFloat, true, false),
		},
		Checks: []schemadef.CheckConstraint{
			{
				Name:    "readings_celsius_check",
				Def:     `CHECK ((celsius >= (0)::double precision))`,
				Columns: []string{"celsius"},
			},
		},
	}

	fx := fixtureModel(t, []schemadef.Table{tbl}, "readings")
	v1, v2, ok := fx.deriveFieldFixture(
		ServiceDef{}, EntityDef{TableName: "readings"}, "CreateReadingRequest", "celsius", "float64", FieldKindScalar)
	if !ok {
		t.Fatal("no fixture derived")
	}
	f1, _ := strconv.ParseFloat(v1, 64)
	f2, _ := strconv.ParseFloat(v2, 64)
	if f1 < 0 || f2 < 0 {
		t.Errorf("fixtures (%v, %v) violate the >= 0 bound", f1, f2)
	}
	if f1 == f2 {
		t.Errorf("both fixtures are %v; the lifecycle test needs two distinct rows", f1)
	}
}

// The integer twin of the float bug, and the quieter one: a column bounded to
// [0,1] derived its two fixtures as clampInt(1) and clampInt(2), which both
// pin to 1. Nothing fails — the generated lifecycle test just compares a row
// against itself and reports success, so the "two creates produce two distinct
// rows" assertion proves nothing for every narrow-bounded integer column.
func TestIntFixtures_NarrowBoundStaysDistinct(t *testing.T) {
	tbl := schemadef.Table{
		Name:   "flags",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			{Name: "level", Type: schemadef.TypeInt, NotNull: true},
		},
		Checks: []schemadef.CheckConstraint{
			{Name: "flags_level_check", Def: `CHECK ((level >= 0))`, Columns: []string{"level"}},
			{Name: "flags_level_check1", Def: `CHECK ((level <= 1))`, Columns: []string{"level"}},
		},
	}

	fx := fixtureModel(t, []schemadef.Table{tbl}, "flags")
	v1, v2, ok := fx.deriveFieldFixture(
		ServiceDef{}, EntityDef{TableName: "flags"}, "CreateFlagRequest", "level", "int64", FieldKindScalar)
	if !ok {
		t.Fatal("no fixture derived for a bounded int column")
	}
	n1, _ := strconv.ParseInt(v1, 10, 64)
	n2, _ := strconv.ParseInt(v2, 10, 64)
	for _, tc := range []struct {
		label string
		got   int64
	}{{"create #1", n1}, {"create #2", n2}} {
		if tc.got < 0 || tc.got > 1 {
			t.Errorf("%s fixture = %d, outside the column's own CHECK (0 <= x <= 1)", tc.label, tc.got)
		}
	}
	if n1 == n2 {
		t.Errorf("both fixtures are %d; the lifecycle test would compare a row against itself", n1)
	}
}

// Guard the guard: BoundsFromTables must actually parse a `<= 1` double CHECK.
// If it silently returned no bound, the tests above would pass by accident.
func TestBoundsFromTables_ParsesDoubleUpperBound(t *testing.T) {
	tbl := schemadef.Table{
		Name: "catalog_items",
		Columns: []schemadef.Column{
			{Name: "waste_factor", Type: schemadef.TypeFloat, NotNull: true},
		},
		Checks: []schemadef.CheckConstraint{
			{
				Name:    "catalog_items_waste_factor_check1",
				Def:     `CHECK ((waste_factor <= (1)::double precision))`,
				Columns: []string{"waste_factor"},
			},
		},
	}
	b := seedplan.BoundsFromTables([]schemadef.Table{tbl})["catalog_items"]["waste_factor"]
	if b.Max == nil {
		t.Fatal("upper bound was not parsed from the double CHECK; the fixture tests would pass vacuously")
	}
	if *b.Max != 1 {
		t.Errorf("parsed upper bound = %d, want 1", *b.Max)
	}
}
