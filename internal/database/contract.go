package database

import (
	"context"
	"database/sql"
)

// SchemaIntrospector connects to databases and introspects their schema.
type SchemaIntrospector interface {
	ConnectDB(ctx context.Context, dsn string) (*sql.DB, error)
	IntrospectSchema(ctx context.Context, db *sql.DB, tableFilter string) ([]Table, error)
	IntrospectTable(ctx context.Context, db *sql.DB, tableName string) (*Table, error)
}

// MigrationService creates and manages database migrations.
type MigrationService interface {
	CreateMigration(ctx context.Context, name string, dir string, opts *MigrationOptions) error
	GatherMigrationContext(ctx context.Context, name string, migDir string, opts MigrationOptions) (*MigrationContext, error)
	GetPreviousMigration(ctx context.Context, dir string) (*PreviousMigrationInfo, error)
	GetMigrationHistory(ctx context.Context, dir string) ([]string, error)
}
