package orm

import (
	"strings"
	"testing"
)

func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "users", `"users"`},
		{"with space", "my table", `"my table"`},
		{"with double quote", `col"name`, `"col""name"`},
		{"empty string", "", `""`},
		{"multiple quotes", `a""b`, `"a""""b"`},
		{"reserved word", "select", `"select"`},
		{"with null byte", "col\x00name", "\"col\x00name\""},
		{"very long string", strings.Repeat("a", 1000), `"` + strings.Repeat("a", 1000) + `"`},
		{"nested double quotes", `""already""quoted""`, `"""""already""""quoted"""""`},
		{"unicode", "таблица", `"таблица"`},
		{"mixed special chars", `tab.col;--`, `"tab.col;--"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteIdent(tt.input)
			if got != tt.expected {
				t.Errorf("quoteIdent(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGenerateCreateTableSQL_SimpleTable(t *testing.T) {
	schema := TableSchema{
		Name: "users",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeText, PrimaryKey: true},
			{Name: "email", Type: TypeVarchar, NotNull: true, Unique: true},
			{Name: "name", Type: TypeText},
		},
	}

	sql := GenerateCreateTableSQL(schema)

	// Should contain CREATE TABLE with quoted name
	if !strings.Contains(sql, `CREATE TABLE IF NOT EXISTS "users"`) {
		t.Errorf("expected CREATE TABLE with quoted name, got:\n%s", sql)
	}
	// Primary key field
	if !strings.Contains(sql, `"id" TEXT PRIMARY KEY`) {
		t.Errorf("expected primary key field, got:\n%s", sql)
	}
	// NOT NULL + UNIQUE
	if !strings.Contains(sql, `"email" VARCHAR NOT NULL UNIQUE`) {
		t.Errorf("expected NOT NULL UNIQUE field, got:\n%s", sql)
	}
	// Regular field
	if !strings.Contains(sql, `"name" TEXT`) {
		t.Errorf("expected regular field, got:\n%s", sql)
	}
}

func TestGenerateCreateTableSQL_WithDefault(t *testing.T) {
	schema := TableSchema{
		Name: "items",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeSerial, PrimaryKey: true},
			{Name: "status", Type: TypeText, NotNull: true, DefaultValue: "'active'"},
			{Name: "count", Type: TypeInteger, DefaultValue: "0"},
		},
	}

	sql := GenerateCreateTableSQL(schema)

	if !strings.Contains(sql, `DEFAULT 'active'`) {
		t.Errorf("expected DEFAULT 'active', got:\n%s", sql)
	}
	if !strings.Contains(sql, `DEFAULT 0`) {
		t.Errorf("expected DEFAULT 0, got:\n%s", sql)
	}
}

func TestGenerateCreateTableSQL_CompositePrimaryKey(t *testing.T) {
	schema := TableSchema{
		Name: "user_roles",
		Fields: []FieldSchema{
			{Name: "user_id", Type: TypeText, PrimaryKey: true},
			{Name: "role_id", Type: TypeText, PrimaryKey: true},
		},
	}

	sql := GenerateCreateTableSQL(schema)

	// Composite PK should appear as a table-level constraint
	if !strings.Contains(sql, `PRIMARY KEY ("user_id", "role_id")`) {
		t.Errorf("expected composite PRIMARY KEY constraint, got:\n%s", sql)
	}
}

func TestGenerateCreateTableSQL_WithIndexes(t *testing.T) {
	schema := TableSchema{
		Name: "users",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeText, PrimaryKey: true},
			{Name: "email", Type: TypeVarchar},
		},
		Indexes: []IndexSchema{
			{Name: "idx_users_email", Fields: []string{"email"}, Unique: true},
			{Name: "idx_users_composite", Fields: []string{"id", "email"}, Unique: false},
		},
	}

	sql := GenerateCreateTableSQL(schema)

	if !strings.Contains(sql, `CREATE UNIQUE INDEX IF NOT EXISTS "idx_users_email" ON "users" ("email")`) {
		t.Errorf("expected unique index, got:\n%s", sql)
	}
	if !strings.Contains(sql, `CREATE INDEX IF NOT EXISTS "idx_users_composite" ON "users" ("id", "email")`) {
		t.Errorf("expected composite index, got:\n%s", sql)
	}
}

func TestGenerateCreateTableSQL_SpecialCharactersInNames(t *testing.T) {
	schema := TableSchema{
		Name: `my"table`,
		Fields: []FieldSchema{
			{Name: `col"1`, Type: TypeText, PrimaryKey: true},
		},
	}

	sql := GenerateCreateTableSQL(schema)

	if !strings.Contains(sql, `"my""table"`) {
		t.Errorf("expected escaped table name, got:\n%s", sql)
	}
	if !strings.Contains(sql, `"col""1"`) {
		t.Errorf("expected escaped column name, got:\n%s", sql)
	}
}

func TestGenerateCreateTableSQL_EmptyFields(t *testing.T) {
	schema := TableSchema{
		Name:   "empty",
		Fields: []FieldSchema{},
	}

	sql := GenerateCreateTableSQL(schema)

	// Should still produce valid SQL structure
	if !strings.Contains(sql, `CREATE TABLE IF NOT EXISTS "empty"`) {
		t.Errorf("expected CREATE TABLE, got:\n%s", sql)
	}
}

