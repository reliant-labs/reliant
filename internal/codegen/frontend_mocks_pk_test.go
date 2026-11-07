package codegen

import (
	"strings"
	"testing"
)

// TestEffectiveMockProtoType_RepeatedScalar pins that a repeated-scalar entity
// field is re-encoded with a `repeated ` prefix so the mock value/type
// generators emit an array literal + `string[]` TS type. EntityField.ProtoType
// holds only the element kind for repeated scalars (the repeated-ness lives in
// Kind/GoType), so without this a `repeated string` field would mock a bare
// scalar and fail `tsc` against the protobuf-es `string[]` field.
func TestEffectiveMockProtoType_RepeatedScalar(t *testing.T) {
	repeated := EntityField{Name: "models", ProtoType: "string", GoType: "[]string", Kind: FieldKindRepeatedScalar}
	if got := effectiveMockProtoType(repeated); got != "repeated string" {
		t.Fatalf("effectiveMockProtoType(repeated string) = %q, want %q", got, "repeated string")
	}
	if ts := protoTypeToTSType(effectiveMockProtoType(repeated)); ts != "string[]" {
		t.Errorf("TS type = %q, want %q", ts, "string[]")
	}
	ef := repeated
	ef.ProtoType = effectiveMockProtoType(repeated)
	if v := mockGenerateValue(nil, "llm_keys", ef, 0, ServiceDef{}); !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		t.Errorf("mock value = %q, want a TS array literal", v)
	}

	// A plain scalar is unchanged.
	scalar := EntityField{Name: "name", ProtoType: "string", GoType: "string", Kind: FieldKindScalar}
	if got := effectiveMockProtoType(scalar); got != "string" {
		t.Errorf("effectiveMockProtoType(scalar) = %q, want %q", got, "string")
	}
}

// TestEffectiveMockProtoType_RepeatedMessage pins that a repeated-MESSAGE
// entity field is re-encoded with a `repeated ` prefix so the mock generators
// emit an ARRAY literal + an `object[]` TS type. Like repeated scalars,
// EntityField.ProtoType carries only the element kind ("message") for repeated
// messages — the repeated-ness lives in Kind (FieldKindRepeatedMessage) /
// GoType ("[]*Foo"). Without this, a `repeated Foo` field mocks a bare `{}`
// object against the protobuf-es `Foo[]` field and fails `tsc`
// ("Type '{}' is not assignable to type 'Foo[]'").
func TestEffectiveMockProtoType_RepeatedMessage(t *testing.T) {
	repeated := EntityField{Name: "items", ProtoType: "message", MessageType: "demo.v1.Item", GoType: "[]*Item", Kind: FieldKindRepeatedMessage}
	if got := effectiveMockProtoType(repeated); got != "repeated message" {
		t.Fatalf("effectiveMockProtoType(repeated message) = %q, want %q", got, "repeated message")
	}
	if ts := protoTypeToTSType(effectiveMockProtoType(repeated)); ts != "object[]" {
		t.Errorf("TS type = %q, want %q", ts, "object[]")
	}
	ef := repeated
	ef.ProtoType = effectiveMockProtoType(repeated)
	v := mockGenerateValue(nil, "orders", ef, 0, ServiceDef{})
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		t.Errorf("repeated-message mock value = %q, want a TS array literal", v)
	}

	// A singular message field stays an empty-object init — `{}` is a valid
	// partial MessageInitShape and type-checks against the message field.
	singular := EntityField{Name: "meta", ProtoType: "message", MessageType: "demo.v1.Meta", GoType: "*Meta", Kind: FieldKindMessage}
	if got := effectiveMockProtoType(singular); got != "message" {
		t.Errorf("effectiveMockProtoType(message) = %q, want %q", got, "message")
	}
	if v := mockGenerateValue(nil, "orders", singular, 0, ServiceDef{}); v != "{}" {
		t.Errorf("singular-message mock value = %q, want %q", v, "{}")
	}

	// Idempotent: an already-prefixed proto type is not double-wrapped.
	pre := EntityField{Name: "items", ProtoType: "repeated message", GoType: "[]*Item", Kind: FieldKindRepeatedMessage}
	if got := effectiveMockProtoType(pre); got != "repeated message" {
		t.Errorf("effectiveMockProtoType(already-repeated) = %q, want no double prefix", got)
	}
}

// TestMockPkFieldCamel_ResolvesAgainstWireFields pins the contract that the
// mutable mock-store key references a field that EXISTS on the entity's wire
// message. The store maps over mock RECORDS (projections of the wire message),
// so keying by a column the proto omits fails `tsc` with "Property 'x' does
// not exist on type 'Entity'".
func TestMockPkFieldCamel_ResolvesAgainstWireFields(t *testing.T) {
	field := func(name string) EntityField { return EntityField{Name: name, ProtoType: "string"} }

	tests := []struct {
		name   string
		entity EntityDef
		want   string
	}{
		{
			name: "db pk is a wire field",
			entity: EntityDef{
				Name: "Daemon", TableName: "daemons", PkField: "id",
				Fields: []EntityField{field("id"), field("name")},
			},
			want: "id",
		},
		{
			name: "db pk absent from wire message → conventional <singular>_id",
			// usage_events: surrogate `id` PK in the table, but the published
			// UsageEvent proto omits `id` and exposes `usage_event_id`.
			entity: EntityDef{
				Name: "UsageEvent", TableName: "usage_events", PkField: "id",
				Fields: []EntityField{field("usage_event_id"), field("provider")},
			},
			want: "usageEventId",
		},
		{
			name: "no pk, no conventional key → first wire field",
			entity: EntityDef{
				Name: "Thing", TableName: "things", PkField: "",
				Fields: []EntityField{field("ref"), field("label")},
			},
			want: "ref",
		},
		{
			name: "empty entity → id fallback",
			entity: EntityDef{
				Name: "Empty", TableName: "empties", PkField: "id",
			},
			want: "id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mockPkFieldCamel(tt.entity); got != tt.want {
				t.Errorf("mockPkFieldCamel(%s) = %q, want %q", tt.entity.Name, got, tt.want)
			}
		})
	}
}
