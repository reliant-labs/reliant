package codegen

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// The frontend mock generator used to invent its own demo vocabulary from
// column names: `name` drew from a company pool, `status` from {active,
// pending, ...}, a float called `win_probability` from [0,1), and a `<x>_id`
// column got a UUID keyed on the stem pluralized with an "s". One measured app
// therefore carried TWO demo vocabularies — the database said `sample_name_1`
// where the frontend said "Acme Corp" — and the invented values read no
// constraints at all, so a `sku` column whose CHECK is `^[A-Z]{3}-[0-9]{4}$`
// mocked as `"sample_sku_3"`: data the API the mock stands in for would
// reject.
//
// These tests pin the replacement: a mock value IS the value the project's own
// seed plan writes into that cell. Nothing here restates a heuristic — the
// expectations are read from the plan, and the constraint assertions are
// checked against the CHECK the schema declares.

// mockTestSchema is a two-table schema carrying the three things the old
// generator got wrong: a CHECK-constrained string, an ordinary undescribed
// string, and a real foreign key.
func mockTestSchema() []schemadef.Table {
	col := func(name string, typ schemadef.CanonicalType, notNull, pk bool) schemadef.Column {
		return schemadef.Column{Name: name, Type: typ, NotNull: notNull, IsPK: pk}
	}
	categories := schemadef.Table{
		Name:   "categories",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("name", schemadef.TypeString, true, false),
		},
	}
	products := schemadef.Table{
		Name:   "products",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("name", schemadef.TypeString, true, false),
			col("sku", schemadef.TypeString, true, false),
			col("price_cents", schemadef.TypeInt, true, false),
			col("category_id", schemadef.TypeString, true, false),
		},
		Checks: []schemadef.CheckConstraint{{
			Name:    "products_sku_check",
			Def:     `CHECK ((sku ~ '^[A-Z]{3}-[0-9]{4}$'::text))`,
			Columns: []string{"sku"},
		}},
		ForeignKeys: []schemadef.ForeignKey{{Column: "category_id", RefTable: "categories", RefColumn: "id"}},
	}
	return []schemadef.Table{products, categories}
}

// mockTestProductEntity is the Product wire message over that table.
func mockTestProductEntity() EntityDef {
	str := func(name, goName string) EntityField {
		return EntityField{Name: name, GoName: goName, ProtoType: "string", GoType: "string", Kind: FieldKindScalar}
	}
	return EntityDef{
		Name: "Product", TableName: "products", PkField: "id",
		Fields: []EntityField{
			str("id", "Id"),
			str("name", "Name"),
			str("sku", "Sku"),
			{Name: "price_cents", GoName: "PriceCents", ProtoType: "int32", GoType: "int32", Kind: FieldKindScalar},
			str("category_id", "CategoryId"),
		},
	}
}

// mockRecordValue returns the TypeScript literal a rendered record carries for
// one camelCase field.
func mockRecordValue(t *testing.T, rec MockRecord, field string) string {
	t.Helper()
	for _, f := range rec.Fields {
		if f.Name == field {
			return f.Value
		}
	}
	t.Fatalf("mock record carries no field %q (has %v)", field, rec.Fields)
	return ""
}

// TestMockValuesAreTheSeededValues is the agreement assertion: every mock cell
// holds what the seed plan writes at the same (table, column, row).
func TestMockValuesAreTheSeededValues(t *testing.T) {
	tables := mockTestSchema()
	seed := newSeedProjection(tables, seedplan.DefaultConfig(), nil)
	if seed == nil {
		t.Fatal("the planner refused this schema — nothing below would be comparing anything")
	}

	data := EntityDefToMockData(mockTestProductEntity(), ServiceDef{Name: "CatalogService"}, seed)
	if len(data.Records) == 0 {
		t.Fatal("no mock records rendered — every assertion below would pass vacuously")
	}

	for i, rec := range data.Records {
		for _, tc := range []struct{ column, field string }{
			{"id", "id"}, {"name", "name"}, {"sku", "sku"}, {"category_id", "categoryId"},
		} {
			want, ok := seed.Value("products", tc.column, i)
			if !ok {
				t.Fatalf("row %d: the plan holds no value for products.%s — the comparison has no oracle", i, tc.column)
			}
			if got := mockRecordValue(t, rec, tc.field); got != strconv.Quote(want) {
				t.Errorf("row %d: mock %s = %s, but the database will hold %q — one app, two datasets",
					i, tc.field, got, want)
			}
		}
		want, ok := seed.Value("products", "price_cents", i)
		if !ok {
			t.Fatalf("row %d: the plan holds no value for products.price_cents", i)
		}
		if got := mockRecordValue(t, rec, "priceCents"); got != want {
			t.Errorf("row %d: mock priceCents = %s, but the database will hold %s", i, got, want)
		}
	}
}

