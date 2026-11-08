package orm

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/pkg/observe/dbtrace"
)

// Dialect defines the interface for database-specific implementations.
// Each supported database (PostgreSQL, SQLite, MySQL, etc.) should implement this interface.
type Dialect interface {
	// Name returns the name of the dialect (e.g., "postgres", "sqlite")
	Name() string

	// DriverName returns the name of the database driver to use with sql.Open
	DriverName() string

	// Placeholder returns the placeholder string for a given parameter index (0-based)
	// PostgreSQL uses $1, $2, etc.
	// SQLite and MySQL use ?
	Placeholder(index int) string

	// QuoteIdentifier quotes an identifier (table name, column name) for the dialect
	QuoteIdentifier(identifier string) string

	// MapFieldType maps an ORM FieldType to the dialect-specific SQL type
	MapFieldType(fieldType FieldType) string

	// SupportsReturning returns true if the dialect supports RETURNING clause
	SupportsReturning() bool

	// OnConflictClause returns the dialect-specific ON CONFLICT or equivalent clause
	// for upserts. Takes the conflict column and update columns.
	OnConflictClause(conflictColumn string, updateColumns []string) string

	// TableExistsQuery returns a SQL query to check if a table exists
	TableExistsQuery(tableName string) string

	// ListTablesQuery returns a SQL query to list all tables in the current schema
	ListTablesQuery() string

	// IntrospectColumnsQuery returns a SQL query to introspect columns of a table
	IntrospectColumnsQuery(tableName string) string

	// IntrospectIndexesQuery returns a SQL query to introspect indexes of a table
	IntrospectIndexesQuery(tableName string) string

	// ParseColumnType converts a database-specific type string to FieldType
	ParseColumnType(dbType string) (FieldType, error)

	// ScanColumn scans a row from IntrospectColumnsQuery into an IntrospectedColumn
	ScanColumn(rows *sql.Rows) (IntrospectedColumn, error)

	// ScanIndex scans a row from IntrospectIndexesQuery into index information
	ScanIndex(rows *sql.Rows) (indexName, columnName string, isUnique bool, err error)
}

var (
	dialectsMu      sync.RWMutex
	dialects        = make(map[string]Dialect)
	identifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// ValidateIdentifier checks that a string is a safe SQL identifier (letters, digits, underscores only).
// Returns an error if the identifier contains characters that could be used for SQL injection.
func ValidateIdentifier(name string) error {
	if name == "" {
		return fmt.Errorf("orm: identifier cannot be empty")
	}
	if !identifierRegex.MatchString(name) {
		return fmt.Errorf("orm: invalid identifier %q: must contain only letters, digits, and underscores", name)
	}
	return nil
}

// RegisterDialect registers a dialect for use with the ORM.
// This should be called from init() functions in dialect packages.
func RegisterDialect(dialect Dialect) {
	dialectsMu.Lock()
	defer dialectsMu.Unlock()

	if dialect == nil {
		panic("orm: RegisterDialect dialect is nil")
	}

	name := dialect.Name()
	if name == "" {
		panic("orm: RegisterDialect dialect name is empty")
	}

	if _, exists := dialects[name]; exists {
		panic(fmt.Sprintf("orm: RegisterDialect called twice for dialect %s", name))
	}

	dialects[name] = dialect
}

// GetDialect returns the dialect with the given name.
// Returns nil if the dialect is not registered.
func GetDialect(name string) (Dialect, error) {
	dialectsMu.RLock()
	defer dialectsMu.RUnlock()

	dialect, ok := dialects[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrInvalidDialect, name)
	}

	return dialect, nil
}

// ListDialects returns a list of all registered dialect names.
func ListDialects() []string {
	dialectsMu.RLock()
	defer dialectsMu.RUnlock()

	names := make([]string, 0, len(dialects))
	for name := range dialects {
		names = append(names, name)
	}

	return names
}

// PostgresDialect implements the Dialect interface for PostgreSQL
type PostgresDialect struct{}

func (d *PostgresDialect) Name() string {
	return "postgres"
}

func (d *PostgresDialect) DriverName() string {
	return "postgres"
}

func (d *PostgresDialect) Placeholder(index int) string {
	return fmt.Sprintf("$%d", index+1)
}

func (d *PostgresDialect) QuoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func (d *PostgresDialect) MapFieldType(fieldType FieldType) string {
	// PostgreSQL types are already correct in FieldType
	return string(fieldType)
}

func (d *PostgresDialect) SupportsReturning() bool {
	return true
}

func (d *PostgresDialect) OnConflictClause(conflictColumn string, updateColumns []string) string {
	sets := make([]string, len(updateColumns))
	for i, col := range updateColumns {
		sets[i] = fmt.Sprintf("%s = EXCLUDED.%s", col, col)
	}

	var updateSet string
	if len(sets) > 0 {
		updateSet = " DO UPDATE SET "
		for i, set := range sets {
			if i > 0 {
				updateSet += ", "
			}
			updateSet += set
		}
	} else {
		updateSet = " DO NOTHING"
	}

	return fmt.Sprintf("ON CONFLICT (%s)%s", conflictColumn, updateSet)
}

// TableExistsQuery returns a query to check if a table exists in PostgreSQL
func (d *PostgresDialect) TableExistsQuery(tableName string) string {
	if err := ValidateIdentifier(tableName); err != nil {
		panic(err)
	}
	return fmt.Sprintf(`
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = 'public'
			AND table_name = '%s'
		)`, tableName)
}

