package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ColumnDiff represents a difference in a column definition
type ColumnDiff struct {
	ColumnName string
	OldType    string
	NewType    FieldType
	OldNotNull bool
	NewNotNull bool
	OldDefault sql.NullString
	NewDefault string
}

// SchemaDiff represents all differences between expected and actual schema for a table
type SchemaDiff struct {
	TableName       string
	MissingColumns  []FieldSchema // Columns in proto but not in DB
	ExtraColumns    []string      // Columns in DB but not in proto
	ModifiedColumns []ColumnDiff  // Columns with type/constraint changes
	MissingIndexes  []IndexSchema // Indexes in proto but not in DB
	ExtraIndexes    []string      // Indexes in DB but not in proto
}

// Diff errors
var (
	// ErrDestructiveOperation is returned when attempting a destructive operation without permission
	ErrDestructiveOperation = errors.New("orm: destructive operation not allowed")

	// ErrUnsupportedOperation is returned when a dialect doesn't support an operation
	ErrUnsupportedOperation = errors.New("orm: operation not supported by dialect")

	// ErrIncompatibleTypes is returned when column types cannot be safely converted
	ErrIncompatibleTypes = errors.New("orm: incompatible column types")
)

// HasChanges returns true if there are any differences between schemas
func (d *SchemaDiff) HasChanges() bool {
	return len(d.MissingColumns) > 0 ||
		len(d.ExtraColumns) > 0 ||
		len(d.ModifiedColumns) > 0 ||
		len(d.MissingIndexes) > 0 ||
		len(d.ExtraIndexes) > 0
}

// IsDestructive returns true if the diff contains destructive changes
func (d *SchemaDiff) IsDestructive() bool {
	return len(d.ExtraColumns) > 0 || len(d.ExtraIndexes) > 0
}

// CompareSchemas compares expected schema against actual schema and returns differences
func CompareSchemas(expected, actual TableSchema) (SchemaDiff, error) {
	diff := SchemaDiff{
		TableName:       expected.Name,
		MissingColumns:  make([]FieldSchema, 0),
		ExtraColumns:    make([]string, 0),
		ModifiedColumns: make([]ColumnDiff, 0),
		MissingIndexes:  make([]IndexSchema, 0),
		ExtraIndexes:    make([]string, 0),
	}

	// Build maps for efficient lookup
	expectedFields := make(map[string]FieldSchema)
	for _, field := range expected.Fields {
		expectedFields[field.Name] = field
	}

	actualFields := make(map[string]FieldSchema)
	for _, field := range actual.Fields {
		actualFields[field.Name] = field
	}

	// Find missing and modified columns
	for _, expectedField := range expected.Fields {
		actualField, exists := actualFields[expectedField.Name]
		if !exists {
			// Column exists in expected but not in actual DB
			diff.MissingColumns = append(diff.MissingColumns, expectedField)
			continue
		}

		// Check for modifications
		colDiff, hasChanges, err := compareFields(expectedField, actualField)
		if err != nil {
			return diff, fmt.Errorf("failed to compare field %s: %w", expectedField.Name, err)
		}

		if hasChanges {
			diff.ModifiedColumns = append(diff.ModifiedColumns, colDiff)
		}
	}

	// Find extra columns (in actual but not in expected)
	for actualFieldName := range actualFields {
		if _, exists := expectedFields[actualFieldName]; !exists {
			diff.ExtraColumns = append(diff.ExtraColumns, actualFieldName)
		}
	}

	// Compare indexes
	expectedIndexes := make(map[string]IndexSchema)
	for _, idx := range expected.Indexes {
		expectedIndexes[idx.Name] = idx
	}

	actualIndexes := make(map[string]IndexSchema)
	for _, idx := range actual.Indexes {
		actualIndexes[idx.Name] = idx
	}

	// Find missing indexes
	for _, expectedIdx := range expected.Indexes {
		actualIdx, exists := actualIndexes[expectedIdx.Name]
		if !exists {
			diff.MissingIndexes = append(diff.MissingIndexes, expectedIdx)
			continue
		}

		// Check if index definition changed
		if !indexesEqual(expectedIdx, actualIdx) {
			// Index changed - treat as missing (will be recreated)
			diff.MissingIndexes = append(diff.MissingIndexes, expectedIdx)
			diff.ExtraIndexes = append(diff.ExtraIndexes, actualIdx.Name)
		}
	}

	// Find extra indexes (in actual but not in expected)
	for actualIdxName := range actualIndexes {
		if _, exists := expectedIndexes[actualIdxName]; !exists {
			diff.ExtraIndexes = append(diff.ExtraIndexes, actualIdxName)
		}
	}

	return diff, nil
}

