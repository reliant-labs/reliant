package orm

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// TestCompareSchemas_NoChanges verifies that identical schemas report no differences
func TestCompareSchemas_NoChanges(t *testing.T) {
	expected := TableSchema{
		Name: "users",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeSerial, PrimaryKey: true, NotNull: true},
			{Name: "email", Type: TypeVarchar, NotNull: true, Unique: true},
			{Name: "name", Type: TypeText, NotNull: false},
			{Name: "age", Type: TypeInteger, NotNull: false},
			{Name: "active", Type: TypeBoolean, NotNull: true, DefaultValue: "true"},
		},
		Indexes: []IndexSchema{
			{Name: "idx_users_email", Fields: []string{"email"}, Unique: true},
			{Name: "idx_users_name", Fields: []string{"name"}, Unique: false},
		},
	}

	// Actual is identical to expected
	actual := TableSchema{
		Name: "users",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeSerial, PrimaryKey: true, NotNull: true},
			{Name: "email", Type: TypeVarchar, NotNull: true, Unique: true},
			{Name: "name", Type: TypeText, NotNull: false},
			{Name: "age", Type: TypeInteger, NotNull: false},
			{Name: "active", Type: TypeBoolean, NotNull: true, DefaultValue: "true"},
		},
		Indexes: []IndexSchema{
			{Name: "idx_users_email", Fields: []string{"email"}, Unique: true},
			{Name: "idx_users_name", Fields: []string{"name"}, Unique: false},
		},
	}

	diff, err := CompareSchemas(expected, actual)
	if err != nil {
		t.Fatalf("CompareSchemas failed: %v", err)
	}

	if diff.HasChanges() {
		t.Errorf("Expected no changes, but got diff: %+v", diff)
	}

	if len(diff.MissingColumns) != 0 {
		t.Errorf("Expected 0 missing columns, got %d", len(diff.MissingColumns))
	}

	if len(diff.ExtraColumns) != 0 {
		t.Errorf("Expected 0 extra columns, got %d", len(diff.ExtraColumns))
	}

	if len(diff.ModifiedColumns) != 0 {
		t.Errorf("Expected 0 modified columns, got %d", len(diff.ModifiedColumns))
	}

	if len(diff.MissingIndexes) != 0 {
		t.Errorf("Expected 0 missing indexes, got %d", len(diff.MissingIndexes))
	}

	if len(diff.ExtraIndexes) != 0 {
		t.Errorf("Expected 0 extra indexes, got %d", len(diff.ExtraIndexes))
	}
}

// TestCompareSchemas_MissingColumns verifies detection of columns in expected but not in actual
func TestCompareSchemas_MissingColumns(t *testing.T) {
	expected := TableSchema{
		Name: "users",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeSerial, PrimaryKey: true, NotNull: true},
			{Name: "email", Type: TypeVarchar, NotNull: true},
			{Name: "name", Type: TypeText, NotNull: false},
			{Name: "created_at", Type: TypeTimestampTZ, NotNull: true, DefaultValue: "NOW()"},
		},
	}

	actual := TableSchema{
		Name: "users",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeSerial, PrimaryKey: true, NotNull: true},
			{Name: "email", Type: TypeVarchar, NotNull: true},
		},
	}

	diff, err := CompareSchemas(expected, actual)
	if err != nil {
		t.Fatalf("CompareSchemas failed: %v", err)
	}

	if !diff.HasChanges() {
		t.Error("Expected changes, but got none")
	}

	if len(diff.MissingColumns) != 2 {
		t.Errorf("Expected 2 missing columns, got %d", len(diff.MissingColumns))
	}

	// Verify the missing columns
	missingNames := make(map[string]bool)
	for _, col := range diff.MissingColumns {
		missingNames[col.Name] = true
	}

	if !missingNames["name"] {
		t.Error("Expected 'name' to be in missing columns")
	}

	if !missingNames["created_at"] {
		t.Error("Expected 'created_at' to be in missing columns")
	}

	// Verify no extra columns or modifications
	if len(diff.ExtraColumns) != 0 {
		t.Errorf("Expected 0 extra columns, got %d", len(diff.ExtraColumns))
	}

	if len(diff.ModifiedColumns) != 0 {
		t.Errorf("Expected 0 modified columns, got %d", len(diff.ModifiedColumns))
	}
}

