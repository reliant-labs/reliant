package codegen

import (
	"context"
	"encoding/json"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// fixtureSchema builds the constraint-heavy shop schema the fixture tests
// run against: a `products` entity table carrying every failure class the
// blind `"test-value"` fixtures hit in the wild — a NOT NULL foreign key,
// an email-regex CHECK, an enum-vocabulary CHECK, a char_length CHECK, a
// numeric range CHECK, and a single-column UNIQUE index — plus its
// `brands` FK parent.
func fixtureSchema() []schemadef.Table {
	c := func(name string, typ schemadef.CanonicalType, notNull, pk bool) schemadef.Column {
		return schemadef.Column{Name: name, Type: typ, NotNull: notNull, IsPK: pk}
	}
	brands := schemadef.Table{
		Name:   "brands",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			c("id", schemadef.TypeString, true, true),
			c("name", schemadef.TypeString, true, false),
		},
	}
	products := schemadef.Table{
		Name:   "products",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			c("id", schemadef.TypeString, true, true),
			c("region", schemadef.TypeString, true, false),
			c("contact_email", schemadef.TypeString, true, false),
			c("currency", schemadef.TypeString, true, false),
			c("status", schemadef.TypeString, true, false),
			c("brand_id", schemadef.TypeString, true, false),
			c("sku", schemadef.TypeString, true, false),
			c("name", schemadef.TypeString, true, false),
			c("price_cents", schemadef.TypeInt, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{{Column: "brand_id", RefTable: "brands", RefColumn: "id"}},
		Indexes:     []schemadef.Index{{Name: "products_sku_key", Columns: []string{"sku"}, Unique: true}},
		Checks: []schemadef.CheckConstraint{
			// pg_get_constraintdef canonical spellings.
			{Name: "products_contact_email_check", Def: `CHECK ((contact_email ~* '^[^@]+@[^@]+\.[^@]+$'::text))`, Columns: []string{"contact_email"}},
			{Name: "products_currency_check", Def: `CHECK ((char_length(currency) = 3))`, Columns: []string{"currency"}},
			{Name: "products_status_check", Def: `CHECK ((status = ANY (ARRAY['PRODUCT_STATUS_ACTIVE'::text, 'PRODUCT_STATUS_DISCONTINUED'::text])))`, Columns: []string{"status"}},
			{Name: "products_price_cents_check", Def: `CHECK ((price_cents >= 100))`, Columns: []string{"price_cents"}},
		},
	}
	return []schemadef.Table{brands, products}
}

func fixtureModel(t *testing.T, tables []schemadef.Table, entityTables ...string) *crudTestFixtures {
	t.Helper()
	byName := map[string]schemadef.Table{}
	for _, tb := range tables {
		byName[tb.Name] = tb
	}
	fx := &crudTestFixtures{
		tables: byName,
		pools:  seedplan.PoolsFromTables(tables),
		bounds: seedplan.BoundsFromTables(tables),
		plans:  map[string]*entitySeedPlan{},
	}
	for _, tn := range entityTables {
		fx.plans[tn] = fx.buildEntitySeedPlan(tn)
	}
	return fx
}

func productSvcAndMethods() (ServiceDef, []CRUDMethod) {
	entity := EntityDef{
		Name:      "Product",
		TableName: "products",
		PkField:   "id",
		PkGoType:  "string",
		// contact_email is deliberately the FIRST non-PK string field: the
		// old mutable-field pick would choose it and the update leg would
		// then write "lifecycle-updated" into a regex-CHECKed column.
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "contact_email", GoName: "ContactEmail", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "currency", GoName: "Currency", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "status", GoName: "Status", ProtoType: "enum", GoType: "ProductStatus", Kind: FieldKindEnum, MessageType: "shop.v1.ProductStatus"},
			{Name: "brand_id", GoName: "BrandId", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "sku", GoName: "Sku", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "name", GoName: "Name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "price_cents", GoName: "PriceCents", ProtoType: "int64", GoType: "int64", Kind: FieldKindScalar},
		},
	}
	svc := ServiceDef{
		Name:       "ShopService",
		Package:    "shop.v1",
		GoPackage:  "example.com/test/gen/proto/services/shop/v1",
		PkgName:    "shopv1",
		ModulePath: "example.com/test",
		Messages: map[string][]MessageFieldDef{
			"CreateProductRequest": {
				{Name: "contact_email", ProtoType: "string"},
				{Name: "currency", ProtoType: "string"},
				{Name: "status", ProtoType: "enum"},
				{Name: "brand_id", ProtoType: "string"},
				{Name: "sku", ProtoType: "string"},
				{Name: "name", ProtoType: "string"},
				{Name: "price_cents", ProtoType: "int64"},
			},
		},
		Schemas: map[string][]SchemaFieldDef{
			"shop.v1.CreateProductRequest": {
				{Name: "status", Kind: "enum", TypeName: "shop.v1.ProductStatus"},
			},
		},
		Enums: map[string][]string{
			"shop.v1.ProductStatus": {"PRODUCT_STATUS_UNSPECIFIED", "PRODUCT_STATUS_ACTIVE", "PRODUCT_STATUS_DISCONTINUED"},
		},
	}
	methods := []CRUDMethod{
		{Method: MethodTemplateData{Name: "CreateProduct", InputType: "CreateProductRequest", OutputType: "CreateProductResponse"}, Entity: entity, Operation: "create"},
		{Method: MethodTemplateData{Name: "GetProduct", InputType: "GetProductRequest", OutputType: "GetProductResponse"}, Entity: entity, Operation: "get"},
		{Method: MethodTemplateData{Name: "UpdateProduct", InputType: "UpdateProductRequest", OutputType: "UpdateProductResponse"}, Entity: entity, Operation: "update"},
	}
	return svc, methods
}

// createFieldsByName indexes one create RPC's derived request fields.
func createFieldsByName(svc ServiceDef, cm CRUDMethod, fx *crudTestFixtures) (map[string]CRUDTestFieldData, []CRUDTestFieldData) {
	fields := buildCreateRequestFields(svc, cm, fx)
	byName := map[string]CRUDTestFieldData{}
	for _, f := range fields {
		byName[f.ProtoName] = f
	}
	return byName, fields
}

// renderFactoriesForTest renders a factories_gen_test.go carrying one create
// RPC's request factory and asserts it is valid, gofmt-clean Go.
func renderFactoriesForTest(t *testing.T, svc ServiceDef, cm CRUDMethod, fx *crudTestFixtures) string {
	t.Helper()
	spec := createRequestSpec{
		funcName:  createRequestFactoryName(cm),
		inputType: cm.Method.InputType,
		entity:    cm.Entity.Name,
		fields:    buildCreateRequestFields(svc, cm, fx),
	}
	if esp := fx.plans[cm.Entity.TableName]; esp != nil {
		spec.parentSQL = esp.seedSQL
	}
	out := renderEntityFactoryFile("example.com/test/internal/db", factoryGroup{
		pkg: "shop", pbImport: "example.com/test/gen/services/shop/v1", creates: []createRequestSpec{spec},
	})
	if _, err := format.Source(out); err != nil {
		t.Fatalf("rendered factories_gen_test.go is not valid Go: %v\n----\n%s", err, out)
	}
	return string(out)
}