// TestMockValuesSatisfyTheirCheckConstraints is the half that makes the mocks
// usable: a value the mock hands the UI is one the real API would accept.
func TestMockValuesSatisfyTheirCheckConstraints(t *testing.T) {
	tables := mockTestSchema()
	seed := newSeedProjection(tables, seedplan.DefaultConfig(), nil)
	if seed == nil {
		t.Fatal("the planner refused this schema")
	}
	data := EntityDefToMockData(mockTestProductEntity(), ServiceDef{Name: "CatalogService"}, seed)

	sku := regexp.MustCompile(`^[A-Z]{3}-[0-9]{4}$`)
	for i, rec := range data.Records {
		raw, err := strconv.Unquote(mockRecordValue(t, rec, "sku"))
		if err != nil {
			t.Fatalf("row %d: mock sku is not a string literal: %v", i, err)
		}
		if !sku.MatchString(raw) {
			t.Errorf("row %d: mock sku %q violates the column's own CHECK %s — the API this mock "+
				"stands in for would reject it", i, raw, sku)
		}
	}
}

// TestMockReferencesResolveWithinTheFixtureSet pins that a foreign key points
// at a row that EXISTS. The pluralize-the-stem guess this replaced named
// `categorys`, so `product.categoryId` referenced ids no category fixture
// carried and every mock join came back empty.
func TestMockReferencesResolveWithinTheFixtureSet(t *testing.T) {
	tables := mockTestSchema()
	seed := newSeedProjection(tables, seedplan.DefaultConfig(), nil)
	if seed == nil {
		t.Fatal("the planner refused this schema")
	}

	categoryIDs := map[string]bool{}
	for i := 0; i < seed.Rows("categories"); i++ {
		if v, ok := seed.Value("categories", "id", i); ok {
			categoryIDs[v] = true
		}
	}
	if len(categoryIDs) == 0 {
		t.Fatal("no category ids in the dataset — the membership check would pass vacuously")
	}

	data := EntityDefToMockData(mockTestProductEntity(), ServiceDef{Name: "CatalogService"}, seed)
	for i, rec := range data.Records {
		raw, err := strconv.Unquote(mockRecordValue(t, rec, "categoryId"))
		if err != nil {
			t.Fatalf("row %d: mock categoryId is not a string literal: %v", i, err)
		}
		if !categoryIDs[raw] {
			t.Errorf("row %d: mock categoryId %q is not the id of any seeded category", i, raw)
		}
	}
}

// TestMockValuesCarryTheProjectsVocabulary pins that db/seeds/vocab.yaml —
// the one place a project declares what its columns MEAN — reaches the
// frontend fixtures too. It is the whole reason the mocks have no pools of
// their own: a project teaches forge its vocabulary once.
func TestMockValuesCarryTheProjectsVocabulary(t *testing.T) {
	vocab := &seedplan.Vocab{Columns: map[string][]string{
		"products.name": {"BPC-157", "Semaglutide", "Tirzepatide"},
	}}
	seed := newSeedProjection(mockTestSchema(), seedplan.DefaultConfig(), vocab)
	if seed == nil {
		t.Fatal("the planner refused this schema")
	}
	declared := map[string]bool{"BPC-157": true, "Semaglutide": true, "Tirzepatide": true}

	data := EntityDefToMockData(mockTestProductEntity(), ServiceDef{Name: "CatalogService"}, seed)
	for i, rec := range data.Records {
		raw, err := strconv.Unquote(mockRecordValue(t, rec, "name"))
		if err != nil {
			t.Fatalf("row %d: mock name is not a string literal: %v", i, err)
		}
		if !declared[raw] {
			t.Errorf("row %d: mock name = %q, which is not one of the values the project declared for "+
				"products.name — the frontend is inventing vocabulary again", i, raw)
		}
	}
}