// TestCompareSchemas_ExtraColumns verifies detection of columns in actual but not in expected
func TestCompareSchemas_ExtraColumns(t *testing.T) {
	expected := TableSchema{
		Name: "users",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeSerial, PrimaryKey: true, NotNull: true},
			{Name: "email", Type: TypeVarchar, NotNull: true},
		},
	}

	actual := TableSchema{
		Name: "users",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeSerial, PrimaryKey: true, NotNull: true},
			{Name: "email", Type: TypeVarchar, NotNull: true},
			{Name: "legacy_field", Type: TypeText, NotNull: false},
			{Name: "deprecated_field", Type: TypeInteger, NotNull: false},
		},
	}

	diff, err := CompareSchemas(expected, actual)
	if err != nil {
		t.Fatalf("CompareSchemas failed: %v", err)
	}

	if !diff.HasChanges() {
		t.Error("Expected changes, but got none")
	}

	if len(diff.ExtraColumns) != 2 {
		t.Errorf("Expected 2 extra columns, got %d", len(diff.ExtraColumns))
	}

	// Verify the extra columns
	extraNames := make(map[string]bool)
	for _, name := range diff.ExtraColumns {
		extraNames[name] = true
	}

	if !extraNames["legacy_field"] {
		t.Error("Expected 'legacy_field' to be in extra columns")
	}

	if !extraNames["deprecated_field"] {
		t.Error("Expected 'deprecated_field' to be in extra columns")
	}

	// Verify IsDestructive returns true
	if !diff.IsDestructive() {
		t.Error("Expected diff to be destructive when there are extra columns")
	}
}

// TestCompareSchemas_ModifiedColumns verifies detection of type and constraint changes
func TestCompareSchemas_ModifiedColumns(t *testing.T) {
	tests := []struct {
		name          string
		expected      FieldSchema
		actual        FieldSchema
		expectChange  bool
		expectError   bool
		errorContains string
	}{
		{
			name:         "Type change: INTEGER to BIGINT (safe)",
			expected:     FieldSchema{Name: "count", Type: TypeBigInt, NotNull: true},
			actual:       FieldSchema{Name: "count", Type: TypeInteger, NotNull: true},
			expectChange: true,
			expectError:  false,
		},
		{
			name:         "Type change: SERIAL to BIGSERIAL (safe)",
			expected:     FieldSchema{Name: "sequence", Type: TypeBigSerial, NotNull: false},
			actual:       FieldSchema{Name: "sequence", Type: TypeSerial, NotNull: false},
			expectChange: true,
			expectError:  false,
		},
		{
			name:         "NOT NULL constraint added",
			expected:     FieldSchema{Name: "email", Type: TypeVarchar, NotNull: true},
			actual:       FieldSchema{Name: "email", Type: TypeVarchar, NotNull: false},
			expectChange: true,
			expectError:  false,
		},
		{
			name:         "NOT NULL constraint removed",
			expected:     FieldSchema{Name: "email", Type: TypeVarchar, NotNull: false},
			actual:       FieldSchema{Name: "email", Type: TypeVarchar, NotNull: true},
			expectChange: true,
			expectError:  false,
		},
		{
			name:         "Default value changed",
			expected:     FieldSchema{Name: "active", Type: TypeBoolean, NotNull: true, DefaultValue: "false"},
			actual:       FieldSchema{Name: "active", Type: TypeBoolean, NotNull: true, DefaultValue: "true"},
			expectChange: true,
			expectError:  false,
		},
		{
			name:         "Default value added",
			expected:     FieldSchema{Name: "status", Type: TypeText, NotNull: true, DefaultValue: "'pending'"},
			actual:       FieldSchema{Name: "status", Type: TypeText, NotNull: true, DefaultValue: ""},
			expectChange: true,
			expectError:  false,
		},
		{
			name:          "Type change: TEXT to INTEGER (incompatible)",
			expected:      FieldSchema{Name: "data", Type: TypeInteger, NotNull: false},
			actual:        FieldSchema{Name: "data", Type: TypeText, NotNull: false},
			expectChange:  false,
			expectError:   true,
			errorContains: "incompatible",
		},
		{
			name:          "Type change: BIGINT to INTEGER (unsafe)",
			expected:      FieldSchema{Name: "count", Type: TypeInteger, NotNull: false},
			actual:        FieldSchema{Name: "count", Type: TypeBigInt, NotNull: false},
			expectChange:  false,
			expectError:   true,
			errorContains: "incompatible",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expected := TableSchema{
				Name:   "test_table",
				Fields: []FieldSchema{tt.expected},
			}

			actual := TableSchema{
				Name:   "test_table",
				Fields: []FieldSchema{tt.actual},
			}

			diff, err := CompareSchemas(expected, actual)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error, but got none")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Expected error containing %q, got: %v", tt.errorContains, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if tt.expectChange {
				if len(diff.ModifiedColumns) != 1 {
					t.Errorf("Expected 1 modified column, got %d", len(diff.ModifiedColumns))
				}
			} else {
				if len(diff.ModifiedColumns) != 0 {
					t.Errorf("Expected 0 modified columns, got %d", len(diff.ModifiedColumns))
				}
			}
		})
	}
}