// TestCRUDTestFixtures_ConstraintAwareCreateValues pins the create-fixture
// contract: every literal the create-request factory carries satisfies the
// column's schema constraints, and every field differs between variant 0 and
// variant 1.
func TestCRUDTestFixtures_ConstraintAwareCreateValues(t *testing.T) {
	fx := fixtureModel(t, fixtureSchema(), "products")
	svc, methods := productSvcAndMethods()
	byName, fields := createFieldsByName(svc, methods[0], fx)

	// FK → the seeded brands rows: row 0 for variant 0, row 1 for variant 1.
	// Distinct parents, so a UNIQUE index added later on the FK column (a
	// 1-1 relationship, the most common evolution of one) cannot collide the
	// two rows a test creates.
	esp := fx.plans["products"]
	if esp == nil || esp.plan == nil {
		t.Fatal("expected a seed plan for products")
	}
	wantBrand, ok := esp.plan.SeedValue("brands", "id", 0)
	if !ok {
		t.Fatal("SeedValue(brands, id, 0) not available")
	}
	wantBrand2, ok := esp.plan.SeedValue("brands", "id", 1)
	if !ok {
		t.Fatal("SeedValue(brands, id, 1) not available")
	}
	if got := byName["BrandId"]; got.TestValue != `"`+wantBrand+`"` || got.TestValue2 != `"`+wantBrand2+`"` {
		t.Errorf("BrandId fixture = (%s, %s), want (%q, %q)", got.TestValue, got.TestValue2, wantBrand, wantBrand2)
	}
	// ... and the seeded parent row must actually be in the parent SQL.
	parentSQL := fx.seedSQLFor("products")
	if !strings.Contains(parentSQL, `INSERT INTO "brands"`) || !strings.Contains(parentSQL, wantBrand) {
		t.Errorf("parent SQL must insert the referenced brands row; got:\n%s", parentSQL)
	}
	if strings.Contains(parentSQL, `INSERT INTO "products"`) {
		t.Errorf("parent SQL must NOT seed the entity's own table (it would break the list row-count assertion); got:\n%s", parentSQL)
	}

	// Email-regex CHECK → a real address.
	if got := byName["ContactEmail"].TestValue; !strings.Contains(got, "@") || !strings.Contains(got, ".") {
		t.Errorf("ContactEmail fixture %s does not look like an email", got)
	}
	// char_length(currency) = 3 → exactly 3 characters.
	if got := strings.Trim(byName["Currency"].TestValue, `"`); len(got) != 3 {
		t.Errorf("Currency fixture %q must be exactly 3 chars (char_length CHECK)", got)
	}
	// Enum vocabulary CHECK on a column with no DEFAULT → the minimal row
	// must write it, so it is set, to a real pb constant (never the 0
	// sentinel).
	if got := byName["Status"].TestValue; got != "pb.ProductStatus_PRODUCT_STATUS_ACTIVE" {
		t.Errorf("Status fixture = %s, want pb.ProductStatus_PRODUCT_STATUS_ACTIVE", got)
	}
	// Range CHECK price_cents >= 100 → clamped into range.
	if got := byName["PriceCents"].TestValue; got != "100" {
		t.Errorf("PriceCents fixture = %s, want 100 (range CHECK clamp)", got)
	}
	// Unconstrained name → the legacy literal, and a DISTINCT second one.
	if got := byName["Name"]; got.TestValue != `"test-value"` || got.TestValue2 != `"test-value-2"` {
		t.Errorf("Name fixture = (%s, %s), want (\"test-value\", \"test-value-2\")", got.TestValue, got.TestValue2)
	}
	// Every field differs across the two variants, so any UNIQUE index the
	// author adds later leaves the lifecycle test passing.
	for _, f := range fields {
		if f.TestValue == f.TestValue2 {
			t.Errorf("field %s carries the same value on both variants (%s) — a UNIQUE index would break create #2",
				f.ProtoName, f.TestValue)
		}
	}
	sku := byName["Sku"]

	// The mutation target skips constrained columns: contact_email (regex
	// CHECK) is the first string field, but "lifecycle-updated" would
	// violate its CHECK — sku (unique, unconstrained) is the right pick.
	data := buildCRUDTestTemplateData(svc, methods, "example.com/test", "", fx)
	if len(data.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(data.Entities))
	}
	ent := data.Entities[0]
	if ent.MutableStringField != "Sku" {
		t.Errorf("MutableStringField = %q, want Sku (constrained columns are not mutation targets)", ent.MutableStringField)
	}

	// The factory file carries the parent seed and both variants.
	content := renderFactoriesForTest(t, svc, methods[0], fx)
	for _, want := range []string{
		"func NewCreateProductRequest(t testing.TB, database orm.Context, variant int) *pb.CreateProductRequest",
		"seedFactoryParents(t, database, createProductRequestParentSQL)",
		`INSERT INTO "brands"`,
		sku.TestValue2, // variant 1 carries the distinct unique value
	} {
		if !strings.Contains(content, want) {
			t.Errorf("rendered factories missing %q\n----\n%s", want, content)
		}
	}
}

// TestCRUDTestFixtures_LifecycleTestCarriesNoFixtures pins the shape this
// whole file exists for: the scaffold-once lifecycle test builds its rows from
// the regenerated create-request factory and carries NO literal fixture —
// no INSERT, no request field values — so nothing in it can age out of a
// schema the author keeps hardening.
func TestCRUDTestFixtures_LifecycleTestCarriesNoFixtures(t *testing.T) {
	fx := fixtureModel(t, fixtureSchema(), "products")
	svc, methods := productSvcAndMethods()
	data := buildCRUDTestTemplateData(svc, methods, "example.com/test", "", fx)
	data.Package = "shop"
	data.ServiceImportPath = "shop"
	rendered, err := templates.ServiceTemplates().Render("handlers_crud_test.go.tmpl", data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	content := string(rendered)
	if _, err := parser.ParseFile(token.NewFileSet(), "handlers_crud_test.go", content, parser.SkipObjectResolution); err != nil {
		t.Fatalf("rendered lifecycle test is not valid Go: %v\n----\n%s", err, content)
	}
	for _, want := range []string{
		"shop.NewCreateProductRequest(t, db, 0)",
		"shop.NewCreateProductRequest(t, db, 1)",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("lifecycle test does not build its rows from the factory (%q missing)\n----\n%s", want, content)
		}
	}
	for _, frozen := range []string{"INSERT INTO", "seed parent rows", "ContactEmail:", "&pb.CreateProductRequest{"} {
		if strings.Contains(content, frozen) {
			t.Errorf("lifecycle test carries a frozen fixture (%q) — it would age out of the schema\n----\n%s", frozen, content)
		}
	}
}

