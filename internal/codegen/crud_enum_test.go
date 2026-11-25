package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildEntityConv_EnumPairing pins the enum half of the proto<->entity
// conversion contract. Entity birth stores enum fields as TEXT columns
// holding the proto enum VALUE NAMES (CHECK (col IN ('ORDER_STATUS_...')));
// the wire side is the int32-kinded pb enum. The conversion pair maps by
// name: String() toward the database, pb.<Enum>_value[name] toward the
// wire (unknown names degrade to 0/UNSPECIFIED via the map zero value).
// Before this pairing existed every enum field fell to the unmapped
// default — dead over the API: rows born UNSPECIFIED forever, full-row
// updates writing "" into the CHECK.
func TestBuildEntityConv_EnumPairing(t *testing.T) {
	svc := ServiceDef{
		Package: "orders.v1",
		Schemas: map[string][]SchemaFieldDef{
			// Deep schema carries the repeated cardinality EntityField
			// collapses for enums.
			"orders.v1.Order": {
				{Name: "status", Kind: "enum", TypeName: "orders.v1.OrderStatus"},
				{Name: "fallback_status", Kind: "enum", TypeName: "orders.v1.OrderStatus", Optional: true},
				{Name: "history", Kind: "enum", TypeName: "orders.v1.OrderStatus", Repeated: true},
			},
		},
	}
	entity := EntityDef{
		Name: "Order",
		Fields: []EntityField{
			{Name: "status", GoName: "Status", ProtoType: "enum", GoType: "OrderStatus", Kind: FieldKindEnum, MessageType: "orders.v1.OrderStatus"},
			{Name: "fallback_status", GoName: "FallbackStatus", ProtoType: "enum", GoType: "OrderStatus", Kind: FieldKindEnum, MessageType: "orders.v1.OrderStatus", Optional: true},
			{Name: "history", GoName: "History", ProtoType: "enum", GoType: "OrderStatus", Kind: FieldKindEnum, MessageType: "orders.v1.OrderStatus"},
			// Nested enum declaration: protoc-gen-go names it Order_Kind.
			{Name: "kind", GoName: "Kind", ProtoType: "enum", GoType: "Kind", Kind: FieldKindEnum, MessageType: "orders.v1.Order.Kind"},
			// Cross-package enum: birth TODO-skips its column, so normally
			// there is none and the field is wire-only. When a column DOES
			// exist (hand migration) the pairing is unmappable — the single
			// pb import cannot name the foreign type — and that is a dead
			// field, so it fails the generate.
			{Name: "ext_status", GoName: "ExtStatus", ProtoType: "enum", GoType: "Status", Kind: FieldKindEnum, MessageType: "shared.v1.Status"},
		},
		Columns: []EntityColumn{
			{Name: "status", Type: "string", NotNull: true, DeclType: "TEXT"},
			{Name: "fallback_status", Type: "string", DeclType: "TEXT"}, // nullable -> *string entity field
			{Name: "history", Type: "string", IsArray: true, NotNull: true, DeclType: "TEXT[]"},
			{Name: "kind", Type: "string", NotNull: true, DeclType: "TEXT"},
			{Name: "ext_status", Type: "string", NotNull: true, DeclType: "TEXT"},
		},
	}

	conv, unmapped := BuildEntityConv(svc, entity)
	toProto := strings.Join(conv.ToProtoAssigns, "\n")
	fromProto := strings.Join(conv.FromProtoAssigns, "\n")

	// Plain enum over NOT NULL TEXT: name-mapped both ways. The read path
	// (toProto) is now comma-ok checked — a known name maps, an unknown
	// NON-EMPTY name is a loud "corrupt enum value" error, never a silent 0.
	if !strings.Contains(fromProto, "e.Status = m.Status.String()") {
		t.Errorf("plain enum fromProto should store the value name; got:\n%s", fromProto)
	}
	for _, want := range []string{
		"pb.OrderStatus_value[e.Status]",
		"m.Status = pb.OrderStatus(v)",
		`corrupt enum value %q for column status`,
	} {
		if !strings.Contains(toProto, want) {
			t.Errorf("plain enum toProto missing %q; got:\n%s", want, toProto)
		}
	}
	// The pre-fix SILENT form (0 on an unknown name) must be gone.
	if strings.Contains(toProto, "pb.OrderStatus(pb.OrderStatus_value[e.Status])") {
		t.Errorf("plain enum toProto must not use the unchecked map lookup (silent UNSPECIFIED); got:\n%s", toProto)
	}

	// Optional enum (pointer wire field) over nullable TEXT: nil-safe both
	// ways; the read path stays comma-ok checked under the nil guard. The
	// write path also treats an explicit proto zero as unset — a nullable
	// column spells unset as NULL, and the CHECK does not admit the
	// sentinel (see crud_enum_unset_test.go).
	if !strings.Contains(fromProto, "if m.FallbackStatus != nil && *m.FallbackStatus != 0 {\n\t\tv := m.FallbackStatus.String()\n\t\te.FallbackStatus = &v\n\t}") {
		t.Errorf("optional enum fromProto should nil-guard and store the name; got:\n%s", fromProto)
	}
	for _, want := range []string{
		"if e.FallbackStatus != nil {",
		"pb.OrderStatus_value[*e.FallbackStatus]",
		"m.FallbackStatus = &ev",
		`corrupt enum value %q for column fallback_status`,
	} {
		if !strings.Contains(toProto, want) {
			t.Errorf("optional enum toProto missing %q; got:\n%s", want, toProto)
		}
	}

	// Repeated enum over TEXT[]: element-wise name mapping; the read path
	// checks each element and errors on an unknown non-empty name.
	if !strings.Contains(fromProto, "for _, v := range m.History {\n\t\te.History = append(e.History, v.String())\n\t}") {
		t.Errorf("repeated enum fromProto should map element-wise to names; got:\n%s", fromProto)
	}
	for _, want := range []string{
		"for _, sv := range e.History {",
		"pb.OrderStatus_value[sv]",
		"m.History = append(m.History, pb.OrderStatus(v))",
		`corrupt enum value %q for column history`,
	} {
		if !strings.Contains(toProto, want) {
			t.Errorf("repeated enum toProto missing %q; got:\n%s", want, toProto)
		}
	}

	// Nested enum: the Go identifier joins the declaration path with '_'.
	for _, want := range []string{
		"pb.Order_Kind_value[e.Kind]",
		"m.Kind = pb.Order_Kind(v)",
		`corrupt enum value %q for column kind`,
	} {
		if !strings.Contains(toProto, want) {
			t.Errorf("nested enum toProto missing %q; got:\n%s", want, toProto)
		}
	}
	if !strings.Contains(fromProto, "e.Kind = m.Kind.String()") {
		t.Errorf("nested enum fromProto should store the value name; got:\n%s", fromProto)
	}

	// Cross-package enum over a real column: unmappable (the single pb
	// import cannot name the foreign type), so it fails the generate
	// instead of emitting an assignment or an explanatory comment.
	requireUnmapped(t, unmapped, "ext_status", "TEXT")
	if strings.Contains(fromProto, "ExtStatus") || strings.Contains(toProto, "ExtStatus") {
		t.Error("cross-package enum must never be emitted at all")
	}
}

