package database

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatSchemaText(t *testing.T) {
	tables := []Table{
		{
			Name:       "users",
			Schema:     "public",
			PrimaryKey: []string{"id"},
			Columns: []Column{
				{Name: "id", Type: "uuid", Nullable: false, IsPrimary: true},
				{Name: "name", Type: "text", Nullable: false},
				{Name: "email", Type: "character varying", Nullable: false, MaxLength: intPtr(255)},
				{Name: "created_at", Type: "timestamp with time zone", Nullable: false, Default: "NOW()"},
			},
			Indexes: []Index{
				{Name: "idx_users_email", Columns: []string{"email"}, Unique: true},
			},
			ForeignKeys: []ForeignKey{},
		},
	}

	output := FormatSchemaText(tables)

	// Verify key elements are present.
	for _, want := range []string{
		"Table: public.users",
		"Primary Key: id",
		"id",
		"uuid",
		"email",
		"character varying(255)",
		"DEFAULT NOW()",
		"PRIMARY KEY",
		"idx_users_email",
		"UNIQUE",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("FormatSchemaText output missing %q\nGot:\n%s", want, output)
		}
	}
}

func TestFormatSchemaJSON(t *testing.T) {
	tables := []Table{
		{
			Name:       "users",
			Schema:     "public",
			PrimaryKey: []string{"id"},
			Columns: []Column{
				{Name: "id", Type: "uuid", Nullable: false, IsPrimary: true},
			},
		},
	}

	out, err := FormatSchemaJSON(tables)
	if err != nil {
		t.Fatalf("FormatSchemaJSON error: %v", err)
	}

	// Verify it's valid JSON.
	var parsed []Table
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("FormatSchemaJSON produced invalid JSON: %v\nOutput: %s", err, out)
	}

	if len(parsed) != 1 {
		t.Fatalf("expected 1 table, got %d", len(parsed))
	}
	if parsed[0].Name != "users" {
		t.Errorf("expected table name 'users', got %q", parsed[0].Name)
	}
}

func TestColumnTypeDisplay(t *testing.T) {
	tests := []struct {
		name string
		col  Column
		want string
	}{
		{name: "no max length", col: Column{Type: "text"}, want: "text"},
		{name: "with max length", col: Column{Type: "character varying", MaxLength: intPtr(255)}, want: "character varying(255)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := columnTypeDisplay(tt.col)
			if got != tt.want {
				t.Errorf("columnTypeDisplay = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatSchemaTextMultipleTables(t *testing.T) {
	tables := []Table{
		{Name: "accounts", Schema: "public", Columns: []Column{{Name: "id", Type: "uuid"}}},
		{Name: "orders", Schema: "public", Columns: []Column{{Name: "id", Type: "integer"}}},
	}

	output := FormatSchemaText(tables)

	if !strings.Contains(output, "Table: public.accounts") {
		t.Error("missing accounts table")
	}
	if !strings.Contains(output, "Table: public.orders") {
		t.Error("missing orders table")
	}
}

func TestFormatSchemaTextForeignKeys(t *testing.T) {
	tables := []Table{
		{
			Name:   "orders",
			Schema: "public",
			Columns: []Column{
				{Name: "id", Type: "integer"},
				{Name: "user_id", Type: "integer"},
			},
			ForeignKeys: []ForeignKey{
				{Name: "fk_orders_user_id", Column: "user_id", ReferencedTable: "users", ReferencedColumn: "id"},
			},
		},
	}

	output := FormatSchemaText(tables)

	for _, want := range []string{
		"Foreign Keys:",
		"fk_orders_user_id",
		"user_id -> users.id",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("FormatSchemaText missing %q\nGot:\n%s", want, output)
		}
	}
}

func intPtr(v int) *int {
	return &v
}
