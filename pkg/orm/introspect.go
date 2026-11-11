package orm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lib/pq" // PostgreSQL array support
)

// IntrospectedColumn represents a database column discovered through introspection
type IntrospectedColumn struct {
	Name         string
	Type         FieldType
	Nullable     bool
	DefaultValue *string
	IsPrimaryKey bool
	IsUnique     bool
}

// IntrospectedIndex represents a database index discovered through introspection
type IntrospectedIndex struct {
	Name     string
	Columns  []string
	IsUnique bool
}

// IntrospectTable retrieves the actual schema of a table from the database.
// It returns detailed information about columns and indexes.
// Returns NewSchemaError if the table doesn't exist or any operation fails.
func IntrospectTable(ctx context.Context, db Context, dialect Dialect, tableName string) (TableSchema, error) {
	if ctx == nil {
		return TableSchema{}, NewSchemaError(tableName, "nil context provided", ErrNilContext)
	}

	if tableName == "" {
		return TableSchema{}, NewSchemaError("", "table name cannot be empty", nil)
	}

	schema := TableSchema{
		Name:    tableName,
		Fields:  make([]FieldSchema, 0),
		Indexes: make([]IndexSchema, 0),
	}

	// Check if table exists
	exists, err := tableExists(ctx, db, dialect, tableName)
	if err != nil {
		return schema, NewSchemaError(tableName, "failed to check table existence", err)
	}

	if !exists {
		return schema, NewSchemaError(tableName, "table does not exist", sql.ErrNoRows)
	}

	// Get columns with primary key and unique constraint information
	columns, err := introspectColumns(ctx, db, dialect, tableName)
	if err != nil {
		return schema, NewSchemaError(tableName, "failed to introspect columns", err)
	}

	// Convert introspected columns to FieldSchema
	for _, col := range columns {
		field, err := columnToFieldSchema(col)
		if err != nil {
			return schema, NewSchemaError(tableName, fmt.Sprintf("failed to parse column %s", col.Name), err)
		}
		schema.Fields = append(schema.Fields, field)
	}

	// Get indexes
	indexes, err := introspectIndexes(ctx, db, dialect, tableName)
	if err != nil {
		return schema, NewSchemaError(tableName, "failed to introspect indexes", err)
	}

	// Convert introspected indexes to IndexSchema (excluding primary key indexes)
	for _, idx := range indexes {
		schema.Indexes = append(schema.Indexes, IndexSchema{
			Name:   idx.Name,
			Fields: idx.Columns,
			Unique: idx.IsUnique,
		})
	}

	return schema, nil
}

// IntrospectAllTables retrieves schemas for all user tables in the database.
// It returns a slice of TableSchema objects, one for each table.
// Returns NewSchemaError if the query fails or any table introspection fails.
func IntrospectAllTables(ctx context.Context, db Context, dialect Dialect) ([]TableSchema, error) {
	if ctx == nil {
		return nil, NewSchemaError("", "nil context provided", ErrNilContext)
	}

	// Get all user table names
	tableNames, err := getUserTables(ctx, db, dialect)
	if err != nil {
		return nil, NewSchemaError("", "failed to get user tables", err)
	}

	schemas := make([]TableSchema, 0, len(tableNames))

	for _, tableName := range tableNames {
		schema, err := IntrospectTable(ctx, db, dialect, tableName)
		if err != nil {
			return nil, NewSchemaError(tableName, "failed to introspect table", err)
		}
		schemas = append(schemas, schema)
	}

	return schemas, nil
}

// tableExists checks if a table exists in the database using dialect-specific queries
func tableExists(ctx context.Context, db Context, dialect Dialect, tableName string) (bool, error) {
	var exists bool
	var query string

	dialectName := dialect.Name()

	switch dialectName {
	case "postgres":
		query = `SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_schema = 'public'
			AND table_name = $1
		)`
	case "sqlite":
		query = `SELECT EXISTS (
			SELECT name FROM sqlite_master
			WHERE type = 'table'
			AND name = ?
		)`
	default:
		return false, NewSchemaError(tableName, fmt.Sprintf("unsupported dialect: %s", dialectName), ErrInvalidDialect)
	}

	err := db.QueryRow(ctx, query, tableName).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("query execution failed: %w", err)
	}

	return exists, nil
}

