package orm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Migration represents a database migration
type Migration struct {
	// Version is a unique identifier for this migration (e.g., "20240101_001", "v1.0.0")
	Version string

	// Description is a human-readable description of the migration
	Description string

	// Up is the function to apply the migration.
	//
	// There is no Down. Forge rolls forward only: a reverse step written
	// before the release ran cannot account for what the release wrote, so
	// a bad migration is repaired by a NEW forward migration instead.
	Up func(ctx context.Context, db Context) error
}

// PairedMigration represents a migration that combines schema changes with optional data migrations
type PairedMigration struct {
	// Version is a unique identifier for this migration (e.g., "20240101_001", "v1.0.0")
	Version string

	// Description is a human-readable description of the migration
	Description string

	// SchemaChanges are the proto-based table schemas to migrate to
	SchemaChanges []TableSchema

	// DataMigration is an optional raw SQL migration to run after schema changes
	// This is executed in the same transaction as the schema changes
	DataMigration *Migration
}

// MigrationManager manages database migrations
type MigrationManager struct {
	client     *Client
	migrations []*Migration
	tableName  string
}

// NewMigrationManager creates a new migration manager
func NewMigrationManager(client *Client) *MigrationManager {
	return &MigrationManager{
		client:     client,
		migrations: []*Migration{},
		tableName:  "schema_migrations",
	}
}

// SetTableName sets the name of the migrations tracking table
func (m *MigrationManager) SetTableName(name string) {
	m.tableName = name
}

// Register registers a migration
func (m *MigrationManager) Register(migration *Migration) error {
	if migration.Version == "" {
		return fmt.Errorf("migration version cannot be empty")
	}
	if migration.Up == nil {
		return fmt.Errorf("migration %s: Up function cannot be nil", migration.Version)
	}

	// Check for duplicate versions
	for _, existing := range m.migrations {
		if existing.Version == migration.Version {
			return fmt.Errorf("migration %s already registered", migration.Version)
		}
	}

	m.migrations = append(m.migrations, migration)
	return nil
}

// RegisterMany registers multiple migrations
func (m *MigrationManager) RegisterMany(migrations ...*Migration) error {
	for _, migration := range migrations {
		if err := m.Register(migration); err != nil {
			return err
		}
	}
	return nil
}

