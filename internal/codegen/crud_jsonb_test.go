package codegen

import (
	"strings"
	"testing"
)

// jsonbEntity is the fixture for the json/jsonb pairing: one entity whose
// table carries a JSONB column for every wire shape a JSON document can
// hold. Kept as a function so each test gets a fresh copy.
func jsonbEntity() EntityDef {
	return EntityDef{
		Name:      "Order",
		TableName: "orders",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			// repeated message <-> a JSONB array of objects
			{Name: "line_items", GoName: "LineItems", ProtoType: "message",
				MessageType: "orders.v1.OrderLineItem", GoType: "[]*OrderLineItem", Kind: FieldKindRepeatedMessage},
			// singular message <-> a JSONB object
			{Name: "ship_to", GoName: "ShipTo", ProtoType: "message",
				MessageType: "orders.v1.Address", GoType: "*Address", Kind: FieldKindMessage},
			// repeated scalar <-> a JSONB array of scalars
			{Name: "tags", GoName: "Tags", ProtoType: "string", GoType: "[]string", Kind: FieldKindRepeatedScalar},
			// nullable singular message <-> a NULLABLE JSONB object
			{Name: "gift_note", GoName: "GiftNote", ProtoType: "message",
				MessageType: "orders.v1.GiftNote", GoType: "*GiftNote", Kind: FieldKindMessage},
			// a plain string field over a JSONB column stays a passthrough
			{Name: "raw_payload", GoName: "RawPayload", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "string", NotNull: true, IsPK: true},
			{Name: "line_items", Type: "json", DeclType: "JSONB", NotNull: true, Default: "'[]'::jsonb"},
			{Name: "ship_to", Type: "json", DeclType: "JSONB", NotNull: true},
			{Name: "tags", Type: "json", DeclType: "JSONB", NotNull: true},
			{Name: "gift_note", Type: "json", DeclType: "JSONB"}, // nullable
			{Name: "raw_payload", Type: "json", DeclType: "JSONB", NotNull: true},
		},
	}
}

