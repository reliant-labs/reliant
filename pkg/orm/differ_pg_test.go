package orm

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// These tests drive DiffDatabase — and the MigrationManager paths built on
// it — against a REAL postgres: what the differ reports is whatever the
// catalog introspection reads back, and only the database can say what that
// is. (This replaced an integration-tagged test that asked for a sqlite
// dialect the ORM never registers, so it always skipped: forge#2054.)

// newDiffTestClient returns a client on a fresh pgtest database holding a
// `users` table with one secondary index.
func newDiffTestClient(t *testing.T) *Client {
	t.Helper()
	if testing.Short() {
		t.Skip("boots postgres; skipped under -short")
	}
	sqldb, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	client, err := NewClientWithDB(sqldb, "postgres")
	if err != nil {
		t.Fatalf("NewClientWithDB: %v", err)
	}
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, name TEXT)`,
		`CREATE INDEX idx_users_email ON users (email)`,
	} {
		if _, err := client.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return client
}

// usersNow is the `users` table exactly as newDiffTestClient creates it.
func usersNow() TableSchema {
	return TableSchema{
		Name: "users",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeInteger, PrimaryKey: true, NotNull: true},
			{Name: "email", Type: TypeText, NotNull: true},
			{Name: "name", Type: TypeText},
		},
		Indexes: []IndexSchema{
			{Name: "idx_users_email", Fields: []string{"email"}},
		},
	}
}

// usersNext is usersNow plus two columns and an index.
func usersNext() TableSchema {
	s := usersNow()
	s.Fields = append(s.Fields,
		FieldSchema{Name: "age", Type: TypeInteger},
		FieldSchema{Name: "created_at", Type: TypeTimestampTZ},
	)
	s.Indexes = append(s.Indexes, IndexSchema{Name: "idx_users_name", Fields: []string{"name"}})
	return s
}

// postsNew is a table the database does not have yet.
func postsNew() TableSchema {
	return TableSchema{
		Name: "posts",
		Fields: []FieldSchema{
			{Name: "id", Type: TypeBigInt, PrimaryKey: true, NotNull: true},
			{Name: "author_id", Type: TypeInteger, NotNull: true},
			{Name: "body", Type: TypeText},
		},
		Indexes: []IndexSchema{
			{Name: "idx_posts_author", Fields: []string{"author_id"}},
		},
	}
}

func fieldNames(fields []FieldSchema) []string {
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	return names
}

func indexNames(indexes []IndexSchema) []string {
	names := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		names = append(names, idx.Name)
	}
	slices.Sort(names)
	return names
}

func TestDiffDatabase(t *testing.T) {
	ctx := context.Background()
	client := newDiffTestClient(t)

	t.Run("table in sync", func(t *testing.T) {
		diffs, err := DiffDatabase(ctx, client, client.Dialect(), []TableSchema{usersNow()})
		if err != nil {
			t.Fatalf("DiffDatabase: %v", err)
		}
		if len(diffs) != 0 {
			t.Fatalf("diffs = %+v, want none: the expected schema is the table as created", diffs)
		}
	})

	t.Run("added columns and index", func(t *testing.T) {
		diffs, err := DiffDatabase(ctx, client, client.Dialect(), []TableSchema{usersNext()})
		if err != nil {
			t.Fatalf("DiffDatabase: %v", err)
		}
		if len(diffs) != 1 {
			t.Fatalf("got %d diffs, want 1: %+v", len(diffs), diffs)
		}
		d := diffs[0]
		if d.TableName != "users" {
			t.Errorf("TableName = %q, want users", d.TableName)
		}
		if got, want := fieldNames(d.MissingColumns), []string{"age", "created_at"}; !slices.Equal(got, want) {
			t.Errorf("MissingColumns = %v, want %v", got, want)
		}
		if got, want := indexNames(d.MissingIndexes), []string{"idx_users_name"}; !slices.Equal(got, want) {
			t.Errorf("MissingIndexes = %v, want %v", got, want)
		}
		if len(d.ModifiedColumns) != 0 || len(d.ExtraColumns) != 0 || len(d.ExtraIndexes) != 0 {
			t.Errorf("unexpected changes: modified=%+v extraColumns=%v extraIndexes=%v",
				d.ModifiedColumns, d.ExtraColumns, d.ExtraIndexes)
		}
		if d.IsDestructive() {
			t.Error("an additive diff reports itself destructive")
		}
	})

	t.Run("dropped column and index", func(t *testing.T) {
		s := usersNow()
		s.Fields = s.Fields[:2] // no `name`
		s.Indexes = nil         // no idx_users_email
		diffs, err := DiffDatabase(ctx, client, client.Dialect(), []TableSchema{s})
		if err != nil {
			t.Fatalf("DiffDatabase: %v", err)
		}
		if len(diffs) != 1 {
			t.Fatalf("got %d diffs, want 1: %+v", len(diffs), diffs)
		}
		d := diffs[0]
		if !slices.Equal(d.ExtraColumns, []string{"name"}) || !slices.Equal(d.ExtraIndexes, []string{"idx_users_email"}) {
			t.Errorf("extra columns/indexes = %v/%v, want [name]/[idx_users_email]", d.ExtraColumns, d.ExtraIndexes)
		}
		if !d.IsDestructive() {
			t.Error("a diff that drops a column does not report itself destructive")
		}
	})

	t.Run("non-existent table", func(t *testing.T) {
		diffs, err := DiffDatabase(ctx, client, client.Dialect(), []TableSchema{postsNew()})
		if err != nil {
			t.Fatalf("DiffDatabase: %v", err)
		}
		if len(diffs) != 1 {
			t.Fatalf("got %d diffs, want 1: %+v", len(diffs), diffs)
		}
		if got, want := fieldNames(diffs[0].MissingColumns), []string{"author_id", "body", "id"}; !slices.Equal(got, want) {
			t.Errorf("MissingColumns = %v, want every column %v", got, want)
		}
		if got, want := indexNames(diffs[0].MissingIndexes), []string{"idx_posts_author"}; !slices.Equal(got, want) {
			t.Errorf("MissingIndexes = %v, want %v", got, want)
		}
	})
}

// TestMigrationManagerConvergesOnDiffDatabase pins the migration path built on
// DiffDatabase: PlanMigration prints the statements the diff calls for,
// AutoMigrate applies them (an ALTER for a table that exists, a CREATE for one
// that does not), and the database then diffs clean against the same schemas.
func TestMigrationManagerConvergesOnDiffDatabase(t *testing.T) {
	ctx := context.Background()
	client := newDiffTestClient(t)
	mgr := NewMigrationManager(client)
	schemas := []TableSchema{usersNext(), postsNew()}

	plan, err := mgr.PlanMigration(ctx, schemas)
	if err != nil {
		t.Fatalf("PlanMigration: %v", err)
	}
	for _, want := range []string{
		`ALTER TABLE "users" ADD COLUMN "age" INTEGER`,
		`ALTER TABLE "users" ADD COLUMN "created_at" TIMESTAMPTZ`,
		`CREATE INDEX IF NOT EXISTS "idx_users_name" ON "users" ("name")`,
		`CREATE TABLE IF NOT EXISTS "posts"`,
		`CREATE INDEX IF NOT EXISTS "idx_posts_author" ON "posts" ("author_id")`,
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan is missing %q:\n%s", want, plan)
		}
	}

	if err := mgr.AutoMigrate(ctx, schemas); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	diffs, err := DiffDatabase(ctx, client, client.Dialect(), schemas)
	if err != nil {
		t.Fatalf("DiffDatabase after AutoMigrate: %v", err)
	}
	if len(diffs) != 0 {
		t.Fatalf("diffs after AutoMigrate = %+v, want none", diffs)
	}
	plan, err = mgr.PlanMigration(ctx, schemas)
	if err != nil {
		t.Fatalf("PlanMigration after AutoMigrate: %v", err)
	}
	if plan != "-- No schema changes detected\n" {
		t.Errorf("plan after AutoMigrate = %q, want no changes", plan)
	}

	// The migration is recorded, so it is visible to Status.
	var recorded int
	if err := client.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version LIKE 'auto_%'`).Scan(&recorded); err != nil {
		t.Fatalf("count recorded auto-migrations: %v", err)
	}
	if recorded != 1 {
		t.Errorf("recorded auto-migrations = %d, want 1", recorded)
	}
}