// compareFields compares two field schemas and returns a ColumnDiff if they differ
func compareFields(expected, actual FieldSchema) (ColumnDiff, bool, error) {
	diff := ColumnDiff{
		ColumnName: expected.Name,
		OldType:    string(actual.Type),
		NewType:    expected.Type,
		OldNotNull: actual.NotNull,
		NewNotNull: expected.NotNull,
	}

	// Set default values
	if actual.DefaultValue != "" {
		diff.OldDefault = sql.NullString{String: actual.DefaultValue, Valid: true}
	}
	diff.NewDefault = expected.DefaultValue

	hasChanges := false

	// Check type changes
	if !typesCompatible(expected.Type, actual.Type) {
		hasChanges = true
		// Validate type conversion is possible
		if !canConvertType(actual.Type, expected.Type) {
			return diff, false, fmt.Errorf("%w: cannot convert %s to %s for column %s",
				ErrIncompatibleTypes, actual.Type, expected.Type, expected.Name)
		}
	}

	// Check NOT NULL constraint changes
	if expected.NotNull != actual.NotNull {
		hasChanges = true
	}

	// Check default value changes
	if expected.DefaultValue != actual.DefaultValue {
		hasChanges = true
	}

	return diff, hasChanges, nil
}

// typesCompatible checks if two field types are compatible (same or equivalent)
func typesCompatible(t1, t2 FieldType) bool {
	// Exact match
	if t1 == t2 {
		return true
	}

	// Normalize and compare
	type1 := strings.ToLower(string(t1))
	type2 := strings.ToLower(string(t2))

	// Define compatible type groups
	compatibleGroups := [][]string{
		{"text", "varchar", "character varying"},
		{"integer", "int4", "serial"},
		{"bigint", "int8", "bigserial"},
		{"boolean", "bool"},
		{"timestamptz", "timestamp with time zone"},
	}

	for _, group := range compatibleGroups {
		hasType1 := false
		hasType2 := false
		for _, t := range group {
			if type1 == t {
				hasType1 = true
			}
			if type2 == t {
				hasType2 = true
			}
		}
		if hasType1 && hasType2 {
			return true
		}
	}

	return false
}

// canConvertType checks if a type conversion is safe and supported
func canConvertType(from, to FieldType) bool {
	// Same type is always safe
	if typesCompatible(from, to) {
		return true
	}

	// Define safe conversions
	safeConversions := map[FieldType][]FieldType{
		TypeVarchar: {TypeText},                               // VARCHAR -> TEXT is safe
		TypeInteger: {TypeBigInt, TypeText},                   // INT -> BIGINT is safe
		TypeSerial:  {TypeBigSerial, TypeInteger, TypeBigInt}, // SERIAL conversions
		TypeText:    {TypeVarchar},                            // TEXT -> VARCHAR (with potential truncation warning)
		TypeReal:    {TypeDoublePrecision},                    // float32 -> float64 widen is safe
	}

	allowedTargets, exists := safeConversions[from]
	if !exists {
		return false
	}

	for _, allowed := range allowedTargets {
		if to == allowed {
			return true
		}
	}

	return false
}

// indexesEqual compares two index schemas for equality
func indexesEqual(idx1, idx2 IndexSchema) bool {
	if idx1.Name != idx2.Name {
		return false
	}
	if idx1.Unique != idx2.Unique {
		return false
	}
	if len(idx1.Fields) != len(idx2.Fields) {
		return false
	}
	for i := range idx1.Fields {
		if idx1.Fields[i] != idx2.Fields[i] {
			return false
		}
	}
	return true
}

