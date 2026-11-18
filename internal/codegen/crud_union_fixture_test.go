// File: internal/codegen/crud_union_fixture_test.go
//
// The lifecycle test's create requests are forge's OTHER generated dataset,
// written against the same schema as the seeded rows. A discriminated-union
// CHECK spans several columns, so a value chosen per FIELD cannot satisfy it —
// which is what these pin, against seedplan's own union model rather than a
// second reading of the same SQL.

package codegen

import (
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// couponsPayloadCheck is the constraint verbatim as pg_get_constraintdef
// renders it — the exact string the introspector hands the matcher.
const couponsPayloadCheck = `CHECK ((((kind = 'wallet_credit'::text) AND (amount_cents IS NOT NULL) ` +
	`AND (amount_cents > 0) AND (compute_minutes IS NULL)) OR ((kind = 'compute_minutes'::text) ` +
	`AND (compute_minutes IS NOT NULL) AND (compute_minutes > 0) AND (amount_cents IS NULL))))`

// couponsUnionTable is the real control-plane shape: the payload union plus
// the single-column vocabulary on the discriminator, because that is how the
// pattern is actually written and the two have to agree.
func couponsUnionTable() schemadef.Table {
	return schemadef.Table{
		Name:   "coupons",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			{Name: "kind", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true},
			{Name: "amount_cents", DeclType: "BIGINT", Type: schemadef.TypeInt},
			{Name: "compute_minutes", DeclType: "BIGINT", Type: schemadef.TypeInt},
		},
		Checks: []schemadef.CheckConstraint{
			{
				Name:    "ck_cp_coupons_kind",
				Def:     "CHECK ((kind = ANY (ARRAY['wallet_credit'::text, 'compute_minutes'::text])))",
				Columns: []string{"kind"},
			},
			{
				Name:    "ck_cp_coupons_payload_matches_kind",
				Def:     couponsPayloadCheck,
				Columns: []string{"kind", "amount_cents", "compute_minutes"},
			},
		},
	}
}

// The discriminator is PINNED by the branch the creates are written against,
// on both creates. Drawn per column it walked the vocabulary — create #1 took
// `wallet_credit`, create #2 took `compute_minutes` — while the amount columns
// took independent values, so neither row satisfied either branch:
//
//	--- FAIL: TestCRUD_Coupon_Lifecycle
//	    create #1: invalid_argument: create coupon: a field value violates a constraint
func TestUnionFixtures_PinTheDiscriminator(t *testing.T) {
	fx := fixtureModel(t, []schemadef.Table{couponsUnionTable()}, "coupons")
	ent := EntityDef{TableName: "coupons"}

	v1, v2, ok := fx.deriveFieldFixture(ServiceDef{}, ent, "CreateCouponRequest", "kind", "string", FieldKindScalar)
	if !ok {
		t.Fatal("no fixture derived for a union discriminator")
	}
	if v1 != strconv.Quote("wallet_credit") {
		t.Errorf("create #1 kind = %s, want %s — the branch's own literal", v1, strconv.Quote("wallet_credit"))
	}
	if v2 != v1 {
		t.Errorf("create #2 kind = %s, want %s — both creates share one field list, so both take one branch", v2, v1)
	}
}

// The branch requires `compute_minutes IS NULL`, and there is no literal that
// means absent — so the field is left OFF the request, which is what writes
// NULL. Emitting the type's zero value is exactly the write the constraint
// rejects.
func TestUnionFixtures_OmitTheColumnsTheBranchForbids(t *testing.T) {
	fx := fixtureModel(t, []schemadef.Table{couponsUnionTable()}, "coupons")
	ent := EntityDef{TableName: "coupons"}

	if !fx.unionOmitsField(ent, "compute_minutes") {
		t.Error("compute_minutes must be omitted from the create request: the branch requires it to hold no value")
	}
	for _, present := range []string{"kind", "amount_cents"} {
		if fx.unionOmitsField(ent, present) {
			t.Errorf("%s is required to be PRESENT by the same branch and must not be omitted", present)
		}
	}
}

// A sibling the branch bounds takes a value inside that range. The range is an
// ADDITIONAL requirement on top of the column's own CHECKs, so `> 0` must
// reach the fixture even though nothing single-column says so.
func TestUnionFixtures_RespectTheBranchesRange(t *testing.T) {
	fx := fixtureModel(t, []schemadef.Table{couponsUnionTable()}, "coupons")
	ent := EntityDef{TableName: "coupons"}

	v1, v2, ok := fx.deriveFieldFixture(ServiceDef{}, ent, "CreateCouponRequest", "amount_cents", "int64", FieldKindScalar)
	if !ok {
		t.Fatal("no fixture derived for a column the union branch bounds")
	}
	for _, tc := range []struct{ label, got string }{{"create #1", v1}, {"create #2", v2}} {
		n, err := strconv.ParseInt(tc.got, 10, 64)
		if err != nil {
			t.Fatalf("%s amount_cents = %q, which is not an integer: %v", tc.label, tc.got, err)
		}
		if n <= 0 {
			t.Errorf("%s amount_cents = %d, which the branch's own `amount_cents > 0` rejects", tc.label, n)
		}
	}
}

// The union model is seedplan's, so a fixture and the seeded row it mirrors
// resolve to the SAME branch. A second reading of the CHECK here is exactly
// how the two would drift apart.
func TestUnionFixtures_AgreeWithTheSeededRow(t *testing.T) {
	fx := fixtureModel(t, []schemadef.Table{couponsUnionTable()}, "coupons")
	v1, _, ok := fx.deriveFieldFixture(ServiceDef{}, EntityDef{TableName: "coupons"},
		"CreateCouponRequest", "kind", "string", FieldKindScalar)
	if !ok {
		t.Fatal("no fixture derived")
	}
	tables := []schemadef.Table{couponsUnionTable()}
	plan, err := seedplan.BuildPlan(tables, seedplan.PoolsFromTables(tables), seedplan.Config{Rows: 4})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	plan.SetBounds(seedplan.BoundsFromTables(tables))
	seeded, okSeed := plan.SeedValue("coupons", "kind", 0)
	if !okSeed {
		t.Fatal("the seed plan has no value for coupons.kind row 0")
	}
	if strconv.Quote(seeded) != v1 {
		t.Errorf("create #1 kind = %s but seeded row 0 kind = %q — one schema must have one union model", v1, seeded)
	}
}

// A table with no union is untouched: the pass must not claim columns it has
// no constraint for.
func TestUnionFixtures_LeaveUnconstrainedTablesAlone(t *testing.T) {
	tbl := schemadef.Table{
		Name:   "notes",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			{Name: "body", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true},
		},
	}
	fx := fixtureModel(t, []schemadef.Table{tbl}, "notes")
	ent := EntityDef{TableName: "notes"}
	if fx.unionOmitsField(ent, "body") {
		t.Error("a table with no union CHECK must not have fields omitted")
	}
	v1, v2, ok := fx.deriveFieldFixture(ServiceDef{}, ent, "CreateNoteRequest", "body", "string", FieldKindScalar)
	if !ok || !strings.Contains(v1, "test-value") || v1 == v2 {
		t.Errorf("an unconstrained column keeps the legacy pair, got (%s, %s, ok=%v)", v1, v2, ok)
	}
}