// TestCRUDTestFixtures_NilModelKeepsLegacyValues pins the degradation
// contract: with no schema model the values are the type-blind legacy ones and
// there is no parent SQL. The two variants still differ — that property is
// about the schema the author will write LATER, so it cannot depend on forge
// having read one.
func TestCRUDTestFixtures_NilModelKeepsLegacyValues(t *testing.T) {
	svc, methods := productSvcAndMethods()
	_, fields := createFieldsByName(svc, methods[0], nil)
	for _, f := range fields {
		// An enum with no descriptor behind it degrades to the numeric zero
		// value and has no second spelling to pick — the one shape that
		// legitimately repeats.
		if f.Kind == FieldKindEnum || f.TestValue == "0" || f.TestValue == "nil" {
			continue
		}
		if f.TestValue == f.TestValue2 {
			t.Errorf("field %s carries the same value on both variants (%s) without a schema model", f.ProtoName, f.TestValue)
		}
		if f.GoType == "string" && f.TestValue != `"test-value"` {
			t.Errorf("field %s: variant 0 = %s, want the legacy \"test-value\"", f.ProtoName, f.TestValue)
		}
	}
	// First string field is the mutation target again (no constraint model).
	data := buildCRUDTestTemplateData(svc, methods, "example.com/test", "", nil)
	if ent := data.Entities[0]; ent.MutableStringField != "ContactEmail" {
		t.Errorf("MutableStringField = %q, want ContactEmail (legacy first-string pick)", ent.MutableStringField)
	}
}

// TestCRUDTestFixtures_VocabFlowsIntoParentSeed pins the vocab wiring: the
// db/seeds/vocab.yaml overlay applies to the parent-closure seed plans, so
// fixture parent rows carry the same domain values the dev dataset does
// (same Plan, same determinism). Referential columns (the brands.id the FK
// fixture references) stay the seeder's — vocab never touches them.
func TestCRUDTestFixtures_VocabFlowsIntoParentSeed(t *testing.T) {
	tables := fixtureSchema()
	byName := map[string]schemadef.Table{}
	for _, tb := range tables {
		byName[tb.Name] = tb
	}
	fx := &crudTestFixtures{
		tables: byName,
		pools:  seedplan.PoolsFromTables(tables),
		bounds: seedplan.BoundsFromTables(tables),
		vocab:  &seedplan.Vocab{Columns: map[string][]string{"brands.name": {"VitalPep", "PepCore Labs"}}},
		plans:  map[string]*entitySeedPlan{},
	}
	fx.plans["products"] = fx.buildEntitySeedPlan("products")

	esp := fx.plans["products"]
	if esp == nil || esp.plan == nil {
		t.Fatal("expected a seed plan for products")
	}
	allowed := map[string]bool{"VitalPep": true, "PepCore Labs": true}
	for row := 0; row < 2; row++ {
		v, ok := esp.plan.SeedValue("brands", "name", row)
		if !ok || !allowed[v] {
			t.Errorf("seeded brands.name row %d = %q (ok=%v), want a vocab value", row, v, ok)
		}
	}
	if !strings.Contains(esp.seedSQL, "VitalPep") && !strings.Contains(esp.seedSQL, "PepCore Labs") {
		t.Errorf("parent seed SQL must carry the vocab values; got:\n%s", esp.seedSQL)
	}
}

