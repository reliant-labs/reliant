// File: internal/cli/db_reset_pg_test.go
//
// `forge db reset` against a REAL postgres. The unit tests pin the guards;
// this one pins the thing the guards are protecting — that reset genuinely
// drops the database and rebuilds it to a fully-migrated, seeded state.
//
// The headline case is the deadlock from the dogfood run, reproduced exactly:
// seed placeholder rows into an unconstrained column, then add the foreign key
// that column always semantically had. `migrate up` fails part-way and marks
// the database dirty; `seed reset` refuses because the schema is behind;
// `migrate up` refuses because the seeded rows violate the new constraint. The
// test asserts that loop is real, and that reset exits it.
//
// Gated behind testing.Short() — it needs a server.

package cli

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// The two migrations that produce the deadlock.
//
// 1 creates the org spine the way forge actually generated it in the dogfood
// run: memberships.org_id is a bare TEXT column with no REFERENCES, because
// the stem "org" does not resolve to the entity "Organization".
//
// 2 is the ordinary act of tightening the schema during the birth window —
// adding the FK that column always meant. It is the migration that cannot
// apply over placeholder seed rows.
const resetMigration1 = `
CREATE TABLE organizations (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE TABLE memberships (
    id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('owner', 'member'))
);
`

const resetMigration2 = `
ALTER TABLE memberships
    ADD CONSTRAINT memberships_org_id_fkey FOREIGN KEY (org_id) REFERENCES organizations (id);
`

// writeResetMigrations lays down a golang-migrate-shaped migrations tree and
// returns its path.
func writeResetMigrations(t *testing.T, projectDir string) string {
	t.Helper()
	dir := filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"000001_org_spine.up.sql":  resetMigration1,
		"000002_add_org_fk.up.sql": resetMigration2,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// scratchDatabase creates a throwaway database on the shared test server and
// returns its DSN. Named distinctly by pgtest and dropped by name on cleanup —
// it must never be confused with a dogfood run's real database.
func scratchDatabase(t *testing.T) string {
	t.Helper()
	url, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Skipf("no postgres available: %v", err)
	}
	t.Cleanup(cleanup)
	return url
}