// TestCompareSchemas_Indexes verifies detection of index differences
func TestCompareSchemas_Indexes(t *testing.T) {
	tests := []struct {
		name                 string
		expectedIndexes      []IndexSchema
		actualIndexes        []IndexSchema
		wantMissingIndexes   int
		wantExtraIndexes     int
		wantMissingIndexName string
		wantExtraIndexName   string
	}{
		{
			name: "Missing index",
			expectedIndexes: []IndexSchema{
				{Name: "idx_users_email", Fields: []string{"email"}, Unique: true},
				{Name: "idx_users_name", Fields: []string{"name"}, Unique: false},
			},
			actualIndexes: []IndexSchema{
				{Name: "idx_users_email", Fields: []string{"email"}, Unique: true},
			},
			wantMissingIndexes:   1,
			wantExtraIndexes:     0,
			wantMissingIndexName: "idx_users_name",
		},
		{
			name: "Extra index",
			expectedIndexes: []IndexSchema{
				{Name: "idx_users_email", Fields: []string{"email"}, Unique: true},
			},
			actualIndexes: []IndexSchema{
				{Name: "idx_users_email", Fields: []string{"email"}, Unique: true},
				{Name: "idx_users_legacy", Fields: []string{"legacy_field"}, Unique: false},
			},
			wantMissingIndexes: 0,
			wantExtraIndexes:   1,
			wantExtraIndexName: "idx_users_legacy",
		},
		{
			name: "Index definition changed (unique flag)",
			expectedIndexes: []IndexSchema{
				{Name: "idx_users_email", Fields: []string{"email"}, Unique: true},
			},
			actualIndexes: []IndexSchema{
				{Name: "idx_users_email", Fields: []string{"email"}, Unique: false},
			},
			wantMissingIndexes:   1,
			wantExtraIndexes:     1,
			wantMissingIndexName: "idx_users_email",
			wantExtraIndexName:   "idx_users_email",
		},
		{
			name: "Index definition changed (columns)",
			expectedIndexes: []IndexSchema{
				{Name: "idx_users_composite", Fields: []string{"email", "name"}, Unique: false},
			},
			actualIndexes: []IndexSchema{
				{Name: "idx_users_composite", Fields: []string{"email"}, Unique: false},
			},
			wantMissingIndexes:   1,
			wantExtraIndexes:     1,
			wantMissingIndexName: "idx_users_composite",
			wantExtraIndexName:   "idx_users_composite",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expected := TableSchema{
				Name:    "users",
				Fields:  []FieldSchema{{Name: "id", Type: TypeSerial}},
				Indexes: tt.expectedIndexes,
			}

			actual := TableSchema{
				Name:    "users",
				Fields:  []FieldSchema{{Name: "id", Type: TypeSerial}},
				Indexes: tt.actualIndexes,
			}

			diff, err := CompareSchemas(expected, actual)
			if err != nil {
				t.Fatalf("CompareSchemas failed: %v", err)
			}

			if len(diff.MissingIndexes) != tt.wantMissingIndexes {
				t.Errorf("Expected %d missing indexes, got %d", tt.wantMissingIndexes, len(diff.MissingIndexes))
			}

			if len(diff.ExtraIndexes) != tt.wantExtraIndexes {
				t.Errorf("Expected %d extra indexes, got %d", tt.wantExtraIndexes, len(diff.ExtraIndexes))
			}

			if tt.wantMissingIndexName != "" {
				found := false
				for _, idx := range diff.MissingIndexes {
					if idx.Name == tt.wantMissingIndexName {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected missing index %q not found", tt.wantMissingIndexName)
				}
			}

			if tt.wantExtraIndexName != "" {
				found := false
				for _, name := range diff.ExtraIndexes {
					if name == tt.wantExtraIndexName {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected extra index %q not found", tt.wantExtraIndexName)
				}
			}
		})
	}
}

// TestGenerateAlterSQL_AddColumn verifies generation of ADD COLUMN statements
func TestGenerateAlterSQL_AddColumn(t *testing.T) {
	tests := []struct {
		name          string
		dialect       Dialect
		missingColumn FieldSchema
		expectedSQL   string
		expectedInSQL []string
	}{
		{
			name:    "PostgreSQL - Add simple column",
			dialect: &PostgresDialect{},
			missingColumn: FieldSchema{
				Name:    "email",
				Type:    TypeVarchar,
				NotNull: false,
			},
			expectedInSQL: []string{
				`ALTER TABLE "users" ADD COLUMN "email" VARCHAR`,
			},
		},
		{
			name:    "PostgreSQL - Add NOT NULL column",
			dialect: &PostgresDialect{},
			missingColumn: FieldSchema{
				Name:    "name",
				Type:    TypeText,
				NotNull: true,
			},
			expectedInSQL: []string{
				`ALTER TABLE "users" ADD COLUMN "name" TEXT NOT NULL`,
			},
		},
		{
			name:    "PostgreSQL - Add column with default",
			dialect: &PostgresDialect{},
			missingColumn: FieldSchema{
				Name:         "active",
				Type:         TypeBoolean,
				NotNull:      true,
				DefaultValue: "true",
			},
			expectedInSQL: []string{
				`ALTER TABLE "users" ADD COLUMN "active" BOOLEAN NOT NULL DEFAULT true`,
			},
		},
		{
			name:    "PostgreSQL - Add unique column",
			dialect: &PostgresDialect{},
			missingColumn: FieldSchema{
				Name:    "username",
				Type:    TypeVarchar,
				NotNull: true,
				Unique:  true,
			},
			expectedInSQL: []string{
				`ALTER TABLE "users" ADD COLUMN "username" VARCHAR NOT NULL UNIQUE`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := SchemaDiff{
				TableName:      "users",
				MissingColumns: []FieldSchema{tt.missingColumn},
			}

			statements, err := GenerateAlterSQL(diff, tt.dialect, false)
			if err != nil {
				t.Fatalf("GenerateAlterSQL failed: %v", err)
			}

			if len(statements) != 1 {
				t.Fatalf("Expected 1 statement, got %d", len(statements))
			}

			sql := statements[0]
			for _, expected := range tt.expectedInSQL {
				if !strings.Contains(sql, expected) {
					t.Errorf("Expected SQL to contain %q, got: %s", expected, sql)
				}
			}
		})
	}
}

// TestGenerateAlterSQL_DropColumn verifies DROP COLUMN requires allowDestructive flag
func TestGenerateAlterSQL_DropColumn(t *testing.T) {
	diff := SchemaDiff{
		TableName:    "users",
		ExtraColumns: []string{"legacy_field", "deprecated_field"},
	}

	dialect := &PostgresDialect{}

	// Test without allowDestructive flag - should fail
	t.Run("Without allowDestructive flag", func(t *testing.T) {
		_, err := GenerateAlterSQL(diff, dialect, false)
		if err == nil {
			t.Error("Expected error when dropping columns without allowDestructive flag")
		}

		if !errors.Is(err, ErrDestructiveOperation) {
			t.Errorf("Expected ErrDestructiveOperation, got: %v", err)
		}

		if !strings.Contains(err.Error(), "legacy_field") || !strings.Contains(err.Error(), "deprecated_field") {
			t.Errorf("Expected error message to mention the columns being dropped, got: %v", err)
		}
	})

	// Test with allowDestructive flag - should succeed
	t.Run("With allowDestructive flag", func(t *testing.T) {
		statements, err := GenerateAlterSQL(diff, dialect, true)
		if err != nil {
			t.Fatalf("GenerateAlterSQL failed: %v", err)
		}

		if len(statements) != 2 {
			t.Fatalf("Expected 2 DROP COLUMN statements, got %d", len(statements))
		}

		// Verify both columns are dropped
		sqlText := strings.Join(statements, " ")
		if !strings.Contains(sqlText, `DROP COLUMN "legacy_field"`) {
			t.Error("Expected DROP COLUMN statement for legacy_field")
		}
		if !strings.Contains(sqlText, `DROP COLUMN "deprecated_field"`) {
			t.Error("Expected DROP COLUMN statement for deprecated_field")
		}
	})
}

// TestGenerateAlterSQL_AlterColumn verifies that ALTER COLUMN operations are rejected
// Following protobuf philosophy, we only support ADD and DROP operations
func TestGenerateAlterSQL_AlterColumn(t *testing.T) {
	tests := []struct {
		name       string
		dialect    Dialect
		columnDiff ColumnDiff
	}{
		{
			name:    "PostgreSQL - Change type",
			dialect: &PostgresDialect{},
			columnDiff: ColumnDiff{
				ColumnName: "count",
				OldType:    "INTEGER",
				NewType:    TypeBigInt,
				OldNotNull: true,
				NewNotNull: true,
			},
		},
		{
			name:    "PostgreSQL - Set NOT NULL",
			dialect: &PostgresDialect{},
			columnDiff: ColumnDiff{
				ColumnName: "email",
				OldType:    "VARCHAR",
				NewType:    TypeVarchar,
				OldNotNull: false,
				NewNotNull: true,
			},
		},
		{
			name:    "PostgreSQL - Drop NOT NULL",
			dialect: &PostgresDialect{},
			columnDiff: ColumnDiff{
				ColumnName: "phone",
				OldType:    "VARCHAR",
				NewType:    TypeVarchar,
				OldNotNull: true,
				NewNotNull: false,
			},
		},
		{
			name:    "PostgreSQL - Set default value",
			dialect: &PostgresDialect{},
			columnDiff: ColumnDiff{
				ColumnName: "active",
				OldType:    "BOOLEAN",
				NewType:    TypeBoolean,
				OldNotNull: true,
				NewNotNull: true,
				OldDefault: sql.NullString{Valid: false},
				NewDefault: "true",
			},
		},
		{
			name:    "PostgreSQL - Drop default value",
			dialect: &PostgresDialect{},
			columnDiff: ColumnDiff{
				ColumnName: "status",
				OldType:    "TEXT",
				NewType:    TypeText,
				OldNotNull: true,
				NewNotNull: true,
				OldDefault: sql.NullString{String: "'pending'", Valid: true},
				NewDefault: "",
			},
		},
		{
			name:    "PostgreSQL - Multiple changes",
			dialect: &PostgresDialect{},
			columnDiff: ColumnDiff{
				ColumnName: "count",
				OldType:    "INTEGER",
				NewType:    TypeBigInt,
				OldNotNull: false,
				NewNotNull: true,
				OldDefault: sql.NullString{Valid: false},
				NewDefault: "0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := SchemaDiff{
				TableName:       "users",
				ModifiedColumns: []ColumnDiff{tt.columnDiff},
			}

			_, err := GenerateAlterSQL(diff, tt.dialect, false)
			if err == nil {
				t.Fatal("Expected error for column modification, but got none")
			}

			if !strings.Contains(err.Error(), "column modifications not supported") {
				t.Errorf("Expected error about column modifications not supported, got: %v", err)
			}
		})
	}
}