// TestBuildEntityConv_JSONBPairing pins the json/jsonb half of the
// proto<->entity conversion pair.
//
// A jsonb column projects onto a Go `string` on the entity struct, so the
// pairing for a structured wire field is "which JSON text goes in the
// string". Before this pairing existed the generator emitted a COMMENT —
// `// LineItems: unmapped (wire kind repeated_message, column JSONB)` —
// and the field was dead over the whole API: Create accepted line items
// and stored the column DEFAULT, List never returned any, and nothing
// failed.
//
// The set under test is DERIVED from the fixture's jsonb columns rather
// than named field by field, and an empty derived set fails the test:
// a pairing assertion that silently covers nothing is the defect it is
// meant to catch.
func TestBuildEntityConv_JSONBPairing(t *testing.T) {
	entity := jsonbEntity()

	// Derive the set: every wire field whose column is json/jsonb. This is
	// the property the schema stamps, not a hardcoded field list.
	wireByName := map[string]EntityField{}
	for _, f := range entity.Fields {
		wireByName[f.Name] = f
	}
	var jsonbFields []EntityField
	for _, c := range entity.Columns {
		if c.Type != "json" {
			continue
		}
		if f, ok := wireByName[c.Name]; ok {
			jsonbFields = append(jsonbFields, f)
		}
	}
	if len(jsonbFields) == 0 {
		t.Fatal("derived set is empty: the fixture has no wire field over a json column, so this test asserts nothing")
	}

	conv, unmapped := BuildEntityConv(ServiceDef{Package: "orders.v1"}, entity)
	if len(unmapped) != 0 {
		t.Fatalf("every json column in the fixture must map; unmapped: %+v", unmapped)
	}

	toProto := strings.Join(conv.ToProtoAssigns, "\n")
	fromProto := strings.Join(conv.FromProtoAssigns, "\n")

	// Every derived field must be ASSIGNED in both directions — no field
	// may survive as a comment.
	for _, f := range jsonbFields {
		for _, c := range []struct {
			dir, body string
		}{{"toProto", toProto}, {"fromProto", fromProto}} {
			if !strings.Contains(c.body, f.GoName) {
				t.Errorf("%s: %s never appears", c.dir, f.GoName)
				continue
			}
			for _, line := range strings.Split(c.body, "\n") {
				if strings.Contains(line, f.GoName) && strings.HasPrefix(strings.TrimSpace(line), "//") {
					t.Errorf("%s: %s survives as a comment, not an assignment: %q", c.dir, f.GoName, line)
				}
			}
		}
	}

	// repeated message: protojson, both directions, through pkg/orm.
	if !strings.Contains(fromProto, "orm.MarshalJSONBList(m.LineItems)") {
		t.Errorf("repeated message should marshal through orm.MarshalJSONBList; got:\n%s", fromProto)
	}
	if !strings.Contains(fromProto, "e.LineItems = []byte(lineItemsJSON)") {
		t.Errorf("repeated message should assign the marshalled text; got:\n%s", fromProto)
	}
	if !strings.Contains(toProto, "orm.UnmarshalJSONBList(string(e.LineItems), &m.LineItems)") {
		t.Errorf("repeated message should unmarshal through orm.UnmarshalJSONBList; got:\n%s", toProto)
	}

	// singular message over a NOT NULL column.
	if !strings.Contains(fromProto, "orm.MarshalJSONBMessage(m.ShipTo)") {
		t.Errorf("singular message should marshal through orm.MarshalJSONBMessage; got:\n%s", fromProto)
	}
	if !strings.Contains(toProto, "orm.UnmarshalJSONBMessage(string(e.ShipTo), &m.ShipTo)") {
		t.Errorf("singular message should unmarshal through orm.UnmarshalJSONBMessage; got:\n%s", toProto)
	}

	// repeated scalar over a jsonb column (as opposed to a TEXT[] column).
	if !strings.Contains(fromProto, "orm.MarshalJSONBScalars(m.Tags)") {
		t.Errorf("repeated scalar should marshal through orm.MarshalJSONBScalars; got:\n%s", fromProto)
	}
	if !strings.Contains(toProto, "orm.UnmarshalJSONBScalars(string(e.Tags), &m.Tags)") {
		t.Errorf("repeated scalar should unmarshal through orm.UnmarshalJSONBScalars; got:\n%s", toProto)
	}

	// NULLABLE jsonb column: the struct field is json.RawMessage, nil for
	// SQL NULL, so an absent message is NULL rather than a fabricated empty
	// document.
	if !strings.Contains(fromProto, "if m.GiftNote != nil {") {
		t.Errorf("nullable json column should write NULL for an absent message; got:\n%s", fromProto)
	}
	if !strings.Contains(toProto, "if e.GiftNote != nil {") ||
		!strings.Contains(toProto, "orm.UnmarshalJSONBMessage(string(e.GiftNote), &m.GiftNote)") {
		t.Errorf("nullable json column should deref before unmarshalling; got:\n%s", toProto)
	}

	// A plain `string` wire field over a jsonb column is the document
	// itself — passthrough, no encoder in either direction, converted
	// between the wire's text and the entity's json.RawMessage bytes.
	if !strings.Contains(toProto, "m.RawPayload = string(e.RawPayload)") ||
		!strings.Contains(fromProto, "e.RawPayload = []byte(m.RawPayload)") {
		t.Errorf("a string field over a json column stays a passthrough; got:\n%s\n%s", toProto, fromProto)
	}

	// The conversions reach pkg/orm and fmt, so the ops file must import both.
	if !ConvNeedsORM([]EntityConvTemplateData{conv}) {
		t.Error("ConvNeedsORM should be true when a conversion calls into pkg/orm")
	}
	if !ConvNeedsFmt([]EntityConvTemplateData{conv}) {
		t.Error("ConvNeedsFmt should be true when a conversion wraps an error")
	}
}

