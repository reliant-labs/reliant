package codegen

import "testing"

func TestDetermineFieldKind(t *testing.T) {
	tests := []struct {
		name      string
		protoType string
		goType    string
		want      FieldKind
	}{
		// Scalars
		{"string scalar", "string", "string", FieldKindScalar},
		{"int32 scalar", "int32", "int32", FieldKindScalar},
		{"int64 scalar", "int64", "int64", FieldKindScalar},
		{"uint32 scalar", "uint32", "uint32", FieldKindScalar},
		{"uint64 scalar", "uint64", "uint64", FieldKindScalar},
		{"bool scalar", "bool", "bool", FieldKindScalar},
		{"float scalar", "float", "float32", FieldKindScalar},
		{"double scalar", "double", "float64", FieldKindScalar},
		// `bytes` is the one scalar whose Go type is ALREADY a slice, so
		// the "[]" prefix cannot mean `repeated` for it. A singular bytes
		// field is ONE value and pairs with a BYTEA column; the repeated
		// form has an extra slice level and pairs with BYTEA[]. (This case
		// used to assert repeated_scalar and call it "by design" — it was
		// unreachable fiction, because ProtoTypeToGoType answered "string"
		// for bytes and this function was never handed "[]byte".)
		{"bytes type", "bytes", "[]byte", FieldKindScalar},
		{"repeated bytes type", "bytes", "[][]byte", FieldKindRepeatedScalar},

		// Enums
		{"enum field", "enum", "PatientStatus", FieldKindEnum},
		{"enum field custom", "enum", "OrderType", FieldKindEnum},

		// Timestamps
		{"timestamp field", "message", "*timestamppb.Timestamp", FieldKindTimestamp},

		// Wrapper types (nullable scalars)
		{"wrapper string", "message", "*string", FieldKindWrapper},
		{"wrapper int32", "message", "*int32", FieldKindWrapper},
		{"wrapper int64", "message", "*int64", FieldKindWrapper},
		{"wrapper uint32", "message", "*uint32", FieldKindWrapper},
		{"wrapper uint64", "message", "*uint64", FieldKindWrapper},
		{"wrapper bool", "message", "*bool", FieldKindWrapper},
		{"wrapper float32", "message", "*float32", FieldKindWrapper},
		{"wrapper float64", "message", "*float64", FieldKindWrapper},

		// Maps
		{"map string string", "message", "map[string]string", FieldKindMap},
		{"map string int32", "message", "map[string]int32", FieldKindMap},
		{"map int64 string", "message", "map[int64]string", FieldKindMap},

		// Repeated scalars
		{"repeated string", "string", "[]string", FieldKindRepeatedScalar},
		{"repeated int32", "int32", "[]int32", FieldKindRepeatedScalar},
		{"repeated int64", "int64", "[]int64", FieldKindRepeatedScalar},

		// Repeated messages
		{"repeated message", "message", "[]*Address", FieldKindRepeatedMessage},
		{"repeated message patient", "message", "[]*Patient", FieldKindRepeatedMessage},

		// Plain messages (nested, non-wrapper, non-timestamp)
		{"nested message", "message", "*Address", FieldKindMessage},
		{"nested message custom", "message", "*SomeOtherMessage", FieldKindMessage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetermineFieldKind(tt.protoType, tt.goType)
			if got != tt.want {
				t.Errorf("DetermineFieldKind(%q, %q) = %q, want %q", tt.protoType, tt.goType, got, tt.want)
			}
		})
	}
}

func TestIsWrapperGoType(t *testing.T) {
	wrappers := []string{
		"*string", "*int32", "*int64", "*uint32", "*uint64",
		"*bool", "*float32", "*float64",
	}
	for _, w := range wrappers {
		if !isWrapperGoType(w) {
			t.Errorf("isWrapperGoType(%q) = false, want true", w)
		}
	}

	nonWrappers := []string{
		"string", "int32", "*Address", "*timestamppb.Timestamp",
		"[]string", "map[string]string", "*SomeMessage",
	}
	for _, nw := range nonWrappers {
		if isWrapperGoType(nw) {
			t.Errorf("isWrapperGoType(%q) = true, want false", nw)
		}
	}
}

func TestProtoTypeToGoType(t *testing.T) {
	tests := []struct {
		protoType string
		want      string
	}{
		// The mapping is protoc-gen-go's, not forge's to choose: the pb
		// package already declares the field, and a projection that
		// disagrees with it either refuses a legal shape or emits code the
		// compiler rejects.
		{"string", "string"},
		{"bool", "bool"},
		{"bytes", "[]byte"},
		{"float", "float32"},
		{"double", "float64"},
		{"int32", "int32"},
		{"sint32", "int32"},
		{"sfixed32", "int32"},
		{"int64", "int64"},
		{"sint64", "int64"},
		{"sfixed64", "int64"},
		{"uint32", "uint32"},
		{"fixed32", "uint32"},
		{"uint64", "uint64"},
		{"fixed64", "uint64"},
		// Non-scalar kinds have no scalar Go type; callers branch on them
		// before reaching here, and "string" is the inert fallback for a
		// kind that never should have arrived.
		{"message", "string"},
		{"enum", "string"},
	}

	for _, tt := range tests {
		got := ProtoTypeToGoType(tt.protoType)
		if got != tt.want {
			t.Errorf("ProtoTypeToGoType(%q) = %q, want %q", tt.protoType, got, tt.want)
		}
	}
}

func TestEntityField_FieldKindValues(t *testing.T) {
	// Verify all FieldKind constants have distinct values
	kinds := []FieldKind{
		FieldKindScalar,
		FieldKindEnum,
		FieldKindMessage,
		FieldKindMap,
		FieldKindRepeatedScalar,
		FieldKindRepeatedMessage,
		FieldKindWrapper,
		FieldKindTimestamp,
	}

	seen := make(map[FieldKind]bool)
	for _, k := range kinds {
		if seen[k] {
			t.Errorf("duplicate FieldKind value: %q", k)
		}
		seen[k] = true
	}

	if len(seen) != 8 {
		t.Errorf("expected 8 distinct FieldKind values, got %d", len(seen))
	}
}

func TestDetermineFieldKind_ComplexEntityScenario(t *testing.T) {
	// Simulate the field kinds for a Patient entity with complex types
	fields := []struct {
		name      string
		protoType string
		goType    string
		wantKind  FieldKind
	}{
		{"id", "string", "string", FieldKindScalar},
		{"name", "string", "string", FieldKindScalar},
		{"middle_name", "message", "*string", FieldKindWrapper},
		{"email", "string", "string", FieldKindScalar},
		{"status", "enum", "PatientStatus", FieldKindEnum},
		{"address", "message", "*Address", FieldKindMessage},
		{"tags", "string", "[]string", FieldKindRepeatedScalar},
		{"metadata", "message", "map[string]string", FieldKindMap},
		{"org_id", "string", "string", FieldKindScalar},
		{"last_visit", "message", "*timestamppb.Timestamp", FieldKindTimestamp},
		{"age", "message", "*int32", FieldKindWrapper},
	}

	for _, f := range fields {
		t.Run(f.name, func(t *testing.T) {
			got := DetermineFieldKind(f.protoType, f.goType)
			if got != f.wantKind {
				t.Errorf("field %q: DetermineFieldKind(%q, %q) = %q, want %q",
					f.name, f.protoType, f.goType, got, f.wantKind)
			}
		})
	}
}