// TestGenerateAlterSQL_Indexes verifies generation of CREATE/DROP INDEX statements
func TestGenerateAlterSQL_Indexes(t *testing.T) {
	tests := []struct {
		name             string
		dialect          Dialect
		missingIndexes   []IndexSchema
		extraIndexes     []string
		allowDestructive bool
		expectedStmts    int
		expectedInSQL    []string
		expectError      bool
	}{
		{
			name:    "PostgreSQL - Create simple index",
			dialect: &PostgresDialect{},
			missingIndexes: []IndexSchema{
				{Name: "idx_users_email", Fields: []string{"email"}, Unique: false},
			},
			allowDestructive: false,
			expectedStmts:    1,
			expectedInSQL: []string{
				`CREATE INDEX IF NOT EXISTS "idx_users_email" ON "users" ("email")`,
			},
		},
		{
			name:    "PostgreSQL - Create unique index",
			dialect: &PostgresDialect{},
			missingIndexes: []IndexSchema{
				{Name: "idx_users_username", Fields: []string{"username"}, Unique: true},
			},
			allowDestructive: false,
			expectedStmts:    1,
			expectedInSQL: []string{
				`CREATE UNIQUE INDEX IF NOT EXISTS "idx_users_username" ON "users" ("username")`,
			},
		},
		{
			name:    "PostgreSQL - Create composite index",
			dialect: &PostgresDialect{},
			missingIndexes: []IndexSchema{
				{Name: "idx_users_name_email", Fields: []string{"name", "email"}, Unique: false},
			},
			allowDestructive: false,
			expectedStmts:    1,
			expectedInSQL: []string{
				`CREATE INDEX IF NOT EXISTS "idx_users_name_email" ON "users" ("name", "email")`,
			},
		},
		{
			name:             "PostgreSQL - Drop index without flag",
			dialect:          &PostgresDialect{},
			extraIndexes:     []string{"idx_users_legacy"},
			allowDestructive: false,
			expectError:      true,
		},
		{
			name:             "PostgreSQL - Drop index with flag",
			dialect:          &PostgresDialect{},
			extraIndexes:     []string{"idx_users_legacy"},
			allowDestructive: true,
			expectedStmts:    1,
			expectedInSQL: []string{
				`DROP INDEX IF EXISTS "idx_users_legacy"`,
			},
		},
		{
			name:    "PostgreSQL - Recreate changed index",
			dialect: &PostgresDialect{},
			missingIndexes: []IndexSchema{
				{Name: "idx_users_email", Fields: []string{"email"}, Unique: true},
			},
			extraIndexes:     []string{"idx_users_email"},
			allowDestructive: true,
			expectedStmts:    2,
			expectedInSQL: []string{
				`DROP INDEX IF EXISTS "idx_users_email"`,
				`CREATE UNIQUE INDEX IF NOT EXISTS "idx_users_email" ON "users" ("email")`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := SchemaDiff{
				TableName:      "users",
				MissingIndexes: tt.missingIndexes,
				ExtraIndexes:   tt.extraIndexes,
			}

			statements, err := GenerateAlterSQL(diff, tt.dialect, tt.allowDestructive)
			if tt.expectError {
				if err == nil {
					t.Error("Expected error, but got none")
				}
				if !errors.Is(err, ErrDestructiveOperation) {
					t.Errorf("Expected ErrDestructiveOperation, got: %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("GenerateAlterSQL failed: %v", err)
			}

			if len(statements) != tt.expectedStmts {
				t.Errorf("Expected %d statements, got %d: %v", tt.expectedStmts, len(statements), statements)
			}

			sqlText := strings.Join(statements, " ")
			for _, expected := range tt.expectedInSQL {
				if !strings.Contains(sqlText, expected) {
					t.Errorf("Expected SQL to contain %q, got: %s", expected, sqlText)
				}
			}
		})
	}
}

// TestGenerateAlterSQL_StatementOrder verifies that ALTER statements are generated in the correct order
// Note: With protobuf-style migrations, column modifications are not allowed
func TestGenerateAlterSQL_StatementOrder(t *testing.T) {
	// Test with only ADD and DROP operations (no modifications)
	diff := SchemaDiff{
		TableName:    "users",
		ExtraIndexes: []string{"idx_old"},
		ExtraColumns: []string{"old_column"},
		MissingColumns: []FieldSchema{
			{Name: "new_column", Type: TypeText, NotNull: false},
		},
		MissingIndexes: []IndexSchema{
			{Name: "idx_new", Fields: []string{"new_column"}, Unique: false},
		},
	}

	dialect := &PostgresDialect{}
	statements, err := GenerateAlterSQL(diff, dialect, true)
	if err != nil {
		t.Fatalf("GenerateAlterSQL failed: %v", err)
	}

	// Expected order: DROP INDEX, DROP COLUMN, ADD COLUMN, CREATE INDEX
	expectedOrder := []string{
		"DROP INDEX",   // DROP INDEX first
		"DROP COLUMN",  // DROP COLUMN second
		"ADD COLUMN",   // ADD COLUMN third
		"CREATE INDEX", // CREATE INDEX last
	}

	if len(statements) != len(expectedOrder) {
		t.Fatalf("Expected %d statements, got %d: %v", len(expectedOrder), len(statements), statements)
	}

	for i, expected := range expectedOrder {
		if !strings.Contains(statements[i], expected) {
			t.Errorf("Statement %d: expected to contain %q, got: %s", i, expected, statements[i])
		}
	}
}

// TestDiffDatabase moved to differ_integration_test.go (build tag
// `integration`) so the unit test pass stays hermetic. Run it via
// `task test:integration` or `go test -tags=integration ./...`.

// TestTypesCompatible verifies type compatibility detection
func TestTypesCompatible(t *testing.T) {
	tests := []struct {
		name       string
		type1      FieldType
		type2      FieldType
		compatible bool
	}{
		{"Exact match", TypeInteger, TypeInteger, true},
		{"TEXT and VARCHAR", TypeText, TypeVarchar, true},
		{"VARCHAR and TEXT", TypeVarchar, TypeText, true},
		{"INTEGER and INT4", TypeInteger, FieldType("int4"), true},
		{"BIGINT and INT8", TypeBigInt, FieldType("int8"), true},
		{"BOOLEAN and BOOL", TypeBoolean, FieldType("bool"), true},
		{"TIMESTAMPTZ variants", TypeTimestampTZ, FieldType("timestamp with time zone"), true},
		{"INTEGER and BIGINT", TypeInteger, TypeBigInt, false},
		{"TEXT and INTEGER", TypeText, TypeInteger, false},
		{"VARCHAR and BOOLEAN", TypeVarchar, TypeBoolean, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := typesCompatible(tt.type1, tt.type2)
			if result != tt.compatible {
				t.Errorf("typesCompatible(%q, %q) = %v, want %v",
					tt.type1, tt.type2, result, tt.compatible)
			}

			// Test symmetry
			result2 := typesCompatible(tt.type2, tt.type1)
			if result2 != tt.compatible {
				t.Errorf("typesCompatible is not symmetric: (%q, %q) = %v, but (%q, %q) = %v",
					tt.type1, tt.type2, result, tt.type2, tt.type1, result2)
			}
		})
	}
}

// TestCanConvertType verifies type conversion safety checks
func TestCanConvertType(t *testing.T) {
	tests := []struct {
		name       string
		from       FieldType
		to         FieldType
		canConvert bool
	}{
		{"INTEGER to BIGINT (safe upcast)", TypeInteger, TypeBigInt, true},
		{"VARCHAR to TEXT (safe)", TypeVarchar, TypeText, true},
		{"TEXT to VARCHAR (potentially lossy)", TypeText, TypeVarchar, true},
		{"SERIAL to BIGSERIAL (safe)", TypeSerial, TypeBigSerial, true},
		{"SERIAL to INTEGER (safe)", TypeSerial, TypeInteger, true},
		{"INTEGER to TEXT (safe)", TypeInteger, TypeText, true},
		{"BIGINT to INTEGER (unsafe downcast)", TypeBigInt, TypeInteger, false},
		{"TEXT to INTEGER (incompatible)", TypeText, TypeInteger, false},
		{"BOOLEAN to TEXT (incompatible)", TypeBoolean, TypeText, false},
		{"BIGSERIAL to SERIAL (unsafe downcast)", TypeBigSerial, TypeSerial, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := canConvertType(tt.from, tt.to)
			if result != tt.canConvert {
				t.Errorf("canConvertType(%q, %q) = %v, want %v",
					tt.from, tt.to, result, tt.canConvert)
			}
		})
	}
}