// TestCRUDTestFixtures_UnsatisfiableClosureFallsBack pins graceful
// degradation: a NOT NULL FK cycle back through the entity itself cannot
// be seeded — the entity keeps legacy fixtures instead of failing the
// generate.
func TestCRUDTestFixtures_UnsatisfiableClosureFallsBack(t *testing.T) {
	c := func(name string, typ schemadef.CanonicalType, notNull, pk bool) schemadef.Column {
		return schemadef.Column{Name: name, Type: typ, NotNull: notNull, IsPK: pk}
	}
	// items.list_id → lists.id (NOT NULL), lists.cover_item_id → items.id (NOT NULL).
	lists := schemadef.Table{
		Name:   "lists",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			c("id", schemadef.TypeString, true, true),
			c("cover_item_id", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{{Column: "cover_item_id", RefTable: "items", RefColumn: "id"}},
	}
	items := schemadef.Table{
		Name:   "items",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			c("id", schemadef.TypeString, true, true),
			c("list_id", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{{Column: "list_id", RefTable: "lists", RefColumn: "id"}},
	}
	fx := fixtureModel(t, []schemadef.Table{lists, items}, "items")
	if fx.plans["items"] != nil {
		t.Error("expected nil seed plan for an unsatisfiable NOT NULL FK cycle through the entity")
	}
	if got := fx.seedSQLFor("items"); got != "" {
		t.Errorf("expected empty seed SQL, got:\n%s", got)
	}
	ent := EntityDef{Name: "Item", TableName: "items", PkField: "id", PkGoType: "string"}
	if _, _, ok := fx.fieldFixture(ServiceDef{}, ent, "CreateItemRequest", "list_id", "string", FieldKindScalar); ok {
		t.Error("FK fixture must fall back (ok=false) when the closure is unseedable")
	}
}

// TestCRUDTestFixtures_JSONFieldFixtureIsValidJSON pins the JSON/JSONB
// contract: a json column parses its input as a JSON document, so the
// scaffolded create fixtures must be valid JSON. The legacy "test-value"
// bare word is rejected by postgres as invalid JSON input (SQLSTATE 22P02)
// at create #1 — the failing generated test this fix addresses.
func TestCRUDTestFixtures_JSONFieldFixtureIsValidJSON(t *testing.T) {
	c := func(name string, typ schemadef.CanonicalType, notNull, pk bool) schemadef.Column {
		return schemadef.Column{Name: name, Type: typ, NotNull: notNull, IsPK: pk}
	}
	// A "documents" entity with a plain NOT NULL jsonb column and a UNIQUE
	// jsonb column, so both the shared-value and distinct-value legs are hit.
	docs := schemadef.Table{
		Name:   "documents",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			c("id", schemadef.TypeString, true, true),
			c("payload", schemadef.TypeJSON, true, false),
			c("fingerprint", schemadef.TypeJSON, true, false),
		},
		Indexes: []schemadef.Index{{Name: "documents_fingerprint_key", Columns: []string{"fingerprint"}, Unique: true}},
	}
	fx := fixtureModel(t, []schemadef.Table{docs}, "documents")
	ent := EntityDef{Name: "Document", TableName: "documents", PkField: "id", PkGoType: "string"}

	// The fixture is a Go string literal; unquote it to the JSON payload and
	// assert postgres would accept it (valid JSON, not a bare word).
	parseLit := func(t *testing.T, lit string) {
		t.Helper()
		s, err := strconv.Unquote(lit)
		if err != nil {
			t.Fatalf("fixture %q is not a Go string literal: %v", lit, err)
		}
		if !json.Valid([]byte(s)) {
			t.Fatalf("fixture value %q is not valid JSON (would raise SQLSTATE 22P02)", s)
		}
	}

	// Plain NOT NULL json column: two DISTINCT valid JSON documents (a
	// UNIQUE index the author adds later must not break the born test).
	v1, v2, ok := fx.fieldFixture(ServiceDef{}, ent, "CreateDocumentRequest", "payload", "string", FieldKindScalar)
	if !ok {
		t.Fatal("expected a JSON fixture for the payload column, got ok=false")
	}
	parseLit(t, v1)
	parseLit(t, v2)
	if v1 == v2 {
		t.Errorf("json fixture repeats the same document on both creates, got %s", v1)
	}

	// UNIQUE json column: the two creates must be DISTINCT valid JSON.
	u1, u2, ok := fx.fieldFixture(ServiceDef{}, ent, "CreateDocumentRequest", "fingerprint", "string", FieldKindScalar)
	if !ok {
		t.Fatal("expected a JSON fixture for the fingerprint column, got ok=false")
	}
	parseLit(t, u1)
	parseLit(t, u2)
	if u1 == u2 {
		t.Errorf("UNIQUE json fixture must differ between create #1/#2, both = %s", u1)
	}
}

// TestCRUDTestFixtures_OrderedColumnsInCreate pins the fixture half of the
// two-column ordering CHECK — the half a seed-plan fix does not reach.
//
// The born lifecycle test mints its OWN rows through the create RPC, and it
// used to leave timestamp fields entirely unset. A NOT NULL pair then took
// Go's zero time on both ends, both landed on 0001-01-01, and the window
// CHECK sitting in the migration beside the test rejected create #1:
//
//	--- FAIL: TestCRUD_Prescription_Lifecycle
//	    create #1: invalid_argument: create prescription: a field value violates a constraint
//
// An integer pair had the same shape with the legacy literal `1` on both
// sides. Both are placed here the way the seeder places them, from the SAME
// ordering resolution.
func TestCRUDTestFixtures_OrderedColumnsInCreate(t *testing.T) {
	c := func(name string, typ schemadef.CanonicalType, notNull, pk bool) schemadef.Column {
		return schemadef.Column{Name: name, Type: typ, NotNull: notNull, IsPK: pk}
	}
	rx := schemadef.Table{
		Name:   "prescriptions",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			c("id", schemadef.TypeString, true, true),
			c("issued_at", schemadef.TypeTime, true, false),
			c("expires_at", schemadef.TypeTime, true, false),
			c("min_days_supply", schemadef.TypeInt, true, false),
			c("max_days_supply", schemadef.TypeInt, true, false),
		},
		Checks: []schemadef.CheckConstraint{
			// pg_get_constraintdef canonical spellings, NULL-guarded — the
			// form an author writes over columns that may be absent, and the
			// form that cost two measured runs.
			{Name: "prescriptions_expires_after_issued",
				Def:     "CHECK (((issued_at IS NULL) OR (expires_at IS NULL) OR (expires_at > issued_at)))",
				Columns: []string{"issued_at", "expires_at"}},
			{Name: "prescriptions_days_supply_range",
				Def:     "CHECK (((min_days_supply IS NULL) OR (max_days_supply IS NULL) OR (max_days_supply > min_days_supply)))",
				Columns: []string{"min_days_supply", "max_days_supply"}},
		},
	}
	fx := fixtureModel(t, []schemadef.Table{rx}, "prescriptions")

	entity := EntityDef{
		Name: "Prescription", TableName: "prescriptions", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "issued_at", GoName: "IssuedAt", ProtoType: "message", GoType: "*timestamppb.Timestamp",
				Kind: FieldKindTimestamp, MessageType: "google.protobuf.Timestamp"},
			{Name: "expires_at", GoName: "ExpiresAt", ProtoType: "message", GoType: "*timestamppb.Timestamp",
				Kind: FieldKindTimestamp, MessageType: "google.protobuf.Timestamp"},
			{Name: "min_days_supply", GoName: "MinDaysSupply", ProtoType: "int32", GoType: "int32", Kind: FieldKindScalar},
			{Name: "max_days_supply", GoName: "MaxDaysSupply", ProtoType: "int32", GoType: "int32", Kind: FieldKindScalar},
		},
	}
	svc := ServiceDef{
		Name: "ClinicService", Package: "clinic.v1", PkgName: "clinicv1", ModulePath: "example.com/test",
		Messages: map[string][]MessageFieldDef{
			"CreatePrescriptionRequest": {
				{Name: "issued_at", ProtoType: "message", MessageType: "google.protobuf.Timestamp"},
				{Name: "expires_at", ProtoType: "message", MessageType: "google.protobuf.Timestamp"},
				{Name: "min_days_supply", ProtoType: "int32"},
				{Name: "max_days_supply", ProtoType: "int32"},
			},
		},
	}
	methods := []CRUDMethod{
		{Method: MethodTemplateData{Name: "CreatePrescription", InputType: "CreatePrescriptionRequest", OutputType: "CreatePrescriptionResponse"}, Entity: entity, Operation: "create"},
		{Method: MethodTemplateData{Name: "GetPrescription", InputType: "GetPrescriptionRequest", OutputType: "GetPrescriptionResponse"}, Entity: entity, Operation: "get"},
	}

	byName, fields := createFieldsByName(svc, methods[0], fx)

	// The lower bound keeps the value it would have had anyway; the upper
	// bound sits one ordering step above it. Both variants carry both fields —
	// an unset field is how both ends collapsed onto one instant.
	for _, want := range []struct{ field, v1, v2 string }{
		{"IssuedAt", "timestamppb.Now()", "timestamppb.New(time.Now().AddDate(0, 0, 1))"},
		{"ExpiresAt",
			"timestamppb.New(time.Now().AddDate(0, 0, 30))",
			"timestamppb.New(time.Now().AddDate(0, 0, 31))"},
		{"MinDaysSupply", "1", "2"},
		{"MaxDaysSupply", "2", "3"},
	} {
		got, ok := byName[want.field]
		if !ok {
			t.Errorf("%s carries no create fixture — an ordered column left unset takes the type's zero value, which is what broke create #1", want.field)
			continue
		}
		if got.TestValue != want.v1 || got.TestValue2 != want.v2 {
			t.Errorf("%s fixture = (%s, %s), want (%s, %s)", want.field, got.TestValue, got.TestValue2, want.v1, want.v2)
		}
	}

	// The imports the ordered literals need are gated on actually emitting
	// them, and the rendered factory must still be valid Go.
	spec := createRequestSpec{funcName: "NewCreatePrescriptionRequest", inputType: "CreatePrescriptionRequest", fields: fields}
	if ts, tm := createSpecsNeed([]createRequestSpec{spec}); !ts || !tm {
		t.Errorf("createSpecsNeed = (timestamppb %t, time %t), want both: an ordered timestamp literal uses both", ts, tm)
	}
	content := renderFactoriesForTest(t, svc, methods[0], fx)
	for _, want := range []string{
		`"time"`,
		`"google.golang.org/protobuf/types/known/timestamppb"`,
		"IssuedAt:      timestamppb.Now(),",
		"ExpiresAt:     timestamppb.New(time.Now().AddDate(0, 0, 30)),",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("rendered factory missing %q\n----\n%s", want, content)
		}
	}
}