func TestGenerateFieldSQL(t *testing.T) {
	tests := []struct {
		name     string
		field    FieldSchema
		expected string
	}{
		{
			name:     "simple text",
			field:    FieldSchema{Name: "name", Type: TypeText},
			expected: `"name" TEXT`,
		},
		{
			name:     "primary key",
			field:    FieldSchema{Name: "id", Type: TypeSerial, PrimaryKey: true},
			expected: `"id" SERIAL PRIMARY KEY`,
		},
		{
			name:     "not null without pk",
			field:    FieldSchema{Name: "email", Type: TypeVarchar, NotNull: true},
			expected: `"email" VARCHAR NOT NULL`,
		},
		{
			name:     "not null with pk (not null suppressed)",
			field:    FieldSchema{Name: "id", Type: TypeInteger, PrimaryKey: true, NotNull: true},
			expected: `"id" INTEGER PRIMARY KEY`,
		},
		{
			name:     "unique",
			field:    FieldSchema{Name: "code", Type: TypeText, Unique: true},
			expected: `"code" TEXT UNIQUE`,
		},
		{
			name:     "with default",
			field:    FieldSchema{Name: "status", Type: TypeText, DefaultValue: "'pending'"},
			expected: `"status" TEXT DEFAULT 'pending'`,
		},
		{
			name:     "all flags",
			field:    FieldSchema{Name: "email", Type: TypeVarchar, NotNull: true, Unique: true, DefaultValue: "''"},
			expected: `"email" VARCHAR NOT NULL UNIQUE DEFAULT ''`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateFieldSQL(tt.field)
			if got != tt.expected {
				t.Errorf("generateFieldSQL() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGenerateIndexSQL(t *testing.T) {
	tests := []struct {
		name      string
		tableName string
		idx       IndexSchema
		expected  string
	}{
		{
			name:      "simple index",
			tableName: "users",
			idx:       IndexSchema{Name: "idx_email", Fields: []string{"email"}, Unique: false},
			expected:  `CREATE INDEX IF NOT EXISTS "idx_email" ON "users" ("email");`,
		},
		{
			name:      "unique index",
			tableName: "users",
			idx:       IndexSchema{Name: "idx_email_unique", Fields: []string{"email"}, Unique: true},
			expected:  `CREATE UNIQUE INDEX IF NOT EXISTS "idx_email_unique" ON "users" ("email");`,
		},
		{
			name:      "composite index",
			tableName: "orders",
			idx:       IndexSchema{Name: "idx_user_date", Fields: []string{"user_id", "created_at"}, Unique: false},
			expected:  `CREATE INDEX IF NOT EXISTS "idx_user_date" ON "orders" ("user_id", "created_at");`,
		},
		{
			name:      "special chars in names",
			tableName: `my"table`,
			idx:       IndexSchema{Name: `idx"1`, Fields: []string{`col"1`}, Unique: false},
			expected:  `CREATE INDEX IF NOT EXISTS "idx""1" ON "my""table" ("col""1");`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateIndexSQL(tt.tableName, tt.idx)
			if got != tt.expected {
				t.Errorf("generateIndexSQL() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestGenerateCreateTableSQL_FloatTypes pins the float/double DDL emission.
// Before TypeReal / TypeDoublePrecision landed, both `float` and `double`
// proto kinds mapped to orm.TypeText, so any DDL introspection or contract
// sync that looked at TableSchema saw a TEXT column for a numeric field.
// Runtime Scan still worked (Scan uses *float64 directly), but everything
// schema-shaped lied. See orm-typetext-for-double in FORGE_BACKLOG.
func TestGenerateCreateTableSQL_FloatTypes(t *testing.T) {
	schema := TableSchema{
		Name: "model_performance",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeBigInt, PrimaryKey: true},
			{Name: "brier_score", Type: TypeDoublePrecision, NotNull: true},
			{Name: "calibration_error", Type: TypeDoublePrecision},
			{Name: "small_metric", Type: TypeReal},
		},
	}

	sql := GenerateCreateTableSQL(schema)

	if !strings.Contains(sql, `"brier_score" DOUBLE PRECISION NOT NULL`) {
		t.Errorf("expected DOUBLE PRECISION NOT NULL column, got:\n%s", sql)
	}
	if !strings.Contains(sql, `"calibration_error" DOUBLE PRECISION`) {
		t.Errorf("expected DOUBLE PRECISION column, got:\n%s", sql)
	}
	if !strings.Contains(sql, `"small_metric" REAL`) {
		t.Errorf("expected REAL column, got:\n%s", sql)
	}
	// And the legacy TypeText sentinel must NOT appear anywhere for these
	// columns — the regression we're guarding against is a silent fall
	// through to TEXT in the proto → orm.TypeX mapping.
	if strings.Contains(sql, `"brier_score" TEXT`) || strings.Contains(sql, `"calibration_error" TEXT`) {
		t.Errorf("float/double columns regressed to TEXT, got:\n%s", sql)
	}
}