// TestMockValuesWithoutADatasetAreSelfEvidentlySynthetic covers the other
// branch: no migrations, no reachable shadow server. Forge then knows nothing
// about the domain and says so, in the same words the seeder uses for a column
// nothing describes — never a plausible-looking company name.
func TestMockValuesWithoutADatasetAreSelfEvidentlySynthetic(t *testing.T) {
	data := EntityDefToMockData(mockTestProductEntity(), ServiceDef{Name: "CatalogService"}, nil)
	if len(data.Records) == 0 {
		t.Fatal("no mock records rendered")
	}
	for i, rec := range data.Records {
		for _, field := range []string{"name", "sku"} {
			raw, err := strconv.Unquote(mockRecordValue(t, rec, field))
			if err != nil {
				t.Fatalf("row %d: mock %s is not a string literal: %v", i, field, err)
			}
			if !strings.HasPrefix(raw, seedplan.SyntheticStringPrefix) {
				t.Errorf("row %d: mock %s = %q carries no %q stamp — a value that does not announce "+
					"itself as invented cannot be told apart from one the app produced",
					i, field, raw, seedplan.SyntheticStringPrefix)
			}
		}
	}
}

// TestMockRecordCountFollowsTheDataset pins that the fixtures are the FIRST N
// rows of the dev database and stop where it stops. Padding past its end would
// put the two back into disagreement on exactly the rows nobody looked at.
func TestMockRecordCountFollowsTheDataset(t *testing.T) {
	cfg := seedplan.DefaultConfig()
	cfg.Rows = 3
	seed := newSeedProjection(mockTestSchema(), cfg, nil)
	if seed == nil {
		t.Fatal("the planner refused this schema")
	}
	data := EntityDefToMockData(mockTestProductEntity(), ServiceDef{Name: "CatalogService"}, seed)
	if len(data.Records) != 3 {
		t.Errorf("rendered %d mock records for a 3-row dataset, want 3", len(data.Records))
	}

	// A dataset larger than the fixture page still renders one page.
	cfg.Rows = 50
	seed = newSeedProjection(mockTestSchema(), cfg, nil)
	data = EntityDefToMockData(mockTestProductEntity(), ServiceDef{Name: "CatalogService"}, seed)
	if len(data.Records) != mockRecordCount {
		t.Errorf("rendered %d mock records for a 50-row dataset, want %d", len(data.Records), mockRecordCount)
	}
}

// Booleans and enums were the two kinds still inventing their own values
// after every other kind had been routed to the seed plan.
//
// A boolean fell back because the plan's scalar DECODER rejected it: a
// seeded bool cell renders as the SQL keyword `true`/`false`, and the
// decoder's bare-numeric scan accepts only digits, so `SeedValue` answered
// "not a scalar" and the mock generator emitted its own `i%2` alternation.
// That is not a cosmetic disagreement — the seeder deliberately makes two
// bool columns vary INDEPENDENTLY (see seeddata's
// TestSeedBooleans_AreIndependentAcrossColumns, which exists because a
// parity bug once made `active` and `requires_prescription` opposite in
// every row); the mocks' `i%2` gives every bool column on a table the
// IDENTICAL value in every row, so a fixture set can only ever show two of
// the four combinations.
//
// An enum fell back because nothing consulted the plan for it at all: the
// generator returned the constant `1` — the first non-UNSPECIFIED value —
// for every enum field in every row. A ten-row fixture set therefore showed
// one status, so a list page's status column rendered a single repeated
// badge and its enum filter matched all rows or none.