// TestCRUDTestFixtures_ParentClosureSatisfiesOrdering pins the OTHER live
// failure of the same defect, and the seam between its two halves: the born
// lifecycle test seeds its entity's FK-parent closure through the ordinary
// seed plan, so a parent carrying an ordering CHECK is placed by the SAME
// detector the dev dataset uses. When it could not read the guard, the
// generated test died before its first RPC:
//
//	--- FAIL: TestCRUD_Order_Lifecycle
//	    seed parent rows: pq: new row for relation "prescriptions" violates
//	    check constraint "prescriptions_expires_after_issued"
func TestCRUDTestFixtures_ParentClosureSatisfiesOrdering(t *testing.T) {
	c := func(name string, typ schemadef.CanonicalType, notNull, pk bool) schemadef.Column {
		return schemadef.Column{Name: name, Type: typ, NotNull: notNull, IsPK: pk}
	}
	rx := schemadef.Table{
		Name: "prescriptions", PKCols: []string{"id"},
		Columns: []schemadef.Column{
			c("id", schemadef.TypeString, true, true),
			// NOT NULL: the parents are seeded from a MINIMAL plan, which
			// leaves a nullable column NULL (vacuously satisfying the CHECK).
			// Required columns are the ones it must PLACE.
			c("issued_at", schemadef.TypeTime, true, false),
			c("expires_at", schemadef.TypeTime, true, false),
		},
		Checks: []schemadef.CheckConstraint{{
			Name:    "prescriptions_expires_after_issued",
			Def:     "CHECK (((issued_at IS NULL) OR (expires_at IS NULL) OR (expires_at > issued_at)))",
			Columns: []string{"issued_at", "expires_at"},
		}},
	}
	orders := schemadef.Table{
		Name: "orders", PKCols: []string{"id"},
		Columns: []schemadef.Column{
			c("id", schemadef.TypeString, true, true),
			c("prescription_id", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{{Column: "prescription_id", RefTable: "prescriptions", RefColumn: "id"}},
	}
	fx := fixtureModel(t, []schemadef.Table{rx, orders}, "orders")
	esp := fx.plans["orders"]
	if esp == nil || esp.plan == nil {
		t.Fatal("expected a parent-closure seed plan for orders")
	}
	if warns := esp.plan.Warnings(); len(warns) > 0 {
		t.Errorf("the parent-closure plan must place the guarded window, got:\n  %s", strings.Join(warns, "\n  "))
	}
	// Every parent row the born test INSERTs has to satisfy the parent's own
	// CHECK. Two rows are seeded; check both.
	for row := 0; row < 2; row++ {
		issued, ok1 := esp.plan.SeedValue("prescriptions", "issued_at", row)
		expires, ok2 := esp.plan.SeedValue("prescriptions", "expires_at", row)
		if !ok1 || !ok2 {
			t.Fatalf("row %d: parent window not seeded", row)
		}
		if !(expires > issued) { // RFC3339 sorts lexicographically
			t.Errorf("row %d: seeded parent has expires_at %s <= issued_at %s — the INSERT the born test runs would be rejected",
				row, expires, issued)
		}
	}
	if !strings.Contains(fx.seedSQLFor("orders"), `INSERT INTO "prescriptions"`) {
		t.Errorf("the parent closure must seed prescriptions; got:\n%s", fx.seedSQLFor("orders"))
	}
}

// TestCRUDTestFixtures_TimestampSetOnlyWhenRequired pins which timestamps a
// create request carries: the ones the schema REQUIRES. A NOT NULL timestamp
// with no DEFAULT must be set — left unset, the create writes the zero time or
// nothing at all — while a nullable one stays unset, so a fresh row does not
// arrive already stamped (a LEAD job with a completion time).
func TestCRUDTestFixtures_TimestampSetOnlyWhenRequired(t *testing.T) {
	notes := schemadef.Table{
		Name:   "notes",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			{Name: "noted_at", Type: schemadef.TypeTime, NotNull: true},
			{Name: "archived_at", Type: schemadef.TypeTime},
			{Name: "seen_at", Type: schemadef.TypeTime, NotNull: true, Default: "now()"},
		},
	}
	fx := fixtureModel(t, []schemadef.Table{notes}, "notes")
	ts := func(name, goName string) EntityField {
		return EntityField{Name: name, GoName: goName, ProtoType: "message", GoType: "*timestamppb.Timestamp",
			Kind: FieldKindTimestamp, MessageType: "google.protobuf.Timestamp"}
	}
	entity := EntityDef{
		Name: "Note", TableName: "notes", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			ts("noted_at", "NotedAt"), ts("archived_at", "ArchivedAt"), ts("seen_at", "SeenAt"),
		},
	}
	svc := ServiceDef{
		Name: "NotesService", Package: "notes.v1", PkgName: "notesv1", ModulePath: "example.com/test",
		Messages: map[string][]MessageFieldDef{
			"CreateNoteRequest": {
				{Name: "noted_at", ProtoType: "message", MessageType: "google.protobuf.Timestamp"},
				{Name: "archived_at", ProtoType: "message", MessageType: "google.protobuf.Timestamp"},
				{Name: "seen_at", ProtoType: "message", MessageType: "google.protobuf.Timestamp"},
			},
		},
	}
	cm := CRUDMethod{Method: MethodTemplateData{Name: "CreateNote", InputType: "CreateNoteRequest", OutputType: "CreateNoteResponse"}, Entity: entity, Operation: "create"}

	byName, _ := createFieldsByName(svc, cm, fx)
	if got, ok := byName["NotedAt"]; !ok || got.TestValue != "timestamppb.Now()" {
		t.Errorf("NOT NULL timestamp with no DEFAULT: got %+v (set=%t), want timestamppb.Now()", got, ok)
	}
	for _, unset := range []string{"ArchivedAt", "SeenAt"} {
		if f, ok := byName[unset]; ok {
			t.Errorf("%s is filled by the database (NULL / its DEFAULT), yet the request sets it to %s", unset, f.TestValue)
		}
	}
}