// TestBuildEntityConv_EnumShapeMismatches pins the three enum pairings
// forge cannot map. Each one is a DEAD field — the column is real, the wire
// field is real, and no value crosses between them — so each fails the
// generate naming message, field and column. None of them may emit code
// (a range loop over a scalar would not compile) and none of them may ship
// as an explanatory comment, which is what they used to do: green build,
// green tests, silently dead API.
func TestBuildEntityConv_EnumShapeMismatches(t *testing.T) {
	// No deep schema at all AND no enum type name (shallow-map shape):
	// promoteEnum cannot fire — unmapped, never a bare-identifier emit.
	legacy, legacyUnmapped := BuildEntityConv(ServiceDef{Package: "orders.v1"}, EntityDef{
		Name: "Order",
		Fields: []EntityField{
			{Name: "status", GoName: "Status", ProtoType: "enum", GoType: "string", Kind: FieldKindEnum},
		},
		Columns: []EntityColumn{{Name: "status", Type: "string", NotNull: true, DeclType: "TEXT"}},
	})
	joined := strings.Join(append(legacy.ToProtoAssigns, legacy.FromProtoAssigns...), "\n")
	if strings.Contains(joined, "Status") {
		t.Errorf("an unresolvable enum field must emit nothing at all; got:\n%s", joined)
	}
	requireUnmapped(t, legacyUnmapped, "status", "TEXT")

	// Repeated enum (deep schema says Repeated) over a NON-array column:
	// shape mismatch — unmapped, not a for-range over a scalar.
	svc := ServiceDef{
		Package: "orders.v1",
		Schemas: map[string][]SchemaFieldDef{
			"orders.v1.Order": {{Name: "history", Kind: "enum", TypeName: "orders.v1.OrderStatus", Repeated: true}},
		},
	}
	mismatch, mismatchUnmapped := BuildEntityConv(svc, EntityDef{
		Name: "Order",
		Fields: []EntityField{
			{Name: "history", GoName: "History", ProtoType: "enum", GoType: "OrderStatus", Kind: FieldKindEnum, MessageType: "orders.v1.OrderStatus"},
		},
		Columns: []EntityColumn{{Name: "history", Type: "string", NotNull: true, DeclType: "TEXT"}},
	})
	joined = strings.Join(append(mismatch.ToProtoAssigns, mismatch.FromProtoAssigns...), "\n")
	if strings.Contains(joined, "for _, v := range") {
		t.Errorf("repeated enum over a non-array column must not emit a range loop; got:\n%s", joined)
	}
	requireUnmapped(t, mismatchUnmapped, "history", "TEXT")

	// Enum over a column that is neither TEXT (value names) nor an
	// integer (wire numbers) — hand migration drift: unmapped. (An integer
	// column maps: TestBuildEntityConv_IntegerEnumColumns.)
	floatCol, floatColUnmapped := BuildEntityConv(ServiceDef{Package: "orders.v1"}, EntityDef{
		Name: "Order",
		Fields: []EntityField{
			{Name: "status", GoName: "Status", ProtoType: "enum", GoType: "OrderStatus", Kind: FieldKindEnum, MessageType: "orders.v1.OrderStatus"},
		},
		Columns: []EntityColumn{{Name: "status", Type: "float64", NotNull: true, DeclType: "DOUBLE PRECISION"}},
	})
	joined = strings.Join(append(floatCol.ToProtoAssigns, floatCol.FromProtoAssigns...), "\n")
	if strings.Contains(joined, "Status") {
		t.Errorf("an enum over a float column must emit nothing at all; got:\n%s", joined)
	}
	requireUnmapped(t, floatColUnmapped, "status", "DOUBLE PRECISION")
}

