package codegen

import (
	"strings"
	"testing"
)

// crud_convert_test.go — the SCHEMA-DEFAULT half of the create path.
//
// A `// forge:read-only` column is by construction absent from the create
// request, as is any column the wire message never had. For a NOT NULL
// column whose DEFAULT is not the Go zero, the row is invalid on arrival —
// or silently wrong — if the op writes the zero: an enum column born
// `NOT NULL DEFAULT 'TONE_WARM' CHECK (tone IN (...))` receives the empty
// string, an `INTEGER NOT NULL DEFAULT 2` receives 0.
//
// The op used to paste the DEFAULT's literal into Go (schemaDefaultAssigns).
// It no longer has to: such a column is a POINTER field on the entity
// (ColumnInsertDefault), the op leaves it nil, and the INSERT omits it so
// the database applies its own DEFAULT — whatever expression it is. These
// tests pin that, per canonical type, against the closed born-scalar
// vocabulary (bornScalarShapes()) shared with scalar_shape_pairing_test.go.

// defaultProjection is what the schema's DEFAULT for one canonical column
// type must project to on the entity struct.
type defaultProjection struct {
	// sqlDefault is the DEFAULT expression as postgres reports it.
	sqlDefault string
	// goLiteral is the Go spelling of sqlDefault (documentation; the op
	// no longer pastes it).
	goLiteral string
	// zeroDefault is a DEFAULT whose value IS the Go zero — Bun writes
	// that anyway, so the op must emit NO assignment for it.
	zeroDefault string
}

// canonicalDefaults maps each canonical column type to a default worth
// projecting. Keyed by canonical type rather than by proto kind because
// that is what pgDefaultGoLiteral branches on: fifteen proto kinds
// collapse onto five canonical column types, and a table keyed by kind
// would asserts the same five arms three times over while claiming
// fifteen.
//
// `bytes` is deliberately absent: BYTEA has no Go literal spelling
// pgDefaultGoLiteral can produce, and its refusal is asserted separately
// (TestSchemaDefaults_UnprojectableDefaultIsAComment).
var canonicalDefaults = map[string]defaultProjection{
	"string":  {sqlDefault: "'DRAFT'", goLiteral: `"DRAFT"`, zeroDefault: "''"},
	"int64":   {sqlDefault: "42", goLiteral: "42", zeroDefault: "0"},
	"float64": {sqlDefault: "2.5", goLiteral: "2.5", zeroDefault: "0"},
	"bool":    {sqlDefault: "true", goLiteral: "true", zeroDefault: "false"},
}

// readOnlyEntity builds an entity whose create request carries NO
// fields — the `// forge:read-only` shape.
func readOnlyEntity(cols ...EntityColumn) (ServiceDef, Method, EntityDef) {
	svc := ServiceDef{
		Package: "orders.v1",
		Schemas: map[string][]SchemaFieldDef{
			"orders.v1.CreateOrderRequest": {},
		},
	}
	m := Method{Name: "CreateOrder", InputType: "CreateOrderRequest", InputTypeFQ: "orders.v1.CreateOrderRequest"}
	return svc, m, EntityDef{Name: "Order", TableName: "orders", Columns: cols}
}

// createAssigns runs the real create-assignment builder and fails on any
// unmapped pairing, so a test that meant to assert on emitted code can
// never quietly assert on an error path instead.
func createAssigns(t *testing.T, cols ...EntityColumn) []string {
	t.Helper()
	svc, m, entity := readOnlyEntity(cols...)
	assigns, unmapped := buildCreateAssigns(svc, m, entity)
	if err := UnmappedFieldsError(unmapped); err != nil {
		t.Fatalf("unexpected unmapped fields: %v", err)
	}
	return assigns
}

// TestSchemaDefaults_EveryScalarColumnDefaultIsLeftToTheDatabase is the
// main pin: for every proto scalar kind in the closed vocabulary, a NOT NULL
// column carrying a non-zero DEFAULT that the create request does not supply
// is a pointer field the create op leaves nil.
func TestSchemaDefaults_EveryScalarColumnDefaultIsLeftToTheDatabase(t *testing.T) {
	shapes := bornScalarShapes()
	if len(shapes) == 0 {
		t.Fatal("the born-scalar vocabulary is empty — this test would assert nothing and report green")
	}
	checked := 0
	for _, s := range shapes {
		want, ok := canonicalDefaults[s.canonical]
		if !ok {
			if s.canonical == "bytes" {
				continue
			}
			t.Fatalf("the scalar vocabulary grew canonical type %q (kind %s) and this test has no "+
				"default for it — add one to canonicalDefaults", s.canonical, s.kind)
		}
		checked++
		col := scalarCol("f_"+s.kind, s, false, true)
		col.Default = want.sqlDefault
		if !dbNullable(col) {
			t.Errorf("%s column (%s DEFAULT %s): must be a pointer field so nil can mean the DEFAULT", s.kind, s.declType, want.sqlDefault)
		}
		if assigns := createAssigns(t, col); len(assigns) != 0 {
			t.Errorf("%s column (%s DEFAULT %s): the create op must leave it unset, got:\n%s",
				s.kind, s.declType, want.sqlDefault, strings.Join(assigns, "\n"))
		}
	}
	if checked == 0 {
		t.Fatal("no scalar kind was actually checked — the projection table and the vocabulary do not intersect")
	}
}