// TestCRUDTestFixtures_LifecycleConsistentRequest is the reproduction for the
// create-request half of the defect: a status guard the author adds after
// birth. The request must leave the guarded status at its DEFAULT (the row's
// initial state) and must not stamp the lifecycle timestamp — the old
// fixtures put the SECOND enum value on create #2, which the guard rejects.
func TestCRUDTestFixtures_LifecycleConsistentRequest(t *testing.T) {
	tickets := schemadef.Table{
		Name:   "tickets",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.TypeString, NotNull: true, IsPK: true},
			{Name: "title", Type: schemadef.TypeString, NotNull: true, Default: "''::text"},
			{Name: "status", Type: schemadef.TypeString, NotNull: true, Default: "'TICKET_STATUS_OPEN'::text"},
			{Name: "resolved_at", Type: schemadef.TypeTime},
			{Name: "total_cents", Type: schemadef.TypeInt, IsGenerated: true},
		},
		Checks: []schemadef.CheckConstraint{
			{Name: "tickets_status_check", Columns: []string{"status"},
				Def: "CHECK ((status = ANY (ARRAY['TICKET_STATUS_OPEN'::text, 'TICKET_STATUS_RESOLVED'::text])))"},
			{Name: "tickets_resolved_has_stamp", Columns: []string{"status", "resolved_at"},
				Def: "CHECK (((status <> 'TICKET_STATUS_RESOLVED'::text) OR (resolved_at IS NOT NULL)))"},
		},
	}
	fx := fixtureModel(t, []schemadef.Table{tickets}, "tickets")
	entity := EntityDef{
		Name: "Ticket", TableName: "tickets", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "title", GoName: "Title", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "status", GoName: "Status", ProtoType: "enum", GoType: "TicketStatus", Kind: FieldKindEnum, MessageType: "ops.v1.TicketStatus"},
			{Name: "resolved_at", GoName: "ResolvedAt", ProtoType: "message", GoType: "*timestamppb.Timestamp",
				Kind: FieldKindTimestamp, MessageType: "google.protobuf.Timestamp"},
			{Name: "total_cents", GoName: "TotalCents", ProtoType: "int64", GoType: "int64", Kind: FieldKindScalar},
		},
	}
	svc := ServiceDef{
		Name: "OpsService", Package: "ops.v1", PkgName: "opsv1", ModulePath: "example.com/test",
		Messages: map[string][]MessageFieldDef{
			"CreateTicketRequest": {
				{Name: "title", ProtoType: "string"},
				{Name: "status", ProtoType: "enum"},
				{Name: "resolved_at", ProtoType: "message", MessageType: "google.protobuf.Timestamp"},
				{Name: "total_cents", ProtoType: "int64"},
			},
		},
		Schemas: map[string][]SchemaFieldDef{
			"ops.v1.CreateTicketRequest": {{Name: "status", Kind: "enum", TypeName: "ops.v1.TicketStatus"}},
		},
		Enums: map[string][]string{
			"ops.v1.TicketStatus": {"TICKET_STATUS_UNSPECIFIED", "TICKET_STATUS_OPEN", "TICKET_STATUS_RESOLVED"},
		},
	}
	cm := CRUDMethod{Method: MethodTemplateData{Name: "CreateTicket", InputType: "CreateTicketRequest", OutputType: "CreateTicketResponse"}, Entity: entity, Operation: "create"}

	byName, _ := createFieldsByName(svc, cm, fx)
	if f, ok := byName["Status"]; ok {
		t.Errorf("Status is set to (%s, %s); left unset the create stores the column DEFAULT, the initial state the guard allows unstamped",
			f.TestValue, f.TestValue2)
	}
	if f, ok := byName["ResolvedAt"]; ok {
		t.Errorf("ResolvedAt is set to %s on a fresh ticket", f.TestValue)
	}
	if f, ok := byName["TotalCents"]; ok {
		t.Errorf("TotalCents is GENERATED, yet the request sets it to %s", f.TestValue)
	}
	if _, ok := byName["Title"]; !ok {
		t.Error("a plain scalar always writes a value, so the request must carry a valid one")
	}
}

// TestGenerateCRUDTests_SeedsFKParents_PG is the real-schema end-to-end of
// the fixture path: migrations applied to real postgres, introspected, and
// the scaffolded lifecycle test must reference the seeded parent and carry
// CHECK-satisfying literals. Boots embedded postgres; skipped under -short.
func TestGenerateCRUDTests_SeedsFKParents_PG(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "shop")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte("package shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	migDir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	migration := `
CREATE TABLE brands (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE TABLE products (
    id TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    contact_email TEXT NOT NULL CHECK (contact_email ~* '^[^@]+@[^@]+\.[^@]+$'),
    currency TEXT NOT NULL CHECK (char_length(currency) = 3),
    status TEXT NOT NULL CHECK (status IN ('PRODUCT_STATUS_ACTIVE', 'PRODUCT_STATUS_DISCONTINUED')),
    brand_id TEXT NOT NULL REFERENCES brands(id),
    sku TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    price_cents BIGINT NOT NULL CHECK (price_cents >= 100)
);
`
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(migration), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, _ := productSvcAndMethods()
	svc.Methods = []Method{
		{Name: "CreateProduct", InputType: "CreateProductRequest", OutputType: "CreateProductResponse"},
		{Name: "GetProduct", InputType: "GetProductRequest", OutputType: "GetProductResponse"},
		{Name: "UpdateProduct", InputType: "UpdateProductRequest", OutputType: "UpdateProductResponse"},
	}
	_, methods := productSvcAndMethods()

	if _, err := GenerateCRUDTests(svc, methods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDTests: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(handlerDir, "handlers_crud_test.go"))
	if err != nil {
		t.Fatalf("scaffolded handlers_crud_test.go not found: %v", err)
	}
	lifecycle := string(raw)
	if !strings.Contains(lifecycle, "shop.NewCreateProductRequest(t, db, 0)") {
		t.Errorf("the scaffold-once lifecycle test must build its rows from the regenerated factory:\n%s", lifecycle)
	}
	if strings.Contains(lifecycle, "INSERT INTO") {
		t.Errorf("the scaffold-once lifecycle test must carry no literal INSERT fixture:\n%s", lifecycle)
	}

	// The factory is derived from the real applied schema and verified
	// against it.
	fx, err := loadCRUDTestFixtures(projectDir, methods)
	if err != nil || fx == nil {
		t.Fatalf("loadCRUDTestFixtures: fx=%v err=%v", fx, err)
	}
	defer fx.close()
	specs := buildCreateRequestSpecs(context.Background(), svc, methods, fx)
	if len(specs) != 1 {
		t.Fatalf("expected one create-request spec, got %d", len(specs))
	}
	if specs[0].failure != "" {
		t.Fatalf("a schema forge can satisfy produced a failing factory: %s", specs[0].failure)
	}
	out := renderEntityFactoryFile("example.com/test/internal/db", factoryGroup{
		pkg: "shop", pbImport: "example.com/test/gen/proto/services/shop/v1", creates: specs,
	})
	content := string(out)
	for _, want := range []string{
		`INSERT INTO "brands"`,                   // FK parent seeded
		"pb.ProductStatus_PRODUCT_STATUS_ACTIVE", // enum vocabulary satisfied
		"100",                                    // price range CHECK satisfied
	} {
		if !strings.Contains(content, want) {
			t.Errorf("create-request factory missing %q\n----\n%s", want, content)
		}
	}
	// The regex-CHECKed column, asserted against the CHECK the migration above
	// DECLARES rather than against a literal.
	emailCheck := regexp.MustCompile(`^[^@]+@[^@]+\.[^@]+$`)
	assign := regexp.MustCompile(`ContactEmail:\s*"([^"]*)"`)
	fixtures := assign.FindAllStringSubmatch(content, -1)
	if len(fixtures) == 0 {
		t.Fatal("no ContactEmail fixture in the factory — nothing was checked")
	}
	for _, m := range fixtures {
		if !emailCheck.MatchString(m[1]) {
			t.Errorf("ContactEmail fixture %q violates the CHECK the migration declares", m[1])
		}
	}
	if strings.Contains(content, `INSERT INTO "products"`) {
		t.Error("the factory must not seed the entity's own table")
	}
	if _, err := format.Source(out); err != nil {
		t.Errorf("factory is not valid Go: %v\n----\n%s", err, content)
	}
}

