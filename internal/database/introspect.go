package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver via pgx
)

// Table represents a database table with its full schema metadata.
type Table struct {
	Name        string       `json:"name"`
	Schema      string       `json:"schema"`
	Columns     []Column     `json:"columns"`
	PrimaryKey  []string     `json:"primary_key"`
	Indexes     []Index      `json:"indexes"`
	ForeignKeys []ForeignKey `json:"foreign_keys"`
}

// Column represents a database column.
type Column struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Nullable  bool   `json:"nullable"`
	Default   string `json:"default,omitempty"`
	IsPrimary bool   `json:"is_primary"`
	MaxLength *int   `json:"max_length,omitempty"`
	UDTName   string `json:"udt_name,omitempty"`
}

// Index represents a database index.
type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}

// ForeignKey represents a foreign key constraint.
type ForeignKey struct {
	Name             string `json:"name"`
	Column           string `json:"column"`
	ReferencedTable  string `json:"referenced_table"`
	ReferencedColumn string `json:"referenced_column"`
}

// ConnectDB opens a database connection using the pgx driver.
func ConnectDB(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	return db, nil
}

// IntrospectSchema connects to the database and returns the full schema.
// If tableFilter is non-empty, only that table is returned.
func IntrospectSchema(ctx context.Context, db *sql.DB, tableFilter string) ([]Table, error) {
	tables, err := listTables(ctx, db, tableFilter)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}

	var result []Table
	for _, tableName := range tables {
		t, err := IntrospectTable(ctx, db, tableName)
		if err != nil {
			return nil, fmt.Errorf("introspecting table %s: %w", tableName, err)
		}
		result = append(result, *t)
	}
	return result, nil
}

// IntrospectTable returns full metadata for a single table.
func IntrospectTable(ctx context.Context, db *sql.DB, tableName string) (*Table, error) {
	t := &Table{
		Name:   tableName,
		Schema: "public",
	}

	columns, err := getColumns(ctx, db, tableName)
	if err != nil {
		return nil, fmt.Errorf("getting columns: %w", err)
	}
	t.Columns = columns

	// Derive primary key list from columns.
	for _, c := range columns {
		if c.IsPrimary {
			t.PrimaryKey = append(t.PrimaryKey, c.Name)
		}
	}

	indexes, err := getIndexes(ctx, db, tableName)
	if err != nil {
		return nil, fmt.Errorf("getting indexes: %w", err)
	}
	t.Indexes = indexes

	fks, err := getForeignKeys(ctx, db, tableName)
	if err != nil {
		return nil, fmt.Errorf("getting foreign keys: %w", err)
	}
	t.ForeignKeys = fks

	return t, nil
}