// TestIndexesEqual verifies index comparison logic
func TestIndexesEqual(t *testing.T) {
	tests := []struct {
		name  string
		idx1  IndexSchema
		idx2  IndexSchema
		equal bool
	}{
		{
			name:  "Identical indexes",
			idx1:  IndexSchema{Name: "idx_test", Fields: []string{"field1"}, Unique: true},
			idx2:  IndexSchema{Name: "idx_test", Fields: []string{"field1"}, Unique: true},
			equal: true,
		},
		{
			name:  "Different names",
			idx1:  IndexSchema{Name: "idx_test1", Fields: []string{"field1"}, Unique: true},
			idx2:  IndexSchema{Name: "idx_test2", Fields: []string{"field1"}, Unique: true},
			equal: false,
		},
		{
			name:  "Different unique flags",
			idx1:  IndexSchema{Name: "idx_test", Fields: []string{"field1"}, Unique: true},
			idx2:  IndexSchema{Name: "idx_test", Fields: []string{"field1"}, Unique: false},
			equal: false,
		},
		{
			name:  "Different fields",
			idx1:  IndexSchema{Name: "idx_test", Fields: []string{"field1"}, Unique: true},
			idx2:  IndexSchema{Name: "idx_test", Fields: []string{"field2"}, Unique: true},
			equal: false,
		},
		{
			name:  "Different field count",
			idx1:  IndexSchema{Name: "idx_test", Fields: []string{"field1", "field2"}, Unique: false},
			idx2:  IndexSchema{Name: "idx_test", Fields: []string{"field1"}, Unique: false},
			equal: false,
		},
		{
			name:  "Different field order",
			idx1:  IndexSchema{Name: "idx_test", Fields: []string{"field1", "field2"}, Unique: false},
			idx2:  IndexSchema{Name: "idx_test", Fields: []string{"field2", "field1"}, Unique: false},
			equal: false,
		},
		{
			name:  "Composite index match",
			idx1:  IndexSchema{Name: "idx_composite", Fields: []string{"a", "b", "c"}, Unique: false},
			idx2:  IndexSchema{Name: "idx_composite", Fields: []string{"a", "b", "c"}, Unique: false},
			equal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := indexesEqual(tt.idx1, tt.idx2)
			if result != tt.equal {
				t.Errorf("indexesEqual() = %v, want %v", result, tt.equal)
			}

			// Test symmetry
			result2 := indexesEqual(tt.idx2, tt.idx1)
			if result2 != tt.equal {
				t.Errorf("indexesEqual is not symmetric")
			}
		})
	}
}

