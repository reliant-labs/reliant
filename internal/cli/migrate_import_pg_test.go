// File: internal/cli/migrate_import_pg_test.go
//
// `forge project migrate import --from goose` output, applied by forge's own
// migrator against a REAL postgres. The unit tests pin what the import writes;
// this pins that what it writes RUNS, which is the only property a user of the
// import cares about.
//
// Gated behind testing.Short(): it needs a real postgres (pgtest).

package cli

import (
	"bytes"
	"testing"
)

// The P5a regression, end to end. reliant's goose history had NO TRANSACTION
// files holding several CREATE INDEX CONCURRENTLY statements. Imported whole,
// each failed under `migrate up` with SQLSTATE 25001 — golang-migrate sends a
// file as one multi-statement query, which postgres runs as one implicit
// transaction — and left the schema dirty. The enum case is the other reason a
// goose file declares NO TRANSACTION: a value ADDed in a transaction cannot be
// used before it commits (SQLSTATE 55P04).
func TestMigrateImport_NoTransactionFileAppliesWithForgesMigrator(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a real postgres server")
	}
	src := writeGooseSrc(t, []gooseFile{
		{
			Name: "20240501_add_users.sql",
			Content: `-- +goose Up
CREATE TYPE user_state AS ENUM ('active');
CREATE TABLE users (id INT PRIMARY KEY, email TEXT, org_id INT, state user_state NOT NULL DEFAULT 'active');
INSERT INTO users (id, email, org_id) VALUES (1, 'a@example.com', 7);
-- +goose Down
DROP TABLE users;
`,
		},
		{
			Name: "20240502_index_users.sql",
			Content: `-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY idx_users_email ON users (email);
CREATE INDEX CONCURRENTLY idx_users_org ON users (org_id);
ALTER TYPE user_state ADD VALUE 'suspended';
UPDATE users SET state = 'suspended' WHERE id = 1;
-- +goose Down
DROP INDEX CONCURRENTLY idx_users_org;
DROP INDEX CONCURRENTLY idx_users_email;
`,
		},
	})
	dest := t.TempDir()
	var out bytes.Buffer
	if err := runMigrateImport(migrateImportOptions{From: "goose", SrcDir: src, DestDir: dest, Stdout: &out}); err != nil {
		t.Fatalf("import: %v\n%s", err, out.String())
	}

	dsn := scratchDatabase(t)
	out.Reset()
	if err := runMigrateUp(&out, dsn, dest); err != nil {
		t.Fatalf("migrate up over the imported goose history: %v\n%s", err, out.String())
	}

	db := openScratch(t, dsn)
	if n := countRows(t, db, `SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname IN ('idx_users_email', 'idx_users_org') AND i.indisvalid`); n != 2 {
		t.Errorf("valid CONCURRENTLY indexes = %d, want 2", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM users WHERE state = 'suspended'`); n != 1 {
		t.Errorf("the row using the added enum value was not updated (count = %d)", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM schema_migrations WHERE NOT dirty`); n != 1 {
		t.Error("schema_migrations is dirty or empty after the run")
	}
}