// TestSchemaDefaults_ZeroValuedDefaultIsAPlainField: when the DEFAULT IS
// the Go zero, the struct's zero already says it — no pointer, nothing
// emitted.
func TestSchemaDefaults_ZeroValuedDefaultIsAPlainField(t *testing.T) {
	for _, s := range bornScalarShapes() {
		want, ok := canonicalDefaults[s.canonical]
		if !ok {
			continue
		}
		col := scalarCol("f_"+s.kind, s, false, true)
		col.Default = want.zeroDefault
		if dbNullable(col) {
			t.Errorf("%s column (DEFAULT %s = the Go zero): must stay a plain field", s.kind, want.zeroDefault)
		}
		if assigns := createAssigns(t, col); len(assigns) != 0 {
			t.Errorf("%s column (DEFAULT %s): create op emits %v", s.kind, want.zeroDefault, assigns)
		}
	}
}

// TestSchemaDefaults_ExpressionDefaultIsLeftToTheDatabase: now() /
// gen_random_uuid() / nextval() have no Go spelling, and forge must not
// invent one — the pointer stays nil and the database produces the value.
func TestSchemaDefaults_ExpressionDefaultIsLeftToTheDatabase(t *testing.T) {
	cases := []struct{ name, decl, canonical, sqlDefault string }{
		{"code", "TEXT", "string", "(gen_random_uuid())::text"},
		{"seq", "BIGINT", "int64", "nextval('orders_seq'::regclass)"},
	}
	for _, tc := range cases {
		col := EntityColumn{Name: tc.name, Type: tc.canonical, DeclType: tc.decl, NotNull: true, Default: tc.sqlDefault}
		if !dbNullable(col) {
			t.Errorf("%s DEFAULT %s: must be a pointer field", tc.decl, tc.sqlDefault)
		}
		if assigns := createAssigns(t, col); len(assigns) != 0 {
			t.Errorf("%s DEFAULT %s: forge invented %v — the DB owns this default", tc.decl, tc.sqlDefault, assigns)
		}
	}
}

// TestSchemaDefaults_SkipsColumnsTheDBOrRequestOwns pins the exclusions. A
// primary key, a GENERATED ALWAYS column, a nullable column and a managed
// timestamp are none of the create op's business.
func TestSchemaDefaults_SkipsColumnsTheDBOrRequestOwns(t *testing.T) {
	cases := []struct {
		what string
		col  EntityColumn
	}{
		{"primary key", EntityColumn{Name: "id", Type: "string", DeclType: "TEXT", IsPK: true, NotNull: true, Default: "'x'"}},
		{"generated", EntityColumn{Name: "total", Type: "int64", DeclType: "BIGINT", IsGenerated: true, NotNull: true, Default: "7"}},
		{"nullable", EntityColumn{Name: "note", Type: "string", DeclType: "TEXT", NotNull: false, Default: "'x'"}},
		{"managed timestamp", EntityColumn{Name: "created_at", Type: "time", DeclType: "TIMESTAMPTZ", NotNull: true, Default: "now()"}},
	}
	for _, tc := range cases {
		if assigns := createAssigns(t, tc.col); len(assigns) != 0 {
			t.Errorf("%s column: create op must emit nothing, got:\n%s", tc.what, strings.Join(assigns, "\n"))
		}
	}
}

// TestSchemaDefaults_RequestSuppliedColumnIsWrittenThroughThePointer: a
// column the request carries is written from the request — through the
// pointer — and never replaced by the schema default.
func TestSchemaDefaults_RequestSuppliedColumnIsWrittenThroughThePointer(t *testing.T) {
	svc := ServiceDef{
		Package: "orders.v1",
		Schemas: map[string][]SchemaFieldDef{
			"orders.v1.CreateOrderRequest": {{Name: "status", Kind: "string"}},
		},
	}
	m := Method{Name: "CreateOrder", InputType: "CreateOrderRequest", InputTypeFQ: "orders.v1.CreateOrderRequest"}
	entity := EntityDef{
		Name: "Order", TableName: "orders",
		Columns: []EntityColumn{{Name: "status", Type: "string", DeclType: "TEXT", NotNull: true, Default: "'DRAFT'"}},
	}
	assigns, unmapped := buildCreateAssigns(svc, m, entity)
	if err := UnmappedFieldsError(unmapped); err != nil {
		t.Fatalf("unexpected unmapped fields: %v", err)
	}
	joined := strings.Join(assigns, "\n")
	if !strings.Contains(joined, "v := req.Status") || !strings.Contains(joined, "e.Status = &v") {
		t.Errorf("the request-supplied column must be written through the pointer; got:\n%s", joined)
	}
	if strings.Contains(joined, `"DRAFT"`) {
		t.Errorf("the schema default leaked into the create op:\n%s", joined)
	}
}