// TestSchemaDiff_IsDestructive verifies IsDestructive method
func TestSchemaDiff_IsDestructive(t *testing.T) {
	tests := []struct {
		name        string
		diff        SchemaDiff
		destructive bool
	}{
		{
			name:        "No changes",
			diff:        SchemaDiff{},
			destructive: false,
		},
		{
			name: "Only missing columns",
			diff: SchemaDiff{
				MissingColumns: []FieldSchema{{Name: "new_col", Type: TypeText}},
			},
			destructive: false,
		},
		{
			name: "Only extra columns",
			diff: SchemaDiff{
				ExtraColumns: []string{"old_col"},
			},
			destructive: true,
		},
		{
			name: "Only extra indexes",
			diff: SchemaDiff{
				ExtraIndexes: []string{"old_idx"},
			},
			destructive: true,
		},
		{
			name: "Both extra columns and indexes",
			diff: SchemaDiff{
				ExtraColumns: []string{"old_col"},
				ExtraIndexes: []string{"old_idx"},
			},
			destructive: true,
		},
		{
			name: "Modified columns only",
			diff: SchemaDiff{
				ModifiedColumns: []ColumnDiff{
					{ColumnName: "col", OldType: "INTEGER", NewType: TypeBigInt},
				},
			},
			destructive: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.diff.IsDestructive()
			if result != tt.destructive {
				t.Errorf("IsDestructive() = %v, want %v", result, tt.destructive)
			}
		})
	}
}