// ListTablesQuery returns a query to list all tables in the public schema
func (d *PostgresDialect) ListTablesQuery() string {
	return `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public'
		AND table_type = 'BASE TABLE'
		ORDER BY table_name`
}

// IntrospectColumnsQuery returns a query to introspect columns of a table
func (d *PostgresDialect) IntrospectColumnsQuery(tableName string) string {
	if err := ValidateIdentifier(tableName); err != nil {
		panic(err)
	}
	return fmt.Sprintf(`
		SELECT
			c.column_name,
			c.data_type,
			c.udt_name,
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
				   AND tc.table_name = '%s'
				   AND kcu.column_name = c.column_name),
				false
			) as is_primary_key,
			COALESCE(
				(SELECT true
				 FROM information_schema.table_constraints tc
				 JOIN information_schema.key_column_usage kcu
				   ON tc.constraint_name = kcu.constraint_name
				   AND tc.table_schema = kcu.table_schema
				 WHERE tc.constraint_type = 'UNIQUE'
				   AND tc.table_schema = 'public'
				   AND tc.table_name = '%s'
				   AND kcu.column_name = c.column_name),
				false
			) as is_unique
		FROM information_schema.columns c
		WHERE c.table_schema = 'public'
		  AND c.table_name = '%s'
		ORDER BY c.ordinal_position`, tableName, tableName, tableName)
}

// IntrospectIndexesQuery returns a query to introspect indexes of a table
func (d *PostgresDialect) IntrospectIndexesQuery(tableName string) string {
	if err := ValidateIdentifier(tableName); err != nil {
		panic(err)
	}
	return fmt.Sprintf(`
		SELECT
			i.indexname as index_name,
			a.attname as column_name,
			idx.indisunique as is_unique
		FROM pg_indexes i
		JOIN pg_class t ON t.relname = i.tablename
		JOIN pg_index idx ON idx.indrelid = t.oid
		JOIN pg_class ic ON ic.oid = idx.indexrelid
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(idx.indkey)
		WHERE i.schemaname = 'public'
		  AND i.tablename = '%s'
		  AND NOT idx.indisprimary
		ORDER BY i.indexname, a.attnum`, tableName)
}

// ParseColumnType converts a PostgreSQL type to FieldType
func (d *PostgresDialect) ParseColumnType(dbType string) (FieldType, error) {
	if dbType == "" {
		return "", fmt.Errorf("orm: database type cannot be empty")
	}

	// Map PostgreSQL types to FieldType
	switch dbType {
	case "text":
		return TypeText, nil
	case "character varying", "varchar":
		return TypeVarchar, nil
	case "integer", "int4":
		return TypeInteger, nil
	case "bigint", "int8":
		return TypeBigInt, nil
	case "boolean", "bool":
		return TypeBoolean, nil
	case "timestamp with time zone", "timestamptz":
		return TypeTimestampTZ, nil
	case "jsonb":
		return TypeJSONB, nil
	case "bytea":
		return TypeBytea, nil
	case "serial":
		return TypeSerial, nil
	case "bigserial":
		return TypeBigSerial, nil
	default:
		return "", fmt.Errorf("orm: unknown PostgreSQL column type: %s", dbType)
	}
}

// ScanColumn scans a row from IntrospectColumnsQuery into an IntrospectedColumn
func (d *PostgresDialect) ScanColumn(rows *sql.Rows) (IntrospectedColumn, error) {
	var col IntrospectedColumn
	var dataType, udtName, isNullable string
	var columnDefault sql.NullString

	err := rows.Scan(
		&col.Name,
		&dataType,
		&udtName,
		&isNullable,
		&columnDefault,
		&col.IsPrimaryKey,
		&col.IsUnique,
	)
	if err != nil {
		return IntrospectedColumn{}, fmt.Errorf("orm: failed to scan column row: %w", err)
	}

	// Use udtName for more precise type mapping (e.g., int4, int8, bool)
	col.Type, err = d.ParseColumnType(udtName)
	if err != nil {
		// Fall back to data_type if udt_name fails
		col.Type, err = d.ParseColumnType(dataType)
		if err != nil {
			return IntrospectedColumn{}, err
		}
	}

	col.Nullable = (isNullable == "YES")

	if columnDefault.Valid {
		col.DefaultValue = &columnDefault.String
	}

	return col, nil
}

// ScanIndex scans a row from IntrospectIndexesQuery into index information
func (d *PostgresDialect) ScanIndex(rows *sql.Rows) (indexName, columnName string, isUnique bool, err error) {
	err = rows.Scan(&indexName, &columnName, &isUnique)
	if err != nil {
		return "", "", false, fmt.Errorf("orm: failed to scan index row: %w", err)
	}

	if indexName == "" {
		return "", "", false, fmt.Errorf("orm: scanned index name is empty")
	}

	if columnName == "" {
		return "", "", false, fmt.Errorf("orm: scanned column name is empty for index %s", indexName)
	}

	return indexName, columnName, isUnique, nil
}

// init registers the PostgreSQL dialect
func init() {
	RegisterDialect(&PostgresDialect{})
}

// Helper function to open a database with a specific dialect
func openWithDialect(dialectName, dsn string) (*sql.DB, Dialect, error) {
	dialect, err := GetDialect(dialectName)
	if err != nil {
		return nil, nil, err
	}

	db, err := dbtrace.Open(dialect.DriverName(), dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, dialect, nil
}