// getUserTables retrieves all user table names from the database
func getUserTables(ctx context.Context, db Context, dialect Dialect) ([]string, error) {
	var query string
	dialectName := dialect.Name()

	switch dialectName {
	case "postgres":
		query = `SELECT table_name
			FROM information_schema.tables
			WHERE table_schema = 'public'
			AND table_type = 'BASE TABLE'
			ORDER BY table_name`
	case "sqlite":
		query = `SELECT name
			FROM sqlite_master
			WHERE type = 'table'
			AND name NOT LIKE 'sqlite_%'
			ORDER BY name`
	default:
		return nil, fmt.Errorf("unsupported dialect for introspection: %s", dialectName)
	}

	rows, err := db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query tables: %w", err)
	}
	defer rows.Close()

	tableNames := make([]string, 0)
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, fmt.Errorf("failed to scan table name: %w", err)
		}
		tableNames = append(tableNames, tableName)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating table names: %w", err)
	}

	return tableNames, nil
}

// introspectColumns retrieves detailed column information from the database
func introspectColumns(ctx context.Context, db Context, dialect Dialect, tableName string) ([]IntrospectedColumn, error) {
	dialectName := dialect.Name()

	switch dialectName {
	case "postgres":
		return introspectColumnsPostgres(ctx, db, dialect, tableName)
	case "sqlite":
		return introspectColumnsSQLite(ctx, db, dialect, tableName)
	default:
		return nil, fmt.Errorf("unsupported dialect for column introspection: %s", dialectName)
	}
}

// introspectColumnsPostgres retrieves column information for PostgreSQL
func introspectColumnsPostgres(ctx context.Context, db Context, dialect Dialect, tableName string) ([]IntrospectedColumn, error) {
	query := `
		SELECT
			c.column_name,
			c.data_type,
			c.is_nullable,
			c.column_default,
			COALESCE(
				(SELECT true
				 FROM information_schema.table_constraints tc
				 JOIN information_schema.key_column_usage kcu
				   ON tc.constraint_name = kcu.constraint_name
				   AND tc.table_schema = kcu.table_schema
				 WHERE tc.constraint_type = 'PRIMARY KEY'
				   AND tc.table_schema = 'public'
				   AND tc.table_name = $1
				   AND kcu.column_name = c.column_name),
				false
			) AS is_primary_key,
			COALESCE(
				(SELECT true
				 FROM information_schema.table_constraints tc
				 JOIN information_schema.key_column_usage kcu
				   ON tc.constraint_name = kcu.constraint_name
				   AND tc.table_schema = kcu.table_schema
				 WHERE tc.constraint_type = 'UNIQUE'
				   AND tc.table_schema = 'public'
				   AND tc.table_name = $1
				   AND kcu.column_name = c.column_name),
				false
			) AS is_unique
		FROM information_schema.columns c
		WHERE c.table_schema = 'public'
		  AND c.table_name = $1
		ORDER BY c.ordinal_position
	`

	rows, err := db.Query(ctx, query, tableName)
	if err != nil {
		return nil, fmt.Errorf("column query failed: %w", err)
	}
	defer rows.Close()

	columns := make([]IntrospectedColumn, 0)
	for rows.Next() {
		var colName, dataType, isNullable string
		var columnDefault sql.NullString
		var isPrimaryKey, isUnique bool

		err := rows.Scan(
			&colName,
			&dataType,
			&isNullable,
			&columnDefault,
			&isPrimaryKey,
			&isUnique,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan column data: %w", err)
		}

		// Map database type to FieldType
		fieldType, err := mapDatabaseTypeToFieldType(dataType)
		if err != nil {
			return nil, fmt.Errorf("failed to map type for column %s: %w", colName, err)
		}

		col := IntrospectedColumn{
			Name:         colName,
			Type:         fieldType,
			Nullable:     isNullable == "YES",
			IsPrimaryKey: isPrimaryKey,
			IsUnique:     isUnique,
		}

		if columnDefault.Valid {
			col.DefaultValue = &columnDefault.String
		}

		columns = append(columns, col)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating columns: %w", err)
	}

	if len(columns) == 0 {
		return nil, fmt.Errorf("no columns found for table %s", tableName)
	}

	return columns, nil
}