// TestBuildEntityConv_UnmappableFieldIsLoud pins the guard that replaced
// the explanatory comment.
//
// A field forge cannot map onto its column is DEAD over the API — created
// as a default forever, never read back. Emitting a comment that says so
// is a guard that cannot fire: it ships green, and the only way to notice
// is to read generated code nobody reads. The pairing must fail the
// generate instead, naming message, field and column.
func TestBuildEntityConv_UnmappableFieldIsLoud(t *testing.T) {
	entity := EntityDef{
		Name:      "Order",
		TableName: "orders",
		Fields: []EntityField{
			{Name: "id", GoName: "Id", ProtoType: "string", GoType: "string", Kind: FieldKindScalar},
			// a proto map has no jsonb pairing forge can round-trip
			{Name: "labels", GoName: "Labels", ProtoType: "message", GoType: "map[string]string", Kind: FieldKindMap},
		},
		Columns: []EntityColumn{
			{Name: "id", Type: "string", NotNull: true, IsPK: true},
			{Name: "labels", Type: "json", DeclType: "JSONB", NotNull: true},
		},
	}

	conv, unmapped := BuildEntityConv(ServiceDef{Package: "orders.v1"}, entity)
	if len(unmapped) != 1 {
		t.Fatalf("expected exactly one unmapped field, got %d: %+v", len(unmapped), unmapped)
	}
	u := unmapped[0]
	if u.Message != "Order" || u.Field != "labels" || u.Column != "labels" || u.Table != "orders" {
		t.Errorf("unmapped field must name message/field/table/column; got %+v", u)
	}
	if u.Reason == "" {
		t.Error("unmapped field must carry a reason")
	}

	// The dead field must NOT survive as a comment in the generated body.
	body := strings.Join(append(conv.ToProtoAssigns, conv.FromProtoAssigns...), "\n")
	if strings.Contains(body, "Labels") {
		t.Errorf("an unmappable field must not be emitted at all; got:\n%s", body)
	}

	// The aggregated generate error names every part a user needs to act.
	err := UnmappedFieldsError(unmapped)
	if err == nil {
		t.Fatal("UnmappedFieldsError must return an error for a non-empty set")
	}
	for _, want := range []string{"Order", "labels", "orders", "JSONB"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("generate error must mention %q; got: %s", want, err)
		}
	}
	if UnmappedFieldsError(nil) != nil {
		t.Error("UnmappedFieldsError must be nil for an empty set")
	}
}

// TestBuildCreateAssigns_JSONBReachesTheColumn pins the create path
// specifically. The measured failure was asymmetric: Create ACCEPTED line
// items on the request and wrote the column DEFAULT, so the data loss
// happened at the only point where the caller could still see it.
func TestBuildCreateAssigns_JSONBReachesTheColumn(t *testing.T) {
	entity := jsonbEntity()
	svc := ServiceDef{
		Package: "orders.v1",
		Schemas: map[string][]SchemaFieldDef{
			"orders.v1.CreateOrderRequest": {
				{Name: "line_items", Kind: "message", TypeName: "orders.v1.OrderLineItem", Repeated: true},
			},
		},
	}
	m := Method{Name: "CreateOrder", InputType: "CreateOrderRequest", InputTypeFQ: "orders.v1.CreateOrderRequest"}

	assigns, unmapped := buildCreateAssigns(svc, m, entity)
	if len(unmapped) != 0 {
		t.Fatalf("create request field must map; unmapped: %+v", unmapped)
	}
	body := strings.Join(assigns, "\n")
	if !strings.Contains(body, "orm.MarshalJSONBList(req.LineItems)") {
		t.Errorf("create must marshal the request's repeated message onto the column; got:\n%s", body)
	}
	// The column DEFAULT must NOT be stamped over a value the request carried.
	if strings.Contains(body, `e.LineItems = "[]"`) {
		t.Errorf("create must not overwrite a supplied value with the column DEFAULT; got:\n%s", body)
	}
}