// TestCreateRequestFactory_UninvertibleCheckFailsTheTestNotTheGenerate pins
// what happens when the derivation CANNOT satisfy a constraint. The factory is
// regenerated on every run, so refusing to generate would hold every other
// change hostage to a test fixture; emitting a value postgres rejects would
// resurface as an unattributed create #1 failure. Instead the factory's body
// fails the calling test with the violation — column, constraint, remedy —
// and the request is never built.
func TestCreateRequestFactory_UninvertibleCheckFailsTheTestNotTheGenerate(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	projectDir := t.TempDir()
	migDir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A lookahead: postgres accepts it, Go's RE2 cannot compile it, so there
	// is nothing to invert.
	migration := `
CREATE TABLE credentials (
    id TEXT PRIMARY KEY,
    passcode TEXT NOT NULL CHECK (passcode ~ '^(?=.*[A-Z])[a-zA-Z]{8,}$')
);
`
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(migration), 0o644); err != nil {
		t.Fatal(err)
	}
	entity := EntityDef{Name: "Credential", TableName: "credentials", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "passcode", GoName: "Passcode", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
		}}
	svc := ServiceDef{Name: "AccountService", Package: "account.v1", ModulePath: "example.com/test",
		Messages: map[string][]MessageFieldDef{"CreateCredentialRequest": {{Name: "passcode", ProtoType: "string"}}}}
	methods := []CRUDMethod{{Method: MethodTemplateData{Name: "CreateCredential", InputType: "CreateCredentialRequest"}, Entity: entity, Operation: "create"}}

	fx, err := loadCRUDTestFixtures(projectDir, methods)
	if err != nil || fx == nil {
		t.Fatalf("loadCRUDTestFixtures: fx=%v err=%v", fx, err)
	}
	defer fx.close()
	specs := buildCreateRequestSpecs(context.Background(), svc, methods, fx)
	if len(specs) != 1 || specs[0].failure == "" {
		t.Fatalf("an uninvertible CHECK must mark the factory failed; got %+v", specs)
	}
	for _, want := range []string{"credentials.passcode", "credentials_passcode_check", "db/seeds/vocab.yaml"} {
		if !strings.Contains(specs[0].failure, want) {
			t.Errorf("failure does not name %q:\n%s", want, specs[0].failure)
		}
	}
	out := renderEntityFactoryFile("example.com/test/internal/db", factoryGroup{
		pkg: "account", pbImport: "example.com/test/gen/services/account/v1", creates: specs,
	})
	if _, err := format.Source(out); err != nil {
		t.Fatalf("failing factory is not valid Go: %v\n----\n%s", err, out)
	}
	content := string(out)
	if !strings.Contains(content, "t.Fatalf(\"NewCreateCredentialRequest: forge could not derive") {
		t.Errorf("failing factory does not fail the calling test:\n%s", content)
	}
	if strings.Contains(content, "Passcode:") {
		t.Errorf("failing factory still emits the rejected value:\n%s", content)
	}
}