// introspectColumnsSQLite retrieves column information for SQLite
func introspectColumnsSQLite(ctx context.Context, db Context, dialect Dialect, tableName string) ([]IntrospectedColumn, error) {
	query := fmt.Sprintf("PRAGMA table_info(%s)", tableName)

	rows, err := db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("column query failed: %w", err)
	}
	defer rows.Close()

	columns := make([]IntrospectedColumn, 0)
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, pk int
		var dfltValue sql.NullString

		err := rows.Scan(&cid, &name, &dataType, &notNull, &dfltValue, &pk)
		if err != nil {
			return nil, fmt.Errorf("failed to scan column data: %w", err)
		}

		// Map database type to FieldType
		fieldType, err := mapDatabaseTypeToFieldType(dataType)
		if err != nil {
			return nil, fmt.Errorf("failed to map type for column %s: %w", name, err)
		}

		// In SQLite, primary key columns are implicitly NOT NULL even if the notnull flag is 0
		// See: https://www.sqlite.org/lang_createtable.html#primkeyconst
		isPrimaryKey := pk > 0
		isNullable := notNull == 0 && !isPrimaryKey

		col := IntrospectedColumn{
			Name:         name,
			Type:         fieldType,
			Nullable:     isNullable,
			IsPrimaryKey: isPrimaryKey,
			IsUnique:     false, // SQLite PRAGMA doesn't provide unique info easily
		}

		if dfltValue.Valid {
			col.DefaultValue = &dfltValue.String
		}

		columns = append(columns, col)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating columns: %w", err)
	}

	if len(columns) == 0 {
		return nil, fmt.Errorf("no columns found for table %s", tableName)
	}

	return columns, nil
}

// introspectIndexes retrieves index information from the database
func introspectIndexes(ctx context.Context, db Context, dialect Dialect, tableName string) ([]IntrospectedIndex, error) {
	dialectName := dialect.Name()

	switch dialectName {
	case "postgres":
		return introspectIndexesPostgres(ctx, db, dialect, tableName)
	case "sqlite":
		return introspectIndexesSQLite(ctx, db, dialect, tableName)
	default:
		return nil, fmt.Errorf("unsupported dialect for index introspection: %s", dialectName)
	}
}

// introspectIndexesPostgres retrieves index information for PostgreSQL
func introspectIndexesPostgres(ctx context.Context, db Context, dialect Dialect, tableName string) ([]IntrospectedIndex, error) {
	query := `
		SELECT
			i.relname AS index_name,
			ix.indisunique AS is_unique,
			array_agg(a.attname ORDER BY array_position(ix.indkey, a.attnum)) AS column_names
		FROM pg_class t
		JOIN pg_index ix ON t.oid = ix.indrelid
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(ix.indkey)
		WHERE t.relname = $1
		  AND t.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'public')
		  AND NOT ix.indisprimary
		GROUP BY i.relname, ix.indisunique
		ORDER BY i.relname
	`

	rows, err := db.Query(ctx, query, tableName)
	if err != nil {
		return nil, fmt.Errorf("index query failed: %w", err)
	}
	defer rows.Close()

	indexes := make([]IntrospectedIndex, 0)
	for rows.Next() {
		var indexName string
		var isUnique bool
		var columnNames pq.StringArray

		err := rows.Scan(&indexName, &isUnique, &columnNames)
		if err != nil {
			return nil, fmt.Errorf("failed to scan index data: %w", err)
		}

		idx := IntrospectedIndex{
			Name:     indexName,
			Columns:  []string(columnNames),
			IsUnique: isUnique,
		}

		indexes = append(indexes, idx)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating indexes: %w", err)
	}

	return indexes, nil
}