// requireUnmapped asserts the pairing was reported as unmappable, naming
// the field and its declared column type — the two facts a user needs to
// decide which side to change.
func requireUnmapped(t *testing.T, got []UnmappedField, field, declType string) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("expected exactly one unmapped field, got %d: %+v", len(got), got)
	}
	if got[0].Field != field || got[0].DeclType != declType {
		t.Errorf("unmapped field = %+v, want field %q over %s", got[0], field, declType)
	}
	if err := UnmappedFieldsError(got); err == nil {
		t.Error("an unmapped field must produce a generate error")
	}
}

// TestBuildCreateAssigns_Enum pins the create path: the create-request's
// enum fields map onto the entity struct by name (the CHECK vocabulary),
// through the same deep-schema resolution the conversion pair uses.
func TestBuildCreateAssigns_Enum(t *testing.T) {
	svc := ServiceDef{
		Package: "orders.v1",
		Schemas: map[string][]SchemaFieldDef{
			"orders.v1.CreateOrderRequest": {
				{Name: "name", Kind: "string"},
				{Name: "status", Kind: "enum", TypeName: "orders.v1.OrderStatus"},
				{Name: "fallback_status", Kind: "enum", TypeName: "orders.v1.OrderStatus", Optional: true},
				{Name: "history", Kind: "enum", TypeName: "orders.v1.OrderStatus", Repeated: true},
			},
		},
	}
	entity := EntityDef{
		Name: "Order",
		Columns: []EntityColumn{
			{Name: "name", Type: "string", NotNull: true, DeclType: "TEXT"},
			{Name: "status", Type: "string", NotNull: true, DeclType: "TEXT"},
			{Name: "fallback_status", Type: "string", DeclType: "TEXT"},
			{Name: "history", Type: "string", IsArray: true, NotNull: true, DeclType: "TEXT[]"},
		},
	}
	m := Method{Name: "CreateOrder", InputType: "CreateOrderRequest", InputTypeFQ: "orders.v1.CreateOrderRequest"}

	createAssigns, unmapped := buildCreateAssigns(svc, m, entity)
	if len(unmapped) != 0 {
		t.Fatalf("every enum request field must map; unmapped: %+v", unmapped)
	}
	assigns := strings.Join(createAssigns, "\n")
	if !strings.Contains(assigns, "e.Status = req.Status.String()") {
		t.Errorf("create should store the plain enum's value name; got:\n%s", assigns)
	}
	if !strings.Contains(assigns, "if req.FallbackStatus != nil && *req.FallbackStatus != 0 {\n\t\tv := req.FallbackStatus.String()\n\t\te.FallbackStatus = &v\n\t}") {
		t.Errorf("create should nil-guard the optional enum; got:\n%s", assigns)
	}
	if !strings.Contains(assigns, "for _, v := range req.History {\n\t\te.History = append(e.History, v.String())\n\t}") {
		t.Errorf("create should map the repeated enum element-wise; got:\n%s", assigns)
	}
}

