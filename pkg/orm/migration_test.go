package orm

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func upNoop(context.Context, Context) error { return nil }

func TestMigrationManager_RegisterValidates(t *testing.T) {
	m := NewMigrationManager(nil)
	for _, tc := range []struct {
		name    string
		mig     *Migration
		wantErr string
	}{
		{"empty version", &Migration{Up: upNoop}, "migration version cannot be empty"},
		{"no Up", &Migration{Version: "v1"}, "migration v1: Up function cannot be nil"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := m.Register(tc.mig); err == nil || err.Error() != tc.wantErr {
				t.Fatalf("Register = %v, want %q", err, tc.wantErr)
			}
		})
	}

	if err := m.Register(&Migration{Version: "v1", Up: upNoop}); err != nil {
		t.Fatalf("Register(v1): %v", err)
	}
	if err := m.Register(&Migration{Version: "v1", Up: upNoop}); err == nil || err.Error() != "migration v1 already registered" {
		t.Fatalf("duplicate Register = %v", err)
	}
}

func TestMigrationManager_RegisterManyStopsAtTheFirstBadOne(t *testing.T) {
	m := NewMigrationManager(nil)
	err := m.RegisterMany(
		&Migration{Version: "v1", Up: upNoop},
		&Migration{Version: "v2"}, // no Up
		&Migration{Version: "v3", Up: upNoop},
	)
	if err == nil || !strings.Contains(err.Error(), "migration v2") {
		t.Fatalf("RegisterMany = %v, want the v2 error", err)
	}
	if len(m.migrations) != 1 || m.migrations[0].Version != "v1" {
		t.Fatalf("registered %d migrations after a failure, want only v1", len(m.migrations))
	}
	m.SetTableName("my_migrations")
	if m.tableName != "my_migrations" {
		t.Fatalf("tableName = %q", m.tableName)
	}
}

func TestMigrationManager_RegisterPairedMigrationValidates(t *testing.T) {
	someSchema := []TableSchema{{Name: "t", Fields: []FieldSchema{{Name: "id", Type: TypeInteger, PrimaryKey: true}}}}
	m := NewMigrationManager(nil)
	if err := m.Register(&Migration{Version: "v1", Up: upNoop}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		paired  *PairedMigration
		wantErr string
	}{
		{"nil", nil, "paired migration cannot be nil"},
		{"no version", &PairedMigration{SchemaChanges: someSchema}, "paired migration version cannot be empty"},
		{"nothing to do", &PairedMigration{Version: "v2"}, "paired migration v2 must have either schema changes or data migration"},
		{"version taken", &PairedMigration{Version: "v1", SchemaChanges: someSchema}, "migration version v1 already registered (conflicts with paired migration)"},
		{"data migration under another version", &PairedMigration{Version: "v2", DataMigration: &Migration{Version: "other", Up: upNoop}},
			"paired migration version v2 conflicts with data migration version other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := m.RegisterPairedMigration(tc.paired)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("RegisterPairedMigration = %v, want %q", err, tc.wantErr)
			}
		})
	}

	// A data migration with no version of its own takes the paired one.
	data := &Migration{Up: upNoop}
	if err := m.RegisterPairedMigration(&PairedMigration{Version: "v3", Description: "d", DataMigration: data}); err != nil {
		t.Fatalf("RegisterPairedMigration: %v", err)
	}
	if data.Version != "v3" {
		t.Errorf("data migration version = %q, want it set to the paired version v3", data.Version)
	}
}

func TestMigrationManager_PlanAndGenerateRefuseNoSchemas(t *testing.T) {
	m := NewMigrationManager(nil)
	ctx := context.Background()
	if _, err := m.PlanMigration(ctx, nil); err == nil || err.Error() != "no schemas provided" {
		t.Errorf("PlanMigration(nil) = %v", err)
	}
	if _, err := m.GenerateMigration(ctx, nil); err == nil || err.Error() != "no schemas provided" {
		t.Errorf("GenerateMigration(nil) = %v", err)
	}
	if err := m.AutoMigrate(ctx, nil); err == nil || err.Error() != "no schemas provided" {
		t.Errorf("AutoMigrate(nil) = %v", err)
	}
}

