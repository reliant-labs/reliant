package orm

import (
	"context"
	"fmt"
	"strings"
)

// FieldType represents a database field type
type FieldType string

const (
	TypeText        FieldType = "TEXT"
	TypeVarchar     FieldType = "VARCHAR"
	TypeInteger     FieldType = "INTEGER"
	TypeBigInt      FieldType = "BIGINT"
	TypeBoolean     FieldType = "BOOLEAN"
	TypeTimestampTZ FieldType = "TIMESTAMPTZ"
	TypeJSONB       FieldType = "JSONB"
	TypeBytea       FieldType = "BYTEA"
	TypeSerial      FieldType = "SERIAL"
	TypeBigSerial   FieldType = "BIGSERIAL"
	// TypeReal stores a single-precision floating point value (IEEE 754
	// 32-bit). Maps to PostgreSQL REAL and to SQLite REAL (which is 8-byte
	// internally — SQLite has no float32 column type). Used for proto
	// `float` / `google.protobuf.FloatValue` fields.
	TypeReal FieldType = "REAL"
	// TypeDoublePrecision stores a double-precision floating point value
	// (IEEE 754 64-bit). Maps to PostgreSQL DOUBLE PRECISION and to
	// SQLite REAL. Used for proto `double` / `google.protobuf.DoubleValue`
	// fields. Before TypeDoublePrecision landed, protoc-gen-forge emitted
	// orm.TypeText for double columns; the runtime Scan still worked
	// because Scan uses *float64, but anything that consults
	// TableSchema() (DDL introspection / contract sync) saw TEXT for
	// numeric columns. See orm-typetext-for-double in FORGE_BACKLOG.
	TypeDoublePrecision FieldType = "DOUBLE PRECISION"
)

// FieldSchema represents a database field schema
type FieldSchema struct {
	Name         string
	Type         FieldType
	PrimaryKey   bool
	Unique       bool
	NotNull      bool
	DefaultValue string
}

// TableSchema represents a database table schema
type TableSchema struct {
	Name    string
	Fields  []FieldSchema
	Indexes []IndexSchema
}

// IndexSchema represents a database index
type IndexSchema struct {
	Name   string
	Fields []string
	Unique bool
}

// quoteIdent quotes a SQL identifier to prevent SQL injection and reserved-word
// collisions. Embedded double-quotes are escaped by doubling them.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// GenerateCreateTableSQL generates a CREATE TABLE statement
func GenerateCreateTableSQL(schema TableSchema) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n", quoteIdent(schema.Name)))

	// Add fields
	for i, field := range schema.Fields {
		if i > 0 {
			sb.WriteString(",\n")
		}
		sb.WriteString("  ")
		sb.WriteString(generateFieldSQL(field))
	}

	// Add primary key constraint if not inline
	var pkFields []string
	for _, field := range schema.Fields {
		if field.PrimaryKey {
			pkFields = append(pkFields, quoteIdent(field.Name))
		}
	}
	if len(pkFields) > 1 {
		sb.WriteString(",\n  PRIMARY KEY (")
		sb.WriteString(strings.Join(pkFields, ", "))
		sb.WriteString(")")
	}

	sb.WriteString("\n);")

	// Add indexes
	for _, idx := range schema.Indexes {
		sb.WriteString("\n")
		sb.WriteString(generateIndexSQL(schema.Name, idx))
	}

	return sb.String()
}

func generateFieldSQL(field FieldSchema) string {
	var parts []string

	parts = append(parts, quoteIdent(field.Name))
	parts = append(parts, string(field.Type))

	if field.PrimaryKey {
		parts = append(parts, "PRIMARY KEY")
	}

	if field.NotNull && !field.PrimaryKey {
		parts = append(parts, "NOT NULL")
	}

	if field.Unique {
		parts = append(parts, "UNIQUE")
	}

	if field.DefaultValue != "" {
		parts = append(parts, fmt.Sprintf("DEFAULT %s", field.DefaultValue))
	}

	return strings.Join(parts, " ")
}

func generateIndexSQL(tableName string, idx IndexSchema) string {
	uniqueStr := ""
	if idx.Unique {
		uniqueStr = "UNIQUE "
	}

	quotedFields := make([]string, len(idx.Fields))
	for i, f := range idx.Fields {
		quotedFields[i] = quoteIdent(f)
	}

	return fmt.Sprintf("CREATE %sINDEX IF NOT EXISTS %s ON %s (%s);",
		uniqueStr,
		quoteIdent(idx.Name),
		quoteIdent(tableName),
		strings.Join(quotedFields, ", "))
}

// ValidateSchema validates that the database schema matches the expected schema.
// It uses IntrospectTable and CompareSchemas to check for differences.
func ValidateSchema(ctx context.Context, client *Client, schema TableSchema) error {
	actual, err := IntrospectTable(ctx, client, client.Dialect(), schema.Name)
	if err != nil {
		return fmt.Errorf("failed to introspect table %s: %w", schema.Name, err)
	}

	diff, err := CompareSchemas(schema, actual)
	if err != nil {
		return fmt.Errorf("failed to compare schemas for table %s: %w", schema.Name, err)
	}

	if diff.HasChanges() {
		var msgs []string
		for _, col := range diff.MissingColumns {
			msgs = append(msgs, fmt.Sprintf("missing column %s", col.Name))
		}
		for _, col := range diff.ExtraColumns {
			msgs = append(msgs, fmt.Sprintf("extra column %s", col))
		}
		for _, col := range diff.ModifiedColumns {
			msgs = append(msgs, fmt.Sprintf("column %s type mismatch: expected %s, got %s",
				col.ColumnName, col.NewType, col.OldType))
		}
		for _, idx := range diff.MissingIndexes {
			msgs = append(msgs, fmt.Sprintf("missing index %s", idx.Name))
		}
		for _, idx := range diff.ExtraIndexes {
			msgs = append(msgs, fmt.Sprintf("extra index %s", idx))
		}
		return fmt.Errorf("schema mismatch for table %s: %s", schema.Name, strings.Join(msgs, "; "))
	}

	return nil
}
