// File: internal/cli/db_seed_project_pg_test.go
//
// `forge db ...` driven the way an agent drives it: through the ROOT command,
// with -C naming the project and the process CWD somewhere else entirely.
//
// A dogfood run found every project-relative input of the db commands read
// from the CWD instead of the -C project. `--dir` defaulted to the RELATIVE
// "db/migrations" — evaluated when the command tree was BUILT, before -C was
// even parsed — so `migrate up -C <proj>` from /tmp died on
// `open /tmp/db/migrations/.`, and `seed apply -C <proj>` found no
// migrations, no vocab.yaml and no db/seeds/custom, then printed
// "Seeded 0 row(s) across 0 table(s)" and exited 0 — on an EMPTY database.
//
// Gated behind testing.Short(): these need a real postgres (pgtest).

package cli

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const seedProjectMigration = `
CREATE TABLE crews (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    crew_id TEXT NOT NULL REFERENCES crews(id),
    title TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

const seedProjectMigrationFile = "20261001000000_create_crews_and_jobs.up.sql"

// seedTestProject lays down a complete project — forge.yaml, a dev env that
// declares declaredDSN, one migration, a vocab overlay and a custom SQL
// overlay — and returns its root. forgeYAMLExtra is appended to forge.yaml.
func seedTestProject(t *testing.T, declaredDSN, forgeYAMLExtra string) string {
	t.Helper()
	dir := devProject(t, declaredDSN)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"forge.yaml": "name: seedproj\nmodule_path: example.com/seedproj\n" + forgeYAMLExtra,
		"db/migrations/" + seedProjectMigrationFile: seedProjectMigration,
		"db/seeds/vocab.yaml":                       "columns:\n  crews.name: [From The Project Vocab]\n",
		"db/seeds/custom/10_marker.sql": `INSERT INTO crews (id, name) VALUES ('custom-marker', 'Custom Overlay Row')
ON CONFLICT (id) DO NOTHING;`,
	}
	for rel, body := range files {
		path := filepath.Join(resolved, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return resolved
}

// migrateByHand applies the project's migration to dsn and records it the way
// golang-migrate does, so a test can seed without the `migrate` CLI on PATH.
func migrateByHand(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{
		seedProjectMigration,
		`CREATE TABLE schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL)`,
		`INSERT INTO schema_migrations (version, dirty) VALUES (20261001000000, false)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("migrate by hand: %v", err)
		}
	}
	return db
}

// runForge executes the real root command — flag parsing, PersistentPreRunE
// and all — and returns its error plus everything it printed to stdout.
func runForgeDB(t *testing.T, args ...string) (string, error) {
	t.Helper()
	clearProjectDir(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	var cmdOut bytes.Buffer
	root := newTestRootCmd(t)
	root.SetOut(&cmdOut)
	root.SetErr(&cmdOut)
	root.SetArgs(args)
	runErr := root.ExecuteContext(context.Background())
	os.Stdout = stdout
	_ = w.Close()
	var captured bytes.Buffer
	_, _ = captured.ReadFrom(r)
	return captured.String() + cmdOut.String(), runErr
}

// elsewhere moves the test's CWD to a directory with no project in it.
func elsewhere(t *testing.T) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// The headline: `seed apply -C <proj>` from another directory reads THAT
// project's migrations, vocab.yaml and db/seeds/custom — and a second run is
// an idempotent success, not a "zero rows" error.
func TestDBSeedApply_DashCReadsEveryInputFromTheProject(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a real postgres server")
	}
	dsn := scratchDatabase(t)
	proj := seedTestProject(t, dsn, "")
	db := migrateByHand(t, dsn)
	elsewhere(t)

	out, err := runForgeDB(t, "-C", proj, "db", "seed", "apply")
	if err != nil {
		t.Fatalf("seed apply -C: %v\n%s", err, out)
	}
	if n := countRows(t, db, `SELECT count(*) FROM jobs`); n == 0 {
		t.Fatalf("seed apply -C seeded no jobs — it did not read %s/db/migrations:\n%s", proj, out)
	}
	if n := countRows(t, db, `SELECT count(*) FROM crews WHERE name = 'From The Project Vocab'`); n == 0 {
		t.Errorf("no crew carries the project's vocab.yaml value — vocab was read from the CWD:\n%s", out)
	}
	if n := countRows(t, db, `SELECT count(*) FROM crews WHERE id = 'custom-marker'`); n != 1 {
		t.Errorf("db/seeds/custom/10_marker.sql was not applied (marker rows = %d):\n%s", n, out)
	}
	// Synthesized timestamps sit around the day the seed ran, not in 2024.
	if n := countRows(t, db, `SELECT count(*) FROM jobs WHERE created_at < now() - interval '60 days'`); n != 0 {
		t.Errorf("%d job(s) carry a created_at more than 60 days old; seeded dates must be anchored to now", n)
	}

	// Re-running inserts nothing new — that is idempotency, not a failure.
	if out, err := runForgeDB(t, "-C", proj, "db", "seed", "apply"); err != nil {
		t.Fatalf("a second seed apply over an already-seeded database must succeed: %v\n%s", err, out)
	}
}