// introspectIndexesSQLite retrieves index information for SQLite
func introspectIndexesSQLite(ctx context.Context, db Context, dialect Dialect, tableName string) ([]IntrospectedIndex, error) {
	// First, get the list of indexes
	query := fmt.Sprintf("PRAGMA index_list(%s)", tableName)

	rows, err := db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("index query failed: %w", err)
	}

	// Collect index metadata first
	type indexInfo struct {
		name   string
		unique bool
	}
	var indexList []indexInfo

	for rows.Next() {
		var seq int
		var name string
		var unique int
		var origin string
		var partial int

		err := rows.Scan(&seq, &name, &unique, &origin, &partial)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan index data: %w", err)
		}

		// Skip primary key indexes
		if origin == "pk" {
			continue
		}

		indexList = append(indexList, indexInfo{
			name:   name,
			unique: unique == 1,
		})
	}
	rows.Close()

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating indexes: %w", err)
	}

	// Now get columns for each index
	indexes := make([]IntrospectedIndex, 0, len(indexList))
	for _, info := range indexList {
		colQuery := fmt.Sprintf("PRAGMA index_info(%s)", info.name)
		colRows, err := db.Query(ctx, colQuery)
		if err != nil {
			return nil, fmt.Errorf("failed to query index columns for %s: %w", info.name, err)
		}

		columnNames := make([]string, 0)
		for colRows.Next() {
			var seqno, cid int
			var colName sql.NullString

			if err := colRows.Scan(&seqno, &cid, &colName); err != nil {
				colRows.Close()
				return nil, fmt.Errorf("failed to scan index column: %w", err)
			}

			if colName.Valid {
				columnNames = append(columnNames, colName.String)
			}
		}
		colRows.Close()

		if err := colRows.Err(); err != nil {
			return nil, fmt.Errorf("error iterating index columns: %w", err)
		}

		idx := IntrospectedIndex{
			Name:     info.name,
			Columns:  columnNames,
			IsUnique: info.unique,
		}

		indexes = append(indexes, idx)
	}

	return indexes, nil
}

// columnToFieldSchema converts an IntrospectedColumn to a FieldSchema
func columnToFieldSchema(col IntrospectedColumn) (FieldSchema, error) {
	field := FieldSchema{
		Name:       col.Name,
		Type:       col.Type,
		PrimaryKey: col.IsPrimaryKey,
		Unique:     col.IsUnique,
		NotNull:    !col.Nullable,
	}

	if col.DefaultValue != nil {
		field.DefaultValue = *col.DefaultValue
	}

	return field, nil
}

// mapDatabaseTypeToFieldType maps database-specific type names to FieldType
// Returns an error if the type cannot be parsed
func mapDatabaseTypeToFieldType(dbType string) (FieldType, error) {
	dbType = strings.ToLower(strings.TrimSpace(dbType))

	// In SQLite, columns without a type default to TEXT affinity
	if dbType == "" {
		return TypeText, nil
	}

	typeMap := map[string]FieldType{
		// PostgreSQL types
		"text":                     TypeText,
		"character varying":        TypeVarchar,
		"varchar":                  TypeVarchar,
		"integer":                  TypeInteger,
		"int":                      TypeInteger,
		"int4":                     TypeInteger,
		"serial":                   TypeSerial,
		"bigint":                   TypeBigInt,
		"int8":                     TypeBigInt,
		"bigserial":                TypeBigSerial,
		"boolean":                  TypeBoolean,
		"bool":                     TypeBoolean,
		"timestamp with time zone": TypeTimestampTZ,
		"timestamptz":              TypeTimestampTZ,
		"jsonb":                    TypeJSONB,
		"bytea":                    TypeBytea,
		// Floating-point types — PostgreSQL has dedicated REAL (float32)
		// and DOUBLE PRECISION (float64) columns; SQLite stores both as
		// REAL (8-byte) but we still surface the proto distinction so
		// DDL introspection / contract-sync sees the intended width.
		"real":             TypeReal,
		"float4":           TypeReal,
		"double precision": TypeDoublePrecision,
		"float8":           TypeDoublePrecision,
		"double":           TypeDoublePrecision,
		// SQLite types
		"datetime":  TypeTimestampTZ,
		"timestamp": TypeTimestampTZ,
		"tinyint":   TypeInteger,
		"smallint":  TypeInteger,
		"mediumint": TypeInteger,
		"blob":      TypeBytea,
		"clob":      TypeText,
		"char":      TypeText,
		"float":     TypeReal,
		"numeric":   TypeDoublePrecision,
		"decimal":   TypeDoublePrecision,
	}

	if fieldType, exists := typeMap[dbType]; exists {
		return fieldType, nil
	}

	// Try partial matching for types with modifiers (e.g., "character varying(255)")
	for key, fieldType := range typeMap {
		if strings.HasPrefix(dbType, key) {
			return fieldType, nil
		}
	}

	return "", fmt.Errorf("unknown database type: %s", dbType)
}