// DiffDatabase compares expected schemas against the actual database schema
func DiffDatabase(ctx context.Context, db Context, dialect Dialect, expectedSchemas []TableSchema) ([]SchemaDiff, error) {
	diffs := make([]SchemaDiff, 0, len(expectedSchemas))

	for _, expected := range expectedSchemas {
		// Get actual schema from database
		actual, err := IntrospectTable(ctx, db, dialect, expected.Name)
		if err != nil {
			// If table doesn't exist, all columns are missing
			if errors.Is(err, sql.ErrNoRows) {
				diff := SchemaDiff{
					TableName:      expected.Name,
					MissingColumns: expected.Fields,
					MissingIndexes: expected.Indexes,
				}
				diffs = append(diffs, diff)
				continue
			}
			return nil, fmt.Errorf("failed to introspect table %s: %w", expected.Name, err)
		}

		// Compare schemas
		diff, err := CompareSchemas(expected, actual)
		if err != nil {
			return nil, fmt.Errorf("failed to compare schema for table %s: %w", expected.Name, err)
		}

		// Only include tables with changes
		if diff.HasChanges() {
			diffs = append(diffs, diff)
		}
	}

	return diffs, nil
}

// GenerateAlterSQL generates ALTER TABLE statements to apply the schema diff
// GenerateAlterSQL generates ALTER TABLE statements for the given schema diff.
//
// Following the protobuf philosophy, this function ONLY generates ADD and DROP statements.
// Column modifications (type changes, constraint changes, etc.) are NOT supported.
//
// To modify a column, use a multi-step migration with PairedMigration:
//  1. ADD new column with desired schema
//  2. Migrate data from old column to new column (using DataMigration)
//  3. DROP old column (in a separate migration after verification)
//
// This approach:
//   - Works on all databases (SQLite, PostgreSQL, MySQL, etc.)
//   - Forces explicit thinking about data transformations
//   - Prevents accidental data loss
//   - Follows protobuf's additive-only philosophy
func GenerateAlterSQL(diff SchemaDiff, dialect Dialect, allowDestructive bool) ([]string, error) {
	// Reject any column modifications - we only support ADD and DROP
	if len(diff.ModifiedColumns) > 0 {
		modifiedNames := make([]string, len(diff.ModifiedColumns))
		for i, col := range diff.ModifiedColumns {
			modifiedNames[i] = col.ColumnName
		}
		return nil, fmt.Errorf("orm: column modifications not supported - use multi-step migrations (ADD new column → migrate data → DROP old column). Modified columns in table %s: %v",
			diff.TableName, modifiedNames)
	}

	if diff.IsDestructive() && !allowDestructive {
		return nil, fmt.Errorf("%w: schema diff contains DROP operations (ExtraColumns: %v, ExtraIndexes: %v)",
			ErrDestructiveOperation, diff.ExtraColumns, diff.ExtraIndexes)
	}

	statements := make([]string, 0)

	// Generate DROP INDEX statements for extra indexes
	for _, indexName := range diff.ExtraIndexes {
		stmt, err := generateDropIndexSQL(diff.TableName, indexName, dialect)
		if err != nil {
			return nil, fmt.Errorf("failed to generate DROP INDEX for %s: %w", indexName, err)
		}
		statements = append(statements, stmt)
	}

	// Generate DROP COLUMN statements for extra columns
	for _, columnName := range diff.ExtraColumns {
		stmt, err := generateDropColumnSQL(diff.TableName, columnName, dialect)
		if err != nil {
			return nil, fmt.Errorf("failed to generate DROP COLUMN for %s: %w", columnName, err)
		}
		statements = append(statements, stmt)
	}

	// Generate ADD COLUMN statements for missing columns
	for _, field := range diff.MissingColumns {
		stmt, err := generateAddColumnSQL(diff.TableName, field, dialect)
		if err != nil {
			return nil, fmt.Errorf("failed to generate ADD COLUMN for %s: %w", field.Name, err)
		}
		statements = append(statements, stmt)
	}

	// Generate CREATE INDEX statements for missing indexes
	for _, idx := range diff.MissingIndexes {
		stmt := generateIndexSQL(diff.TableName, idx)
		statements = append(statements, stmt)
	}

	return statements, nil
}