// `migrate up -C <proj>` from another directory applies THAT project's
// migrations.
func TestDBMigrateUp_DashCReadsTheProjectMigrations(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a real postgres server")
	}
	dsn := scratchDatabase(t)
	proj := seedTestProject(t, dsn, "")
	elsewhere(t)

	out, err := runForgeDB(t, "-C", proj, "db", "migrate", "up", "--dsn", dsn)
	if err != nil {
		t.Fatalf("migrate up -C: %v\n%s", err, out)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if n := countRows(t, db, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'jobs'`); n != 1 {
		t.Errorf("migrate up -C did not create the project's tables:\n%s", out)
	}
}

// Seeding a database that HAS tables while the migrations forge read define
// none is a misconfiguration (the wrong directory), never a success.
func TestDBSeedApply_ZeroRowsIntoAnEmptyDatabaseIsAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a real postgres server")
	}
	dsn := scratchDatabase(t)
	proj := seedTestProject(t, dsn, "")
	db := migrateByHand(t, dsn)
	emptyMigrations := filepath.Join(proj, "db", "no-migrations-here")
	if err := os.MkdirAll(emptyMigrations, 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runForgeDB(t, "-C", proj, "db", "seed", "apply", "--dir", emptyMigrations)
	if err == nil {
		t.Fatalf("seed apply wrote nothing into a database with tables and still exited 0:\n%s", out)
	}
	if !strings.Contains(err.Error(), emptyMigrations) {
		t.Errorf("the error must name the migrations directory it read (%s); got:\n%v", emptyMigrations, err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM jobs`); n != 0 {
		t.Errorf("jobs has %d rows; nothing should have been seeded", n)
	}
}

// --dsn naming a LOOPBACK database the env does not declare — a throwaway
// postgres on another port — is accepted: there was no supported way to
// seed one, and port 5432 is usually taken.
func TestDBSeedApply_AcceptsAnyLoopbackDSN(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a real postgres server")
	}
	dsn := scratchDatabase(t)
	proj := seedTestProject(t, "postgres://postgres:postgres@localhost:5999/the-dev-db?sslmode=disable", "")
	db := migrateByHand(t, dsn)

	out, err := runForgeDB(t, "-C", proj, "db", "seed", "apply", "--dsn", dsn)
	if err != nil {
		t.Fatalf("seed apply --dsn <loopback throwaway>: %v\n%s", err, out)
	}
	if n := countRows(t, db, `SELECT count(*) FROM jobs`); n == 0 {
		t.Errorf("the throwaway database got no rows:\n%s", out)
	}
}

// A NON-loopback --dsn is still refused unless the env declares it, and the
// refusal names the override. With the override, the DSN gate opens (the
// command then fails only because the address is unreachable).
func TestDBSeedApply_RefusesANonLoopbackDSNWithoutTheOverride(t *testing.T) {
	proj := seedTestProject(t, "postgres://postgres:postgres@localhost:5999/the-dev-db?sslmode=disable", "")
	// 192.0.2.0/24 is TEST-NET-1: never routable, never anyone's database.
	const remote = "postgres://app:hunter2@192.0.2.10:5432/app?sslmode=disable&connect_timeout=1"

	out, err := runForgeDB(t, "-C", proj, "db", "seed", "apply", "--dsn", remote)
	if err == nil {
		t.Fatalf("seed apply accepted a non-loopback DSN with no override:\n%s", out)
	}
	for _, want := range []string{"loopback", "--allow-remote-dsn"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must mention %q; got:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("refusal leaked a password:\n%v", err)
	}

	start := time.Now()
	_, err = runForgeDB(t, "-C", proj, "db", "seed", "apply", "--dsn", remote, "--allow-remote-dsn")
	if err == nil {
		t.Fatal("seeding an unreachable address cannot succeed")
	}
	if strings.Contains(err.Error(), "refusing") {
		t.Errorf("--allow-remote-dsn must open the DSN gate; still refused:\n%v", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Errorf("connect attempt took %s; connect_timeout=1 should bound it", time.Since(start))
	}
}