// devProject builds a temp project that reset treats exactly as it treats a
// real one: deploy/kcl/dev/config.k marks it development (what the dev gate
// reads), and a KCL render fixture declares declaredDSN as the environment's
// DATABASE_URL (what the DSN reconciliation reads).
//
// Both go through the PRODUCTION path. An earlier draft gave runDBReset
// test-only fields to bypass these two gates, and forge's dead-code guard
// rejected it — the tests would have been green on a shape production cannot
// produce, in front of a DROP DATABASE. FORGE_KCL_RENDER_FIXTURE is the seam
// RenderKCL already documents for exactly this.
func devProject(t *testing.T, declaredDSN string) string {
	t.Helper()
	dir := t.TempDir()
	writeEnvConfigK(t, dir, "dev", `environment = "development"`)

	// RenderKCL needs deploy/kcl/dev/ to exist; writeEnvConfigK made it.
	fixture := filepath.Join(dir, "render.json")
	bundle := `{"output":{"workloads":[{"name":"api","kind":"service","runtime":{"type":"host"},"spec":{"kind":"service","env":[{"name":"DATABASE_URL","value":"` + declaredDSN + `"}]}}]}}`
	if err := os.WriteFile(fixture, []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", fixture)
	// The reconciliation must not read an ambient DATABASE_URL as evidence
	// about itself; clear it so the test is not at the mercy of the shell.
	t.Setenv("DATABASE_URL", "")
	return dir
}

// The headline property: reset drops the database and rebuilds it to a fully
// migrated, seeded state — from a database that is WEDGED, which is the state
// nothing else can exit.
func TestDBReset_ExitsTheDeadlockAndRebuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a real postgres server")
	}
	ctx := context.Background()
	dsn := scratchDatabase(t)
	// The env declares the very database we are resetting — the good path.
	projectDir := devProject(t, dsn)
	migDir := writeResetMigrations(t, projectDir)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// Reproduce the wedge: apply migration 1, insert placeholder rows of the
	// shape `forge db seed apply` synthesizes for an unconstrained column,
	// then try to add the FK.
	if _, err := db.ExecContext(ctx, resetMigration1); err != nil {
		t.Fatalf("apply migration 1: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO memberships (id, org_id, role) VALUES ('m1', 'sample_org_id_14', 'owner')`); err != nil {
		t.Fatalf("seed placeholder row: %v", err)
	}
	// This is the refusal that starts the cycle. It must really happen —
	// otherwise the rest of the test proves nothing.
	if _, err := db.ExecContext(ctx, resetMigration2); err == nil {
		t.Fatal("adding the FK over placeholder rows SUCCEEDED; the deadlock premise no longer holds and this test is not testing what it claims")
	}

	// Now the one command that discards the state rather than reasoning about
	// it. --yes because there is no terminal here.
	if err := runDBReset(ctx, resetOptions{
		dsn:        dsn,
		env:        "dev",
		migDir:     migDir,
		yes:        true,
		projectDir: projectDir,
	}); err != nil {
		t.Fatalf("forge db reset: %v", err)
	}

	// The database exists, is migrated to head (the FK is present), and the
	// placeholder row is gone.
	fresh, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()

	var fkCount int
	if err := fresh.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_constraint WHERE conname = 'memberships_org_id_fkey'`).Scan(&fkCount); err != nil {
		t.Fatalf("check FK: %v", err)
	}
	if fkCount != 1 {
		t.Errorf("after reset the FK from migration 2 is absent (count=%d); reset did not migrate to head", fkCount)
	}

	var placeholders int
	if err := fresh.QueryRowContext(ctx,
		`SELECT count(*) FROM memberships WHERE org_id = 'sample_org_id_14'`).Scan(&placeholders); err != nil {
		t.Fatalf("check placeholder rows: %v", err)
	}
	if placeholders != 0 {
		t.Errorf("the placeholder row survived reset (count=%d); the database was not dropped", placeholders)
	}

	// And the migration state is CLEAN — not dirty, not forced. This is the
	// half `migrate force` could never give you honestly.
	var dirty bool
	if err := fresh.QueryRowContext(ctx, `SELECT dirty FROM schema_migrations LIMIT 1`).Scan(&dirty); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if dirty {
		t.Error("after reset the database is still marked dirty; reset must leave a clean migration state")
	}
}

// reset must refuse a DSN it cannot reconcile with the env being claimed,
// BEFORE it drops anything. This is the destructive-path version of the
// env/DSN hole, and it is the most important refusal in the command.
func TestDBReset_RefusesAnUnreconcilableDSNBeforeDropping(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a real postgres server")
	}
	ctx := context.Background()
	dsn := scratchDatabase(t)
	// The env declares a DIFFERENT database than the DSN we hand reset. This
	// is the reported hole in its most dangerous form: the env IS dev, so the
	// dev gate opens, and only the DSN reconciliation stands between the
	// command and a DROP DATABASE on an unverified target.
	projectDir := devProject(t, "postgres://postgres:postgres@localhost:5999/some-other-project?sslmode=disable")
	migDir := writeResetMigrations(t, projectDir)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, resetMigration1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO organizations (id, name) VALUES ('o1', 'canary')`); err != nil {
		t.Fatal(err)
	}

	err = runDBReset(ctx, resetOptions{
		dsn:        dsn,
		env:        "dev",
		migDir:     migDir,
		yes:        true,
		projectDir: projectDir,
	})
	if err == nil {
		t.Fatal("reset proceeded with a DSN the env does not declare; that is a DROP DATABASE against an unverified target")
	}

	// The canary row proves nothing was dropped. A refusal that happens
	// AFTER the drop is not a refusal.
	var canary int
	if qerr := db.QueryRowContext(ctx,
		`SELECT count(*) FROM organizations WHERE id = 'o1'`).Scan(&canary); qerr != nil {
		t.Fatalf("the database was dropped despite the refusal: %v", qerr)
	}
	if canary != 1 {
		t.Errorf("canary row count = %d, want 1 — data was destroyed by a command that refused", canary)
	}
}