// mockEnumTestSchema is a one-table schema with a boolean column and a
// CHECK-vocabulary status column — the two kinds under test, alongside the
// primary key.
func mockEnumTestSchema() []schemadef.Table {
	return []schemadef.Table{{
		Name:   "orders",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			{Name: "is_paid", Type: schemadef.TypeBool, NotNull: true, DeclType: "boolean"},
			{Name: "settled", Type: schemadef.TypeBool, NotNull: true, DeclType: "boolean"},
			{Name: "status", Type: schemadef.TypeString, NotNull: true, DeclType: "text"},
		},
		Checks: []schemadef.CheckConstraint{{
			Name: "orders_status_check",
			Def: `CHECK ((status = ANY (ARRAY['ORDER_STATUS_PENDING'::text, ` +
				`'ORDER_STATUS_ACTIVE'::text, 'ORDER_STATUS_CLOSED'::text])))`,
			Columns: []string{"status"},
		}},
	}}
}

// mockEnumTestOrderEntity is the Order wire message over that table, and
// mockEnumTestService the enum declaration behind its status field.
func mockEnumTestOrderEntity() EntityDef {
	return EntityDef{
		Name: "Order", TableName: "orders", PkField: "id",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "is_paid", GoName: "IsPaid", ProtoType: "bool", GoType: "bool", Kind: FieldKindScalar},
			{Name: "settled", GoName: "Settled", ProtoType: "bool", GoType: "bool", Kind: FieldKindScalar},
			{
				Name: "status", GoName: "Status", ProtoType: "enum", GoType: "pb.OrderStatus",
				Kind: FieldKindEnum, MessageType: "orders.v1.OrderStatus",
			},
		},
	}
}

func mockEnumTestService() ServiceDef {
	return ServiceDef{
		Name: "OrderService", Package: "orders.v1",
		Enums: map[string][]string{"orders.v1.OrderStatus": {
			"ORDER_STATUS_UNSPECIFIED",
			"ORDER_STATUS_PENDING",
			"ORDER_STATUS_ACTIVE",
			"ORDER_STATUS_CLOSED",
		}},
	}
}

// TestMockBooleansAreTheSeededBooleans pins that a bool mock cell holds
// what the plan writes at the same (table, column, row) — the same
// agreement every other kind already had.
func TestMockBooleansAreTheSeededBooleans(t *testing.T) {
	tables := mockEnumTestSchema()
	seed := newSeedProjection(tables, seedplan.DefaultConfig(), nil)
	if seed == nil {
		t.Fatal("the planner refused this schema — nothing below would be comparing anything")
	}
	data := EntityDefToMockData(mockEnumTestOrderEntity(), mockEnumTestService(), seed)
	if len(data.Records) == 0 {
		t.Fatal("no mock records rendered — every assertion below would pass vacuously")
	}

	for i, rec := range data.Records {
		for _, tc := range []struct{ column, field string }{
			{"is_paid", "isPaid"}, {"settled", "settled"},
		} {
			want, ok := seed.Value("orders", tc.column, i)
			if !ok {
				t.Fatalf("row %d: the plan holds no value for orders.%s — the comparison has no oracle",
					i, tc.column)
			}
			if got := mockRecordValue(t, rec, tc.field); got != want {
				t.Errorf("row %d: mock %s = %s, but the database will hold %s — one app, two datasets",
					i, tc.field, got, want)
			}
		}
	}
}

// TestMockBooleanColumnsVaryIndependently is the property the fallback
// destroyed: the generic `i%2` gave every bool column the same value in the
// same row, so `is_paid` and `settled` were identical in all ten rows and a
// fixture set could show only two of the four combinations. The seeder's
// per-cell hash makes them independent; reading it preserves that.
func TestMockBooleanColumnsVaryIndependently(t *testing.T) {
	seed := newSeedProjection(mockEnumTestSchema(), seedplan.DefaultConfig(), nil)
	if seed == nil {
		t.Fatal("the planner refused this schema")
	}
	data := EntityDefToMockData(mockEnumTestOrderEntity(), mockEnumTestService(), seed)
	if len(data.Records) == 0 {
		t.Fatal("no mock records rendered")
	}

	// The obligation is derived from the seeded dataset, not asserted
	// against a remembered number: however many distinct (is_paid, settled)
	// combinations the DATABASE will show, the fixtures must show the same.
	seeded := map[string]bool{}
	for i := range data.Records {
		a, okA := seed.Value("orders", "is_paid", i)
		b, okB := seed.Value("orders", "settled", i)
		if !okA || !okB {
			t.Fatalf("row %d: the plan holds no boolean pair — the comparison has no oracle", i)
		}
		seeded[a+","+b] = true
	}
	if len(seeded) < 2 {
		t.Fatalf("the seeded dataset itself shows only %d combination(s) of two bool columns — "+
			"this fixture cannot detect the collapse it exists to detect", len(seeded))
	}

	mocked := map[string]bool{}
	for _, rec := range data.Records {
		mocked[mockRecordValue(t, rec, "isPaid")+","+mockRecordValue(t, rec, "settled")] = true
	}
	if len(mocked) != len(seeded) {
		t.Errorf("the fixtures show %d combination(s) of (isPaid, settled) but the seeded database "+
			"shows %d — a UI exercised against these mocks cannot reach a state the real data has",
			len(mocked), len(seeded))
	}
}