// TestSchemaDiff_HasChanges verifies HasChanges method
func TestSchemaDiff_HasChanges(t *testing.T) {
	tests := []struct {
		name       string
		diff       SchemaDiff
		hasChanges bool
	}{
		{
			name:       "Empty diff",
			diff:       SchemaDiff{},
			hasChanges: false,
		},
		{
			name: "Missing columns",
			diff: SchemaDiff{
				MissingColumns: []FieldSchema{{Name: "col", Type: TypeText}},
			},
			hasChanges: true,
		},
		{
			name: "Extra columns",
			diff: SchemaDiff{
				ExtraColumns: []string{"col"},
			},
			hasChanges: true,
		},
		{
			name: "Modified columns",
			diff: SchemaDiff{
				ModifiedColumns: []ColumnDiff{
					{ColumnName: "col", OldType: "INTEGER", NewType: TypeBigInt},
				},
			},
			hasChanges: true,
		},
		{
			name: "Missing indexes",
			diff: SchemaDiff{
				MissingIndexes: []IndexSchema{{Name: "idx", Fields: []string{"col"}}},
			},
			hasChanges: true,
		},
		{
			name: "Extra indexes",
			diff: SchemaDiff{
				ExtraIndexes: []string{"idx"},
			},
			hasChanges: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.diff.HasChanges()
			if result != tt.hasChanges {
				t.Errorf("HasChanges() = %v, want %v", result, tt.hasChanges)
			}
		})
	}
}
