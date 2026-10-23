package orm

import "testing"

// Real-postgres introspection coverage lives in differ_integration_test.go
// (pgtest / embedded postgres). The former in-memory SQLite shadow
// introspection suite was retired with the postgres pin + Bun engine swap
// (the ORM client rejects non-postgres dialects). What remains here is the
// dialect-independent type-mapping unit.

func TestMapDatabaseTypeToFieldType(t *testing.T) {
	tests := []struct {
		dbType      string
		expectType  FieldType
		expectError bool
	}{
		{"text", TypeText, false},
		{"TEXT", TypeText, false},
		{"varchar", TypeVarchar, false},
		{"character varying", TypeVarchar, false},
		{"integer", TypeInteger, false},
		{"int", TypeInteger, false},
		{"int4", TypeInteger, false},
		{"bigint", TypeBigInt, false},
		{"int8", TypeBigInt, false},
		{"boolean", TypeBoolean, false},
		{"bool", TypeBoolean, false},
		{"timestamptz", TypeTimestampTZ, false},
		{"timestamp with time zone", TypeTimestampTZ, false},
		{"jsonb", TypeJSONB, false},
		{"bytea", TypeBytea, false},
		{"serial", TypeSerial, false},
		{"bigserial", TypeBigSerial, false},
		{"varchar(255)", TypeVarchar, false}, // Test with type modifier
		{"", TypeText, false},                // Empty type defaults to TEXT
		{"unknown_type", "", true},           // Unknown type should error
	}

	for _, tt := range tests {
		t.Run(tt.dbType, func(t *testing.T) {
			fieldType, err := mapDatabaseTypeToFieldType(tt.dbType)
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error for type %q, got nil", tt.dbType)
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error for type %q: %v", tt.dbType, err)
			}
			if fieldType != tt.expectType {
				t.Errorf("Type %q: expected %s, got %s", tt.dbType, tt.expectType, fieldType)
			}
		})
	}
}