func TestNewSQLMigration_Fields(t *testing.T) {
	m := NewSQLMigration("v9", "adds a thing", "SELECT 1")
	if m.Version != "v9" || m.Description != "adds a thing" || m.Up == nil {
		t.Fatalf("migration = %+v", m)
	}
	s := NewSchemaCreateMigration("v10", TableSchema{Name: "a"})
	if s.Version != "v10" || s.Description != "Create tables from protobuf schemas" || s.Up == nil {
		t.Fatalf("schema migration = %+v", s)
	}
}

func TestGenerateAlterColumnSQL(t *testing.T) {
	pg := &PostgresDialect{}
	for _, tc := range []struct {
		name string
		diff ColumnDiff
		want []string
	}{
		{"type change", ColumnDiff{ColumnName: "n", OldType: "INTEGER", NewType: TypeBigInt, OldNotNull: true, NewNotNull: true},
			[]string{`ALTER TABLE "t" ALTER COLUMN "n" TYPE BIGINT;`}},
		{"compatible type spellings are not a change", ColumnDiff{ColumnName: "n", OldType: "int4", NewType: TypeInteger},
			[]string{}},
		{"set not null", ColumnDiff{ColumnName: "n", OldType: "TEXT", NewType: TypeText, NewNotNull: true},
			[]string{`ALTER TABLE "t" ALTER COLUMN "n" SET NOT NULL;`}},
		{"drop not null", ColumnDiff{ColumnName: "n", OldType: "TEXT", NewType: TypeText, OldNotNull: true},
			[]string{`ALTER TABLE "t" ALTER COLUMN "n" DROP NOT NULL;`}},
		{"set default", ColumnDiff{ColumnName: "n", OldType: "TEXT", NewType: TypeText, NewDefault: "'x'"},
			[]string{`ALTER TABLE "t" ALTER COLUMN "n" SET DEFAULT 'x';`}},
		{"drop default", ColumnDiff{ColumnName: "n", OldType: "TEXT", NewType: TypeText, OldDefault: nullString("'x'")},
			[]string{`ALTER TABLE "t" ALTER COLUMN "n" DROP DEFAULT;`}},
		{"all three in a fixed order", ColumnDiff{ColumnName: "n", OldType: "INTEGER", NewType: TypeBigInt, NewNotNull: true, NewDefault: "0"},
			[]string{
				`ALTER TABLE "t" ALTER COLUMN "n" TYPE BIGINT;`,
				`ALTER TABLE "t" ALTER COLUMN "n" SET NOT NULL;`,
				`ALTER TABLE "t" ALTER COLUMN "n" SET DEFAULT 0;`,
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := generateAlterColumnSQL("t", tc.diff, pg)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestGenerateAlterColumnSQL_RefusesDialectsItCannotAlter(t *testing.T) {
	other := &otherDialect{}
	for name, diff := range map[string]ColumnDiff{
		"type":    {ColumnName: "n", OldType: "INTEGER", NewType: TypeBigInt},
		"null":    {ColumnName: "n", OldType: "TEXT", NewType: TypeText, NewNotNull: true},
		"default": {ColumnName: "n", OldType: "TEXT", NewType: TypeText, NewDefault: "'x'"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := generateAlterColumnSQL("t", diff, other)
			if !errors.Is(err, ErrUnsupportedOperation) {
				t.Fatalf("err = %v, want ErrUnsupportedOperation", err)
			}
		})
	}
}

func TestGenerateDropIndexSQL(t *testing.T) {
	got, err := generateDropIndexSQL("users", "idx_a", &PostgresDialect{})
	if err != nil || got != `DROP INDEX IF EXISTS "idx_a";` {
		t.Errorf("postgres: %q, %v", got, err)
	}
	// A dialect that scopes an index to its table names the table too.
	got, err = generateDropIndexSQL("users", "idx_a", &otherDialect{})
	if err != nil || got != `DROP INDEX IF EXISTS "idx_a" ON "users";` {
		t.Errorf("other dialect: %q, %v", got, err)
	}
}

func TestGenerateAlterSQL_RefusesDestructiveUnlessAllowed(t *testing.T) {
	diff := SchemaDiff{TableName: "t", ExtraColumns: []string{"old"}, ExtraIndexes: []string{"idx_old"}}
	if _, err := GenerateAlterSQL(diff, &PostgresDialect{}, false); !errors.Is(err, ErrDestructiveOperation) {
		t.Fatalf("err = %v, want ErrDestructiveOperation", err)
	}
	got, err := GenerateAlterSQL(diff, &PostgresDialect{}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`DROP INDEX IF EXISTS "idx_old";`, `ALTER TABLE "t" DROP COLUMN "old";`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