// TestClassifyFilterField_Enum pins the filter classification half of the
// enum contract: an enum-typed list filter binds its VALUE NAME (the
// column stores names), so it must be marked IsEnum and kept out of the
// scalar FieldType branches — the old "string" fallback bound the raw pb
// enum and hit Postgres with `text = integer`.
func TestClassifyFilterField_Enum(t *testing.T) {
	plain := classifyFilterField(MessageFieldDef{Name: "status", ProtoType: "enum"})
	if !plain.IsEnum {
		t.Error("enum filter field must be classified IsEnum")
	}
	if plain.FieldType != "enum" {
		t.Errorf("enum filter FieldType = %q, want \"enum\" (never a bindable Go scalar)", plain.FieldType)
	}
	if plain.FilterType != "exact" {
		t.Errorf("enum filter FilterType = %q, want exact", plain.FilterType)
	}

	opt := classifyFilterField(MessageFieldDef{Name: "status", ProtoType: "enum", IsOptional: true})
	if !opt.IsEnum || !opt.IsOptional {
		t.Errorf("optional enum filter must keep both flags; got IsEnum=%v IsOptional=%v", opt.IsEnum, opt.IsOptional)
	}

	// Non-enum fields are untouched.
	if s := classifyFilterField(MessageFieldDef{Name: "name", ProtoType: "string"}); s.IsEnum || s.FieldType != "string" {
		t.Errorf("string filter must stay a plain string field; got IsEnum=%v FieldType=%q", s.IsEnum, s.FieldType)
	}
}