// generateAddColumnSQL generates an ADD COLUMN statement
func generateAddColumnSQL(tableName string, field FieldSchema, dialect Dialect) (string, error) {
	quotedTable := dialect.QuoteIdentifier(tableName)
	quotedColumn := dialect.QuoteIdentifier(field.Name)
	fieldType := dialect.MapFieldType(field.Type)

	var parts []string
	parts = append(parts, quotedColumn, fieldType)

	if field.NotNull {
		parts = append(parts, "NOT NULL")
	}

	if field.Unique {
		parts = append(parts, "UNIQUE")
	}

	if field.DefaultValue != "" {
		parts = append(parts, fmt.Sprintf("DEFAULT %s", field.DefaultValue))
	}

	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", quotedTable, strings.Join(parts, " ")), nil
}

// generateDropColumnSQL generates a DROP COLUMN statement
func generateDropColumnSQL(tableName, columnName string, dialect Dialect) (string, error) {
	quotedTable := dialect.QuoteIdentifier(tableName)
	quotedColumn := dialect.QuoteIdentifier(columnName)
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", quotedTable, quotedColumn), nil
}

// generateAlterColumnSQL generates ALTER COLUMN statements for type, nullability, and default changes
func generateAlterColumnSQL(tableName string, colDiff ColumnDiff, dialect Dialect) ([]string, error) {
	quotedTable := dialect.QuoteIdentifier(tableName)
	quotedColumn := dialect.QuoteIdentifier(colDiff.ColumnName)

	statements := make([]string, 0)

	// Check if this is PostgreSQL dialect
	isPostgres := dialect.Name() == "postgres"

	// Change type if different
	if !typesCompatible(colDiff.NewType, FieldType(colDiff.OldType)) {
		if !isPostgres {
			return nil, fmt.Errorf("%w: ALTER COLUMN TYPE for dialect %s", ErrUnsupportedOperation, dialect.Name())
		}

		newType := dialect.MapFieldType(colDiff.NewType)
		stmt := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;",
			quotedTable, quotedColumn, newType)
		statements = append(statements, stmt)
	}

	// Change NOT NULL constraint if different
	if colDiff.NewNotNull != colDiff.OldNotNull {
		if !isPostgres {
			return nil, fmt.Errorf("%w: ALTER COLUMN SET/DROP NOT NULL for dialect %s", ErrUnsupportedOperation, dialect.Name())
		}

		if colDiff.NewNotNull {
			stmt := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;",
				quotedTable, quotedColumn)
			statements = append(statements, stmt)
		} else {
			stmt := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;",
				quotedTable, quotedColumn)
			statements = append(statements, stmt)
		}
	}

	// Change default value if different
	oldDefault := ""
	if colDiff.OldDefault.Valid {
		oldDefault = colDiff.OldDefault.String
	}

	if colDiff.NewDefault != oldDefault {
		if !isPostgres {
			return nil, fmt.Errorf("%w: ALTER COLUMN SET/DROP DEFAULT for dialect %s", ErrUnsupportedOperation, dialect.Name())
		}

		if colDiff.NewDefault != "" {
			stmt := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;",
				quotedTable, quotedColumn, colDiff.NewDefault)
			statements = append(statements, stmt)
		} else {
			stmt := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;",
				quotedTable, quotedColumn)
			statements = append(statements, stmt)
		}
	}

	return statements, nil
}

// generateDropIndexSQL generates a DROP INDEX statement
func generateDropIndexSQL(tableName, indexName string, dialect Dialect) (string, error) {
	quotedIndex := dialect.QuoteIdentifier(indexName)

	// PostgreSQL doesn't require table name for DROP INDEX
	if dialect.Name() == "postgres" {
		return fmt.Sprintf("DROP INDEX IF EXISTS %s;", quotedIndex), nil
	}

	// Other dialects might need table name
	quotedTable := dialect.QuoteIdentifier(tableName)
	return fmt.Sprintf("DROP INDEX IF EXISTS %s ON %s;", quotedIndex, quotedTable), nil
}