// ensureMigrationsTable creates the migrations tracking table if it doesn't exist
func (m *MigrationManager) ensureMigrationsTable(ctx context.Context) error {
	quotedTable := m.client.Dialect().QuoteIdentifier(m.tableName)
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			version VARCHAR(255) PRIMARY KEY,
			description TEXT,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`, quotedTable)

	_, err := m.client.Exec(ctx, query)
	return err
}

// getAppliedMigrations returns a map of applied migration versions
func (m *MigrationManager) getAppliedMigrations(ctx context.Context) (map[string]time.Time, error) {
	quotedTable := m.client.Dialect().QuoteIdentifier(m.tableName)
	query := fmt.Sprintf("SELECT version, applied_at FROM %s", quotedTable)
	rows, err := m.client.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[string]time.Time)
	for rows.Next() {
		var version string
		var appliedAt time.Time
		if err := rows.Scan(&version, &appliedAt); err != nil {
			return nil, err
		}
		applied[version] = appliedAt
	}

	return applied, rows.Err()
}

// recordMigration records that a migration has been applied
func (m *MigrationManager) recordMigration(ctx context.Context, db Context, migration *Migration) error {
	d := m.client.Dialect()
	quotedTable := d.QuoteIdentifier(m.tableName)
	query := fmt.Sprintf(
		"INSERT INTO %s (version, description) VALUES (%s, %s)",
		quotedTable, d.Placeholder(0), d.Placeholder(1),
	)
	_, err := db.Exec(ctx, query, migration.Version, migration.Description)
	return err
}

// Migrate runs all pending migrations
func (m *MigrationManager) Migrate(ctx context.Context) error {
	// Ensure migrations table exists
	if err := m.ensureMigrationsTable(ctx); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get applied migrations
	applied, err := m.getAppliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	// Sort migrations by version
	sortedMigrations := make([]*Migration, len(m.migrations))
	copy(sortedMigrations, m.migrations)
	sort.Slice(sortedMigrations, func(i, j int) bool {
		return sortedMigrations[i].Version < sortedMigrations[j].Version
	})

	// Run pending migrations
	for _, migration := range sortedMigrations {
		if _, exists := applied[migration.Version]; exists {
			// Migration already applied
			continue
		}

		// Run migration in a transaction
		err := m.client.RunTransaction(ctx, ReadCommitted, func(tx Context) error {
			if err := migration.Up(ctx, tx); err != nil {
				return fmt.Errorf("migration %s failed: %w", migration.Version, err)
			}

			// Record migration within the transaction
			if err := m.recordMigration(ctx, tx, migration); err != nil {
				return fmt.Errorf("failed to record migration %s: %w", migration.Version, err)
			}

			return nil
		})

		if err != nil {
			return err
		}
	}

	return nil
}

// MigrateTo migrates to a specific version
func (m *MigrationManager) MigrateTo(ctx context.Context, targetVersion string) error {
	// Ensure migrations table exists
	if err := m.ensureMigrationsTable(ctx); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get applied migrations
	applied, err := m.getAppliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	// Sort migrations by version
	sortedMigrations := make([]*Migration, len(m.migrations))
	copy(sortedMigrations, m.migrations)
	sort.Slice(sortedMigrations, func(i, j int) bool {
		return sortedMigrations[i].Version < sortedMigrations[j].Version
	})

	// Run pending migrations up to target version
	for _, migration := range sortedMigrations {
		if migration.Version > targetVersion {
			break
		}

		if _, exists := applied[migration.Version]; exists {
			// Migration already applied
			continue
		}

		// Run migration in a transaction
		err := m.client.RunTransaction(ctx, ReadCommitted, func(tx Context) error {
			if err := migration.Up(ctx, tx); err != nil {
				return fmt.Errorf("migration %s failed: %w", migration.Version, err)
			}

			// Record migration within the transaction
			if err := m.recordMigration(ctx, tx, migration); err != nil {
				return fmt.Errorf("failed to record migration %s: %w", migration.Version, err)
			}

			return nil
		})

		if err != nil {
			return err
		}
	}

	return nil
}

// Status returns the status of all migrations
func (m *MigrationManager) Status(ctx context.Context) ([]MigrationStatus, error) {
	// Ensure migrations table exists
	if err := m.ensureMigrationsTable(ctx); err != nil {
		return nil, fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get applied migrations
	applied, err := m.getAppliedMigrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get applied migrations: %w", err)
	}

	// Sort migrations by version
	sortedMigrations := make([]*Migration, len(m.migrations))
	copy(sortedMigrations, m.migrations)
	sort.Slice(sortedMigrations, func(i, j int) bool {
		return sortedMigrations[i].Version < sortedMigrations[j].Version
	})

	// Build status list
	var statuses []MigrationStatus
	for _, migration := range sortedMigrations {
		appliedAt, exists := applied[migration.Version]
		statuses = append(statuses, MigrationStatus{
			Version:     migration.Version,
			Description: migration.Description,
			Applied:     exists,
			AppliedAt:   appliedAt,
		})
	}

	return statuses, nil
}

// MigrationStatus represents the status of a migration
type MigrationStatus struct {
	Version     string
	Description string
	Applied     bool
	AppliedAt   time.Time
}

// NewSQLMigration creates a forward-only migration from a SQL string.
func NewSQLMigration(version, description, upSQL string) *Migration {
	return &Migration{
		Version:     version,
		Description: description,
		Up: func(ctx context.Context, db Context) error {
			_, err := db.Exec(ctx, upSQL)
			return err
		},
	}
}

// Helper to create a migration that generates schema from protobuf
func NewSchemaCreateMigration(version string, schemas ...TableSchema) *Migration {
	return &Migration{
		Version:     version,
		Description: "Create tables from protobuf schemas",
		Up: func(ctx context.Context, db Context) error {
			for _, schema := range schemas {
				sql := GenerateCreateTableSQL(schema)
				if _, err := db.Exec(ctx, sql); err != nil {
					return fmt.Errorf("failed to create table %s: %w", schema.Name, err)
				}
			}
			return nil
		},
	}
}

// GenerateMigration diffs the provided schemas against the database and generates ALTER statements
// Returns a Migration with the generated SQL. Returns error early if schema diff or SQL generation fails.
func (m *MigrationManager) GenerateMigration(ctx context.Context, schemas []TableSchema) (*Migration, error) {
	if len(schemas) == 0 {
		return nil, fmt.Errorf("no schemas provided")
	}

	// Diff schemas against database
	diffs, err := DiffDatabase(ctx, m.client, m.client.dialect, schemas)
	if err != nil {
		return nil, fmt.Errorf("failed to diff schemas: %w", err)
	}

	// Check if there are any changes
	hasChanges := false
	for _, diff := range diffs {
		if diff.HasChanges() {
			hasChanges = true
			break
		}
	}

	if !hasChanges {
		return nil, fmt.Errorf("no schema changes detected")
	}

	// Generate ALTER statements for all diffs
	var allStatements []string
	for _, diff := range diffs {
		if !diff.HasChanges() {
			continue
		}

		// Check if table needs to be created (all columns are missing)
		actualSchema, err := IntrospectTable(ctx, m.client, m.client.dialect, diff.TableName)
		if err != nil && len(diff.MissingColumns) > 0 && len(actualSchema.Fields) == 0 {
			// Table doesn't exist, create it
			for _, schema := range schemas {
				if schema.Name == diff.TableName {
					createSQL := GenerateCreateTableSQL(schema)
					allStatements = append(allStatements, createSQL)
					break
				}
			}
			continue
		}

		// Generate ALTER statements
		statements, err := GenerateAlterSQL(diff, m.client.dialect, false)
		if err != nil {
			return nil, fmt.Errorf("failed to generate ALTER SQL for table %s: %w", diff.TableName, err)
		}

		allStatements = append(allStatements, statements...)
	}

	if len(allStatements) == 0 {
		return nil, fmt.Errorf("no SQL statements generated")
	}

	// Create a migration with the generated SQL
	migration := &Migration{
		Version:     fmt.Sprintf("auto_%d", time.Now().Unix()),
		Description: "Auto-generated schema migration",
		Up: func(ctx context.Context, db Context) error {
			for _, stmt := range allStatements {
				if _, err := db.Exec(ctx, stmt); err != nil {
					return fmt.Errorf("failed to execute: %s\nError: %w", stmt, err)
				}
			}
			return nil
		},
	}

	return migration, nil
}

// PlanMigration is a dry-run function that returns the SQL that would be executed without executing it
// Returns the SQL statements as a string. Returns error early if schema diff or SQL generation fails.
func (m *MigrationManager) PlanMigration(ctx context.Context, schemas []TableSchema) (string, error) {
	if len(schemas) == 0 {
		return "", fmt.Errorf("no schemas provided")
	}

	// Diff schemas against database
	diffs, err := DiffDatabase(ctx, m.client, m.client.dialect, schemas)
	if err != nil {
		return "", fmt.Errorf("failed to diff schemas: %w", err)
	}

	// Check if there are any changes
	hasChanges := false
	for _, diff := range diffs {
		if diff.HasChanges() {
			hasChanges = true
			break
		}
	}

	if !hasChanges {
		return "-- No schema changes detected\n", nil
	}

	// Generate ALTER statements for all diffs
	var allStatements []string
	for _, diff := range diffs {
		if !diff.HasChanges() {
			continue
		}

		// Check if table needs to be created (all columns are missing)
		actualSchema, err := IntrospectTable(ctx, m.client, m.client.dialect, diff.TableName)
		if err != nil && len(diff.MissingColumns) > 0 && len(actualSchema.Fields) == 0 {
			// Table doesn't exist, create it
			for _, schema := range schemas {
				if schema.Name == diff.TableName {
					createSQL := GenerateCreateTableSQL(schema)
					allStatements = append(allStatements, createSQL)
					break
				}
			}
			continue
		}

		// Generate ALTER statements
		statements, err := GenerateAlterSQL(diff, m.client.dialect, false)
		if err != nil {
			return "", fmt.Errorf("failed to generate ALTER SQL for table %s: %w", diff.TableName, err)
		}

		allStatements = append(allStatements, statements...)
	}

	if len(allStatements) == 0 {
		return "-- No SQL statements generated\n", nil
	}

	// Build SQL string
	var result strings.Builder
	result.WriteString("-- Migration Plan\n")
	result.WriteString("-- Generated at: ")
	result.WriteString(time.Now().Format(time.RFC3339))
	result.WriteString("\n\n")

	for i, stmt := range allStatements {
		result.WriteString(fmt.Sprintf("-- Statement %d\n", i+1))
		result.WriteString(stmt)
		result.WriteString("\n\n")
	}

	return result.String(), nil
}

// RegisterPairedMigration registers a paired migration that combines schema changes with optional data migrations
// Schema changes are executed first, then data migrations, all in the same transaction.
// Returns error early if version conflicts are detected or if migrations are invalid.
func (m *MigrationManager) RegisterPairedMigration(paired *PairedMigration) error {
	if paired == nil {
		return fmt.Errorf("paired migration cannot be nil")
	}

	if paired.Version == "" {
		return fmt.Errorf("paired migration version cannot be empty")
	}

	if len(paired.SchemaChanges) == 0 && paired.DataMigration == nil {
		return fmt.Errorf("paired migration %s must have either schema changes or data migration", paired.Version)
	}

	// Check for version conflicts with existing migrations
	for _, existing := range m.migrations {
		if existing.Version == paired.Version {
			return fmt.Errorf("migration version %s already registered (conflicts with paired migration)", paired.Version)
		}
	}

	// If data migration is provided, check its version matches
	if paired.DataMigration != nil {
		if paired.DataMigration.Version != "" && paired.DataMigration.Version != paired.Version {
			return fmt.Errorf("paired migration version %s conflicts with data migration version %s",
				paired.Version, paired.DataMigration.Version)
		}
		// Override data migration version to match paired migration version
		paired.DataMigration.Version = paired.Version
	}

	// Create a single migration that executes both schema and data changes
	migration := &Migration{
		Version:     paired.Version,
		Description: paired.Description,
		Up: func(ctx context.Context, db Context) error {
			// Execute schema changes first
			if len(paired.SchemaChanges) > 0 {
				// Diff against current database state
				diffs, err := DiffDatabase(ctx, db, m.client.dialect, paired.SchemaChanges)
				if err != nil {
					return fmt.Errorf("failed to diff schemas: %w", err)
				}

				// Generate and execute ALTER statements
				for _, diff := range diffs {
					if !diff.HasChanges() {
						continue
					}

					// Check if table needs to be created
					actualSchema, err := IntrospectTable(ctx, db, m.client.dialect, diff.TableName)
					if err != nil && len(diff.MissingColumns) > 0 && len(actualSchema.Fields) == 0 {
						// Table doesn't exist, create it
						for _, schema := range paired.SchemaChanges {
							if schema.Name == diff.TableName {
								createSQL := GenerateCreateTableSQL(schema)
								if _, err := db.Exec(ctx, createSQL); err != nil {
									return fmt.Errorf("failed to create table %s: %w", schema.Name, err)
								}
								break
							}
						}
						continue
					}

					// Generate ALTER statements
					statements, err := GenerateAlterSQL(diff, m.client.dialect, false)
					if err != nil {
						return fmt.Errorf("failed to generate ALTER SQL for table %s: %w", diff.TableName, err)
					}

					// Execute each statement
					for _, stmt := range statements {
						if _, err := db.Exec(ctx, stmt); err != nil {
							return fmt.Errorf("failed to execute schema change: %s\nError: %w", stmt, err)
						}
					}
				}
			}

			// Then execute data migration if provided
			if paired.DataMigration != nil && paired.DataMigration.Up != nil {
				if err := paired.DataMigration.Up(ctx, db); err != nil {
					return fmt.Errorf("data migration failed: %w", err)
				}
			}

			return nil
		},
	}

	// Register the combined migration
	return m.Register(migration)
}

// AutoMigrate is a convenience function that automatically generates and applies migrations for the provided schemas
// WARNING: This is a DANGEROUS operation that should ONLY be used in development environments.
// - It automatically modifies your database schema without explicit review
// - There is no rollback: forge rolls forward only
// - It may cause data loss if destructive operations are allowed
// - Production databases should use explicit migrations instead
//
// Returns error early if schema diff, SQL generation, or database operations fail.
func (m *MigrationManager) AutoMigrate(ctx context.Context, schemas []TableSchema) error {
	if len(schemas) == 0 {
		return fmt.Errorf("no schemas provided")
	}

	// Ensure migrations table exists
	if err := m.ensureMigrationsTable(ctx); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Diff schemas against database
	diffs, err := DiffDatabase(ctx, m.client, m.client.dialect, schemas)
	if err != nil {
		return fmt.Errorf("failed to diff schemas: %w", err)
	}

	// Check if there are any changes
	hasChanges := false
	for _, diff := range diffs {
		if diff.HasChanges() {
			hasChanges = true
			break
		}
	}

	if !hasChanges {
		// No changes needed
		return nil
	}

	// Generate and execute migrations in a transaction
	return m.client.RunTransaction(ctx, ReadCommitted, func(tx Context) error {
		for _, diff := range diffs {
			if !diff.HasChanges() {
				continue
			}

			// Check if table needs to be created
			actualSchema, err := IntrospectTable(ctx, tx, m.client.dialect, diff.TableName)
			if err != nil && len(diff.MissingColumns) > 0 && len(actualSchema.Fields) == 0 {
				// Table doesn't exist, create it
				for _, schema := range schemas {
					if schema.Name == diff.TableName {
						createSQL := GenerateCreateTableSQL(schema)
						if _, err := tx.Exec(ctx, createSQL); err != nil {
							return fmt.Errorf("failed to create table %s: %w", schema.Name, err)
						}
						break
					}
				}
				continue
			}

			// Generate ALTER statements
			statements, err := GenerateAlterSQL(diff, m.client.dialect, false)
			if err != nil {
				return fmt.Errorf("failed to generate ALTER SQL for table %s: %w", diff.TableName, err)
			}

			// Execute each statement
			for _, stmt := range statements {
				if _, err := tx.Exec(ctx, stmt); err != nil {
					return fmt.Errorf("failed to execute: %s\nError: %w", stmt, err)
				}
			}
		}

		// Record the auto-migration within the transaction
		migration := &Migration{
			Version:     fmt.Sprintf("auto_%d", time.Now().UnixNano()),
			Description: "Auto-migration",
		}
		if err := m.recordMigration(ctx, tx, migration); err != nil {
			return fmt.Errorf("failed to record auto-migration: %w", err)
		}

		return nil
	})
}