// TestGenerateCRUDHandlers_EnumFilterAndConversion renders the real ops
// file for an entity with enum fields and an enum-filtered list, pinning
// BOTH generated surfaces at once:
//
//   - conversions: the enum fields are name-mapped (never the silent
//     "unmapped (wire kind enum" comment of the pre-fix generator);
//   - list filter: enum filters bind .String() — the stored VALUE NAME —
//     with the optional filter nil-guarded and the plain filter always
//     applied (UNSPECIFIED matches its stored name, never skipped).
func TestGenerateCRUDHandlers_EnumFilterAndConversion(t *testing.T) {
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "orders")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	serviceGo := `package orders

import "github.com/reliant-labs/forge/pkg/orm"

type Deps struct {
	DB orm.Context
}

type Service struct {
	deps Deps
}
`
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte(serviceGo), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := ServiceDef{
		Name:       "OrdersService",
		Package:    "orders.v1",
		GoPackage:  "example.com/test/gen/proto/services/orders/v1",
		PkgName:    "ordersv1",
		ModulePath: "example.com/test",
		Methods: []Method{
			{Name: "ListOrders", InputType: "ListOrdersRequest", OutputType: "ListOrdersResponse"},
		},
		Messages: map[string][]MessageFieldDef{
			"ListOrdersRequest": {
				{Name: "page_size", ProtoType: "int32"},
				{Name: "page_token", ProtoType: "string"},
				{Name: "status", ProtoType: "enum", IsOptional: true},
				{Name: "kind", ProtoType: "enum"}, // plain enum filter
			},
		},
		Schemas: map[string][]SchemaFieldDef{
			"orders.v1.Order": {
				{Name: "id", Kind: "string"},
				{Name: "status", Kind: "enum", TypeName: "orders.v1.OrderStatus"},
				{Name: "kind", Kind: "enum", TypeName: "orders.v1.OrderKind"},
			},
		},
	}

	entities := []EntityDef{{
		Name: "Order", TableName: "orders", PkField: "id", PkGoType: "string",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			{Name: "status", GoName: "Status", ProtoType: "enum", GoType: "OrderStatus", Kind: FieldKindEnum, MessageType: "orders.v1.OrderStatus"},
			{Name: "kind", GoName: "Kind", ProtoType: "enum", GoType: "OrderKind", Kind: FieldKindEnum, MessageType: "orders.v1.OrderKind"},
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "string", NotNull: true, IsPK: true, DeclType: "TEXT"},
			{Name: "status", Type: "string", NotNull: true, DeclType: "TEXT"},
			{Name: "kind", Type: "string", NotNull: true, DeclType: "TEXT"},
		},
	}}

	crudMethods := MatchCRUDMethods(svc, entities)
	if _, err := GenerateCRUDHandlers(svc, crudMethods, "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers() error = %v", err)
	}

	opsPath := filepath.Join(handlerDir, "handlers_crud_ops_gen.go")
	data, err := os.ReadFile(opsPath)
	if err != nil {
		t.Fatalf("generated ops file not found: %v", err)
	}
	ops := string(data)

	// Conversions: name-mapped, not silently unmapped.
	if strings.Contains(ops, "unmapped (wire kind enum") {
		t.Errorf("enum entity fields must be mapped, not dropped; got:\n%s", ops)
	}
	if !strings.Contains(ops, "e.Status = m.Status.String()") ||
		!strings.Contains(ops, "pb.OrderStatus_value[e.Status]") ||
		!strings.Contains(ops, "m.Status = pb.OrderStatus(v)") {
		t.Errorf("enum conversions must map by value name in both directions; got:\n%s", ops)
	}
	// The read path must be comma-ok checked (loud on corrupt values), not
	// the pre-fix silent map lookup.
	if !strings.Contains(ops, `corrupt enum value %q for column status`) {
		t.Errorf("enum read path must surface corrupt values loudly; got:\n%s", ops)
	}
	if strings.Contains(ops, "pb.OrderStatus(pb.OrderStatus_value[e.Status])") {
		t.Errorf("enum read path must not use the unchecked (silent UNSPECIFIED) lookup; got:\n%s", ops)
	}

	// Optional enum filter: nil-guarded name bind.
	if !strings.Contains(ops, "if req.Status != nil {") ||
		!strings.Contains(ops, `orm.WhereEq("status", (*req.Status).String())`) {
		t.Errorf("optional enum filter must bind (*req.Status).String() under a nil guard; got:\n%s", ops)
	}
	// Plain enum filter: always applied, name bind (UNSPECIFIED included).
	if !strings.Contains(ops, `orm.WhereEq("kind", req.Kind.String())`) {
		t.Errorf("plain enum filter must always bind req.Kind.String(); got:\n%s", ops)
	}
	if strings.Contains(ops, "if req.Kind != 0 {") || strings.Contains(ops, `if req.Kind != "" {`) {
		t.Errorf("plain enum filter must not fall to a scalar guard (UNSPECIFIED would be silently skipped); got:\n%s", ops)
	}
	// The raw (integer) binds of the pre-fix generator must be gone.
	for _, bad := range []string{`orm.WhereEq("status", *req.Status)`, `orm.WhereEq("kind", req.Kind)`} {
		if strings.Contains(ops, bad) {
			t.Errorf("enum filter must never bind the raw pb enum (%q — Postgres text = integer); got:\n%s", bad, ops)
		}
	}

	if _, perr := parser.ParseFile(token.NewFileSet(), opsPath, ops, parser.SkipObjectResolution); perr != nil {
		t.Errorf("ops file is not valid Go: %v\n----\n%s", perr, ops)
	}
}