// TestMockEnumsAreTheSeededEnums pins that an enum mock cell is the wire
// number of the value the plan writes — resolved through the enum's own
// declaration order, never the constant the generator used to emit.
func TestMockEnumsAreTheSeededEnums(t *testing.T) {
	svc := mockEnumTestService()
	seed := newSeedProjection(mockEnumTestSchema(), seedplan.DefaultConfig(), nil)
	if seed == nil {
		t.Fatal("the planner refused this schema")
	}
	data := EntityDefToMockData(mockEnumTestOrderEntity(), svc, seed)
	if len(data.Records) == 0 {
		t.Fatal("no mock records rendered — every assertion below would pass vacuously")
	}

	// The expected ordinal is read from the DECLARATION, so a renumbered or
	// reordered enum moves this test's expectation with it.
	values := svc.Enums["orders.v1.OrderStatus"]
	if len(values) == 0 {
		t.Fatal("the service declares no enum values — the comparison has no oracle")
	}
	ordinal := map[string]int{}
	for n, name := range values {
		ordinal[name] = n
	}

	for i, rec := range data.Records {
		raw, ok := seed.Value("orders", "status", i)
		if !ok {
			t.Fatalf("row %d: the plan holds no value for orders.status — the comparison has no oracle", i)
		}
		want, known := ordinal[raw]
		if !known {
			t.Fatalf("row %d: the plan seeded %q, which the enum does not declare", i, raw)
		}
		if got := mockRecordValue(t, rec, "status"); got != strconv.Itoa(want) {
			t.Errorf("row %d: mock status = %s, but the database will hold %q (= %d) — "+
				"the fixture names a different member than the row it stands in for",
				i, got, raw, want)
		}
	}
}

// TestMockEnumsShowEveryValueTheDatasetShows is the property the constant
// destroyed: emitting `1` for every row meant a list page rendered one
// repeated badge and an enum filter matched all rows or none, whatever the
// database actually held.
func TestMockEnumsShowEveryValueTheDatasetShows(t *testing.T) {
	seed := newSeedProjection(mockEnumTestSchema(), seedplan.DefaultConfig(), nil)
	if seed == nil {
		t.Fatal("the planner refused this schema")
	}
	data := EntityDefToMockData(mockEnumTestOrderEntity(), mockEnumTestService(), seed)
	if len(data.Records) == 0 {
		t.Fatal("no mock records rendered")
	}

	seeded := map[string]bool{}
	for i := range data.Records {
		v, ok := seed.Value("orders", "status", i)
		if !ok {
			t.Fatalf("row %d: the plan holds no status — the comparison has no oracle", i)
		}
		seeded[v] = true
	}
	if len(seeded) < 2 {
		t.Fatalf("the seeded dataset shows only %d status value(s) — this fixture cannot detect "+
			"the collapse it exists to detect", len(seeded))
	}

	mocked := map[string]bool{}
	for _, rec := range data.Records {
		mocked[mockRecordValue(t, rec, "status")] = true
	}
	if len(mocked) != len(seeded) {
		t.Errorf("the fixtures show %d distinct status value(s) but the seeded database shows %d — "+
			"a status column or filter exercised against these mocks cannot reach a value the "+
			"real data has", len(mocked), len(seeded))
	}
}