func listTables(ctx context.Context, db *sql.DB, filter string) ([]string, error) {
	query := `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public'
			AND table_type = 'BASE TABLE'
	`
	args := []any{}
	if filter != "" {
		query += " AND table_name = $1"
		args = append(args, filter)
	}
	query += " ORDER BY table_name"

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func getColumns(ctx context.Context, db *sql.DB, tableName string) ([]Column, error) {
	query := `
		SELECT
			c.column_name,
			c.data_type,
			c.udt_name,
			c.is_nullable,
			c.column_default,
			c.character_maximum_length,
			CASE WHEN pk.column_name IS NOT NULL THEN true ELSE false END AS is_primary_key
		FROM information_schema.columns c
		LEFT JOIN (
			SELECT ku.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage ku
				ON tc.constraint_name = ku.constraint_name
				AND tc.table_schema = ku.table_schema
			WHERE tc.constraint_type = 'PRIMARY KEY'
				AND tc.table_name = $1
				AND tc.table_schema = 'public'
		) pk ON c.column_name = pk.column_name
		WHERE c.table_name = $1
			AND c.table_schema = 'public'
		ORDER BY c.ordinal_position
	`

	rows, err := db.QueryContext(ctx, query, tableName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var columns []Column
	for rows.Next() {
		var col Column
		var isNullable string
		var defaultValue sql.NullString
		var maxLength sql.NullInt64

		if err := rows.Scan(
			&col.Name,
			&col.Type,
			&col.UDTName,
			&isNullable,
			&defaultValue,
			&maxLength,
			&col.IsPrimary,
		); err != nil {
			return nil, err
		}

		col.Nullable = isNullable == "YES"
		if defaultValue.Valid {
			col.Default = defaultValue.String
		}
		if maxLength.Valid {
			v := int(maxLength.Int64)
			col.MaxLength = &v
		}

		columns = append(columns, col)
	}
	return columns, rows.Err()
}

func getIndexes(ctx context.Context, db *sql.DB, tableName string) ([]Index, error) {
	query := `
		SELECT
			i.relname AS index_name,
			ix.indisunique AS is_unique,
			array_agg(a.attname ORDER BY array_position(ix.indkey, a.attnum)) AS columns
		FROM pg_class t
		JOIN pg_index ix ON t.oid = ix.indrelid
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(ix.indkey)
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE t.relname = $1
			AND n.nspname = 'public'
			AND t.relkind = 'r'
			AND NOT ix.indisprimary
		GROUP BY i.relname, ix.indisunique
		ORDER BY i.relname
	`

	rows, err := db.QueryContext(ctx, query, tableName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var indexes []Index
	for rows.Next() {
		var idx Index
		var colsArray string

		if err := rows.Scan(&idx.Name, &idx.Unique, &colsArray); err != nil {
			return nil, err
		}

		colsArray = strings.Trim(colsArray, "{}")
		if colsArray != "" {
			idx.Columns = strings.Split(colsArray, ",")
		}

		indexes = append(indexes, idx)
	}
	return indexes, rows.Err()
}

func getForeignKeys(ctx context.Context, db *sql.DB, tableName string) ([]ForeignKey, error) {
	query := `
		SELECT
			tc.constraint_name,
			kcu.column_name,
			ccu.table_name AS referenced_table,
			ccu.column_name AS referenced_column
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
			ON tc.constraint_name = kcu.constraint_name
			AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage ccu
			ON tc.constraint_name = ccu.constraint_name
			AND tc.table_schema = ccu.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
			AND tc.table_name = $1
			AND tc.table_schema = 'public'
		ORDER BY tc.constraint_name
	`

	rows, err := db.QueryContext(ctx, query, tableName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var fks []ForeignKey
	for rows.Next() {
		var fk ForeignKey
		if err := rows.Scan(&fk.Name, &fk.Column, &fk.ReferencedTable, &fk.ReferencedColumn); err != nil {
			return nil, err
		}
		fks = append(fks, fk)
	}
	return fks, rows.Err()
}

// FormatSchemaText formats schema as a human-readable text table.
func FormatSchemaText(tables []Table) string {
	var sb strings.Builder

	for i, t := range tables {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "Table: %s.%s\n", t.Schema, t.Name)

		if len(t.PrimaryKey) > 0 {
			fmt.Fprintf(&sb, "Primary Key: %s\n", strings.Join(t.PrimaryKey, ", "))
		}

		sb.WriteString("\nColumns:\n")

		// Calculate column widths for alignment.
		nameWidth, typeWidth := 4, 4
		for _, c := range t.Columns {
			if len(c.Name) > nameWidth {
				nameWidth = len(c.Name)
			}
			ct := columnTypeDisplay(c)
			if len(ct) > typeWidth {
				typeWidth = len(ct)
			}
		}

		header := fmt.Sprintf("  %-*s  %-*s  %-8s  %s", nameWidth, "NAME", typeWidth, "TYPE", "NULLABLE", "CONSTRAINTS")
		sb.WriteString(header + "\n")
		sb.WriteString("  " + strings.Repeat("-", len(header)-2) + "\n")

		for _, c := range t.Columns {
			nullable := "YES"
			if !c.Nullable {
				nullable = "NO"
			}

			var constraints []string
			if c.IsPrimary {
				constraints = append(constraints, "PRIMARY KEY")
			}
			if c.Default != "" {
				constraints = append(constraints, fmt.Sprintf("DEFAULT %s", c.Default))
			}

			fmt.Fprintf(&sb, "  %-*s  %-*s  %-8s  %s\n",
				nameWidth, c.Name,
				typeWidth, columnTypeDisplay(c),
				nullable,
				strings.Join(constraints, ", "))
		}

		if len(t.Indexes) > 0 {
			sb.WriteString("\nIndexes:\n")
			for _, idx := range t.Indexes {
				unique := ""
				if idx.Unique {
					unique = " UNIQUE"
				}
				fmt.Fprintf(&sb, "  %s%s (%s)\n", idx.Name, unique, strings.Join(idx.Columns, ", "))
			}
		}

		if len(t.ForeignKeys) > 0 {
			sb.WriteString("\nForeign Keys:\n")
			for _, fk := range t.ForeignKeys {
				fmt.Fprintf(&sb, "  %s: %s -> %s.%s\n", fk.Name, fk.Column, fk.ReferencedTable, fk.ReferencedColumn)
			}
		}
	}

	return sb.String()
}

// FormatSchemaJSON formats schema as JSON.
func FormatSchemaJSON(tables []Table) (string, error) {
	data, err := json.MarshalIndent(tables, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func columnTypeDisplay(c Column) string {
	if c.MaxLength != nil {
		return fmt.Sprintf("%s(%d)", c.Type, *c.MaxLength)
	}
	return c.Type
}
