package codegen

import (
	"strings"
	"testing"
)

// TestProtoTypeToTSType_BigInt locks the protobuf-es v2 default mapping
// for 64-bit integer scalars to TypeScript `bigint` (not `number`).
func TestProtoTypeToTSType_BigInt(t *testing.T) {
	tests := []struct {
		proto string
		want  string
	}{
		{"int32", "number"},
		{"uint32", "number"},
		{"sint32", "number"},
		{"fixed32", "number"},
		{"sfixed32", "number"},
		{"int64", "bigint"},
		{"uint64", "bigint"},
		{"sint64", "bigint"},
		{"fixed64", "bigint"},
		{"sfixed64", "bigint"},
		{"float", "number"},
		{"double", "number"},
		{"bool", "boolean"},
		{"string", "string"},
		{"enum", "number"},
		{"message", "object"},
	}
	for _, tc := range tests {
		got := protoTypeToTSType(tc.proto)
		if got != tc.want {
			t.Errorf("protoTypeToTSType(%q) = %q, want %q", tc.proto, got, tc.want)
		}
	}
}

// TestMockGenerateValue_BigIntInteger verifies 64-bit ints emit bigint literal.
func TestMockGenerateValue_BigIntInteger(t *testing.T) {
	for _, tc := range []struct{ col, protoType string }{
		{"count", "int64"},
		{"quantity", "uint64"},
		{"amount", "sint64"},
		{"price", "fixed64"},
		{"position", "sfixed64"},
	} {
		f := EntityField{Name: tc.col, ProtoType: tc.protoType}
		got := mockGenerateValue(nil, "things", f, 0, ServiceDef{})
		if !strings.HasSuffix(got, "n") {
			t.Errorf("mockGenerateValue(%s, %s) = %q, want bigint suffix `n`", tc.col, tc.protoType, got)
		}
		if strings.HasPrefix(got, "\"") {
			t.Errorf("mockGenerateValue(%s, %s) = %q, expected bigint literal not string", tc.col, tc.protoType, got)
		}
	}
}

// TestMockGenerateValue_BigIntID verifies bigint-typed primary key emits BigInt("...").
func TestMockGenerateValue_BigIntID(t *testing.T) {
	f := EntityField{Name: "id", ProtoType: "int64"}
	got := mockGenerateValue(nil, "trades", f, 0, ServiceDef{})
	if !strings.HasPrefix(got, "BigInt(") {
		t.Errorf("bigint id: got %q, expected BigInt(...) wrapper", got)
	}
}

// TestMockGenerateValue_StringIDUnchanged guards the UUID-string id path.
func TestMockGenerateValue_StringIDUnchanged(t *testing.T) {
	f := EntityField{Name: "id", ProtoType: "string"}
	got := mockGenerateValue(nil, "trades", f, 0, ServiceDef{})
	if !strings.HasPrefix(got, "\"") {
		t.Errorf("string id: got %q, expected quoted UUID literal", got)
	}
	if strings.Contains(got, "BigInt") {
		t.Errorf("string id: got %q, must not wrap in BigInt", got)
	}
}

// TestMockGenerateValue_BigIntReference verifies a 64-bit-typed reference
// column mocks as a bigint literal, never as a string.
//
// It no longer gets a UUID keyed on a GUESSED parent table: the mock
// generator used to read `<x>_id` and pluralize the stem (`category_id` →
// `categorys`), which named a table that does not exist and produced ids no
// record in the fixture set carries. A reference's value comes from the seed
// plan, which knows the real foreign keys; with no plan there is nothing to
// reference and the column takes an ordinary literal of its own type.
func TestMockGenerateValue_BigIntReference(t *testing.T) {
	f := EntityField{Name: "trader_id", ProtoType: "uint64"}
	got := mockGenerateValue(nil, "orders", f, 3, ServiceDef{})
	if strings.HasPrefix(got, "\"") {
		t.Errorf("bigint reference: got %q, expected a bigint literal not a string", got)
	}
	if !strings.HasSuffix(got, "n") && !strings.HasPrefix(got, "BigInt(") {
		t.Errorf("bigint reference: got %q, expected a bigint literal", got)
	}
}

// TestMockGenerateValue_Int32StillNumber guards 32-bit ints stay plain number literals.
func TestMockGenerateValue_Int32StillNumber(t *testing.T) {
	f := EntityField{Name: "count", ProtoType: "int32"}
	got := mockGenerateValue(nil, "things", f, 0, ServiceDef{})
	if strings.HasSuffix(got, "n") {
		t.Errorf("int32 count: got %q, must not have bigint `n` suffix", got)
	}
}

// TestMockGenerateValue_TimestampByTypeNotName: the mock value for a column
// is chosen from its DECLARED proto type, never from its name.
//
// The regression this pins: the emitter branched on `strings.HasSuffix(col,
// "_at")` with `protoType` sitting unused in the same scope, so a
// `string issued_at` / `int64 expires_at` field got a `timestampFromDate(...)`
// literal. `src/mocks/*.ts` is forge-owned and regenerated every run, so the
// frontend typecheck lane failed on a FRESH scaffold — 40 x TS2322
// "Type 'Timestamp' is not assignable to type 'string'" on a 4-entity project,
// with no file the author is allowed to edit.
//
// `*_at` columns typed as real timestamps still get a Timestamp literal; that
// is now a consequence of their type, which is also how the db skill's
// "legacy TEXT timestamp column" case stays correct.
func TestMockGenerateValue_TimestampByTypeNotName(t *testing.T) {
	for _, tc := range []struct {
		name        string
		f           EntityField
		wantIsStamp bool
	}{
		{"string column named _at", EntityField{Name: "issued_at", ProtoType: "string"}, false},
		{"epoch int64 column named _at", EntityField{Name: "expires_at", ProtoType: "int64"}, false},
		{"real Timestamp message field", EntityField{Name: "created_at", ProtoType: "message", MessageType: "google.protobuf.Timestamp"}, true},
		{"Timestamp spelled in ProtoType", EntityField{Name: "updated_at", ProtoType: "google.protobuf.Timestamp"}, true},
		{"real Timestamp not named _at", EntityField{Name: "valid_from", ProtoType: "message", MessageType: "google.protobuf.Timestamp"}, true},
	} {
		got := mockGenerateValue(nil, "things", tc.f, 0, ServiceDef{})
		isStamp := strings.Contains(got, "timestampFromDate(")
		if isStamp != tc.wantIsStamp {
			t.Errorf("%s: mockGenerateValue(%s, %s/%s) = %q; timestamp literal = %v, want %v",
				tc.name, tc.f.Name, tc.f.ProtoType, tc.f.MessageType, got, isStamp, tc.wantIsStamp)
		}
	}
}