// TestCRUDTestFixtures_SuggestedFKsOrderAndResolveParentRows pins the
// parent-closure seed plan against forge's OWN commented FK suggestions.
//
// A `<x>_id` column is born as a plain TEXT column with its FOREIGN KEY
// written underneath COMMENTED OUT, and forge's charter tells the author to
// uncomment it. So at scaffold time the closure's FKs are UNDECLARED, and
// the only thing that knows `prescriptions.patient_id` points at `patients`
// is fx.foreignKeys()'s suggestion resolver.
//
// buildEntitySeedPlan used that resolver to WALK the closure but handed
// BuildPlan the raw tables, whose ForeignKeys carry only DECLARED keys — so
// BuildPlan saw zero edges and two things broke at once:
//
//   - INSERT order fell back to the alphabetical base order, emitting
//     `prescriptions` (which references products) BEFORE `products`;
//   - a parent row's own FK columns fell through to generic string
//     synthesis, so `prescriptions.patient_id` was `sample_patient_id_1`
//     rather than an id the same statement inserts into `patients`.
//
// Both make the born lifecycle test fail against the schema the author was
// told to write — the exact failure a 2-deep FK chain produces:
//
//	seed parent rows: pq: insert or update on table "prescriptions"
//	violates foreign key constraint "prescriptions_patient_id_fkey"
//
// A 1-deep chain cannot catch this: it needs a parent that is itself a
// child, which is why orders -> prescriptions -> {patients,products} is the
// shape under test.
func TestCRUDTestFixtures_SuggestedFKsOrderAndResolveParentRows(t *testing.T) {
	c := func(name string, typ schemadef.CanonicalType, notNull, pk bool) schemadef.Column {
		return schemadef.Column{Name: name, Type: typ, NotNull: notNull, IsPK: pk}
	}
	tbl := func(name string, cols ...schemadef.Column) schemadef.Table {
		return schemadef.Table{Name: name, PKCols: []string{"id"}, Columns: cols}
	}
	patients := tbl("patients",
		c("id", schemadef.TypeString, true, true),
		c("full_name", schemadef.TypeString, true, false),
	)
	products := tbl("products",
		c("id", schemadef.TypeString, true, true),
		c("name", schemadef.TypeString, true, false),
	)
	// prescriptions is BOTH a parent (of orders) and a child (of patients
	// and products) — and carries NO declared ForeignKeys, exactly as born.
	prescriptions := tbl("prescriptions",
		c("id", schemadef.TypeString, true, true),
		c("patient_id", schemadef.TypeString, true, false),
		c("product_id", schemadef.TypeString, true, false),
	)
	orders := tbl("orders",
		c("id", schemadef.TypeString, true, true),
		c("patient_id", schemadef.TypeString, true, false),
		c("product_id", schemadef.TypeString, true, false),
		c("prescription_id", schemadef.TypeString, true, false),
	)

	fx := fixtureModel(t, []schemadef.Table{patients, products, prescriptions, orders}, "orders")
	sql := fx.seedSQLFor("orders")
	if sql == "" {
		t.Fatal("orders has an FK parent closure; seedSQLFor returned nothing")
	}

	// 1. Topological INSERT order: every parent precedes the table that
	//    references it. Alphabetically `prescriptions` sorts before
	//    `products`, so this fails on the base order alone.
	posProducts := strings.Index(sql, `INSERT INTO "products"`)
	posPrescriptions := strings.Index(sql, `INSERT INTO "prescriptions"`)
	posPatients := strings.Index(sql, `INSERT INTO "patients"`)
	if posProducts < 0 || posPrescriptions < 0 || posPatients < 0 {
		t.Fatalf("closure must seed patients, products and prescriptions:\n%s", sql)
	}
	if posProducts > posPrescriptions {
		t.Errorf("products must be inserted BEFORE prescriptions (it references them); got products@%d prescriptions@%d:\n%s",
			posProducts, posPrescriptions, sql)
	}
	if posPatients > posPrescriptions {
		t.Errorf("patients must be inserted BEFORE prescriptions; got patients@%d prescriptions@%d:\n%s",
			posPatients, posPrescriptions, sql)
	}

	// 2. A parent row's own FK columns carry a REAL seeded parent id, not
	//    the generic `sample_<column>_<n>` placeholder.
	for _, placeholder := range []string{"sample_patient_id", "sample_product_id"} {
		if strings.Contains(sql, placeholder) {
			t.Errorf("parent row carries the %q placeholder instead of a seeded parent id:\n%s", placeholder, sql)
		}
	}

	plan := fx.plans["prescriptions"]
	_ = plan
	esp := fx.plans["orders"]
	if esp == nil || esp.plan == nil {
		t.Fatal("orders seed plan missing")
	}
	patientID, ok := esp.plan.SeedValue("patients", "id", 0)
	if !ok {
		t.Fatal("no seeded patients id")
	}
	if !strings.Contains(sql, patientID) {
		t.Errorf("prescriptions rows must reference the seeded patients id %q:\n%s", patientID, sql)
	}
}

// TestCreateRequestFactory_ParentsInNonPublicSchema_PG pins the control-plane
// regression: every table lives in a named schema (`controlplane.users`), so
// the baked parent INSERT must address it as `"controlplane"."brands"`. A bare
// `"brands"` fails with `relation does not exist`, the factory becomes a
// t.Fatalf, and `forge generate` warns on every run. Boots embedded postgres;
// skipped under -short.
func TestCreateRequestFactory_ParentsInNonPublicSchema_PG(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	projectDir := t.TempDir()
	migDir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	migration := `
CREATE SCHEMA controlplane;
CREATE TABLE controlplane.brands (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE TABLE controlplane.products (
    id TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    contact_email TEXT NOT NULL,
    currency TEXT NOT NULL,
    status TEXT NOT NULL,
    brand_id TEXT NOT NULL REFERENCES controlplane.brands(id),
    sku TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    price_cents BIGINT NOT NULL
);
`
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(migration), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, methods := productSvcAndMethods()

	fx, err := loadCRUDTestFixtures(projectDir, methods)
	if err != nil || fx == nil {
		t.Fatalf("loadCRUDTestFixtures: fx=%v err=%v", fx, err)
	}
	defer fx.close()
	specs := buildCreateRequestSpecs(context.Background(), svc, methods, fx)
	if len(specs) != 1 {
		t.Fatalf("expected one create-request spec, got %d", len(specs))
	}
	if specs[0].failure != "" {
		t.Fatalf("parents in a non-public schema must seed, got: %s", specs[0].failure)
	}
	if !strings.Contains(specs[0].parentSQL, `INSERT INTO "controlplane"."brands"`) {
		t.Errorf("parent INSERT must be schema-qualified:\n%s", specs[0].parentSQL)
	}
}

// TestCreateRequestFactory_UnplaceableParentExplainsWhy_PG pins the message
// for a parent row forge genuinely cannot build: two unions over the same
// columns are refused, the minimal row takes the DEFAULT kind, and postgres
// rejects it. The failure must name the parent table's constraints that forge
// could not place and the two remedies that work, not just echo the SQLSTATE.
// Boots embedded postgres; skipped under -short.
func TestCreateRequestFactory_UnplaceableParentExplainsWhy_PG(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	projectDir := t.TempDir()
	migDir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	migration := `
CREATE TABLE brands (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL DEFAULT 'a' CHECK (kind IN ('a', 'b')),
    tag TEXT,
    note TEXT,
    CONSTRAINT brands_tag_union CHECK ((kind = 'a' AND tag IS NOT NULL) OR (kind = 'b' AND tag IS NULL)),
    CONSTRAINT brands_note_union CHECK ((kind = 'a' AND note IS NULL) OR (kind = 'b' AND note IS NOT NULL))
);
CREATE TABLE unrelated (
    id TEXT PRIMARY KEY,
    lo BIGINT,
    hi BIGINT,
    CONSTRAINT unrelated_pair CHECK ((lo IS NULL) = (hi IS NULL))
);
CREATE TABLE products (
    id TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    contact_email TEXT NOT NULL,
    currency TEXT NOT NULL,
    status TEXT NOT NULL,
    brand_id TEXT NOT NULL REFERENCES brands(id),
    sku TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    price_cents BIGINT NOT NULL
);
`
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(migration), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, methods := productSvcAndMethods()
	fx, err := loadCRUDTestFixtures(projectDir, methods)
	if err != nil || fx == nil {
		t.Fatalf("loadCRUDTestFixtures: fx=%v err=%v", fx, err)
	}
	defer fx.close()
	specs := buildCreateRequestSpecs(context.Background(), svc, methods, fx)
	if len(specs) != 1 {
		t.Fatalf("expected one create-request spec, got %d", len(specs))
	}
	failure := specs[0].failure
	if failure == "" {
		t.Skip("forge placed both unions; this schema no longer exercises the unplaceable path")
	}
	for _, want := range []string{"brands_tag_union", "brands_note_union", "forge project disown", "vocab.yaml"} {
		if !strings.Contains(failure, want) {
			t.Errorf("failure must mention %q:\n%s", want, failure)
		}
	}
	if strings.Contains(failure, "unrelated_pair") {
		t.Errorf("failure must not list constraints of tables that did not fail:\n%s", failure)
	}
}
