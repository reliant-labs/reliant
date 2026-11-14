package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/shadowdb"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// fakeMigrationToolEnv makes this test binary act as a project's migration
// tool: the hook execs it, and TestMain hands control to fakeMigrationTool.
const fakeMigrationToolEnv = "FORGE_CLI_FAKE_MIGRATION_TOOL"

// fakeMigrationTool applies every <version>_*.up.sql in
// $FORGE_MIGRATIONS_DIR above the version its ledger records, in version
// order, to $DATABASE_URL, the way golang-migrate does: the ledger is the
// table an `x-migrations-table=` argument names (else schema_migrations), one
// row holding the current version, marked dirty while that version runs.
//
// Given the argument "flyway", it is flyway-shaped instead (fakeFlyway).
func fakeMigrationTool() int {
	ledger := "schema_migrations"
	flyway := false
	for _, a := range os.Args[1:] {
		if _, v, ok := strings.Cut(a, "x-migrations-table="); ok {
			ledger, _, _ = strings.Cut(v, "&")
		}
		flyway = flyway || a == "flyway"
	}
	db, err := sql.Open(pgtest.DriverName, os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer func() { _ = db.Close() }()
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "fake tool:", err)
		return 1
	}
	if flyway {
		if err := fakeFlyway(db); err != nil {
			return fail(err)
		}
		return 0
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ` + ledger + ` (version bigint PRIMARY KEY, dirty boolean NOT NULL)`); err != nil {
		return fail(err)
	}
	var current int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM ` + ledger).Scan(&current); err != nil {
		return fail(err)
	}
	setVersion := func(v int64, dirty bool) error {
		_, err := db.Exec(`TRUNCATE ` + ledger + `; INSERT INTO ` + ledger + ` VALUES (` + strconv.FormatInt(v, 10) + `, ` + strconv.FormatBool(dirty) + `)`)
		return err
	}
	files, _ := filepath.Glob(filepath.Join(os.Getenv("FORGE_MIGRATIONS_DIR"), "*.up.sql"))
	versionOf := func(f string) int64 {
		n, _ := strconv.ParseInt(strings.SplitN(filepath.Base(f), "_", 2)[0], 10, 64)
		return n
	}
	sort.Slice(files, func(i, j int) bool { return versionOf(files[i]) < versionOf(files[j]) })
	for _, f := range files {
		v := versionOf(f)
		if v <= current {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			return fail(err)
		}
		if err := setVersion(v, true); err != nil {
			return fail(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			return fail(fmt.Errorf("%s: %w", filepath.Base(f), err))
		}
		if err := setVersion(v, false); err != nil {
			return fail(err)
		}
	}
	return 0
}

// flywayHistory is the part of flyway's flyway_schema_history that
// migrationhook reads.
const flywayHistory = `CREATE TABLE IF NOT EXISTS flyway_schema_history (installed_rank int PRIMARY KEY, version varchar(50), type varchar(20) NOT NULL, script varchar(1000) NOT NULL, checksum int, success boolean NOT NULL)`

// flywayChecksum is the checksum fakeFlyway records for a script's content.
func flywayChecksum(body string) int32 { return int32(crc32.ChecksumIEEE([]byte(body))) }

// fakeFlyway migrates the way flyway does: every V<version>__*.sql not yet
// recorded, in version order, then every R__*.sql whose content differs from
// its last successful run, each recorded with its checksum.
func fakeFlyway(db *sql.DB) error {
	if _, err := db.Exec(flywayHistory); err != nil {
		return err
	}
	dir := os.Getenv("FORGE_MIGRATIONS_DIR")
	versioned, _ := filepath.Glob(filepath.Join(dir, "V*__*.sql"))
	repeatable, _ := filepath.Glob(filepath.Join(dir, "R__*.sql"))
	sort.Strings(versioned)
	sort.Strings(repeatable)
	run := func(f, version string, skip func(sum int32) (bool, error)) error {
		body, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		sum := flywayChecksum(string(body))
		if done, err := skip(sum); err != nil || done {
			return err
		}
		if _, err := db.Exec(string(body)); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		var v any
		if version != "" {
			v = version
		}
		_, err = db.Exec(`INSERT INTO flyway_schema_history SELECT COALESCE(MAX(installed_rank), 0) + 1, $1, 'SQL', $2, $3, true FROM flyway_schema_history`, v, filepath.Base(f), sum)
		return err
	}
	for _, f := range versioned {
		version := strings.TrimPrefix(strings.SplitN(filepath.Base(f), "__", 2)[0], "V")
		if err := run(f, version, func(int32) (bool, error) {
			var n int
			err := db.QueryRow(`SELECT COUNT(*) FROM flyway_schema_history WHERE version = $1 AND success`, version).Scan(&n)
			return n > 0, err
		}); err != nil {
			return err
		}
	}
	for _, f := range repeatable {
		if err := run(f, "", func(sum int32) (bool, error) {
			var last sql.NullInt32
			err := db.QueryRow(`SELECT checksum FROM flyway_schema_history WHERE version IS NULL AND script = $1 AND success ORDER BY installed_rank DESC LIMIT 1`, filepath.Base(f)).Scan(&last)
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return last.Valid && last.Int32 == sum, err
		}); err != nil {
			return err
		}
	}
	return nil
}

// hookedSeedProject writes a project whose migrations dir holds files, with
// migrations as the body of forge.yaml's `database.migrations:` block, and
// returns the project and migrations directories. The shadow database the
// tool migrates is a fresh pgtest server's.
func hookedSeedProject(t *testing.T, migrations string, files map[string]string) (dir, migDir string) {
	t.Helper()
	shadowURL, cleanShadow, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanShadow)
	t.Setenv(pgtest.EnvBaseURL, shadowURL)
	t.Setenv(fakeMigrationToolEnv, "1")

	dir = t.TempDir()
	migDir = filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(migDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	forgeYAML := "name: p\nmodule_path: example.com/p\ndatabase:\n  migrations:\n    dir: migrations\n" + migrations
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(forgeYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, migDir
}

// seedTarget is a fresh database with stmts applied, and its URL.
func seedTarget(t *testing.T, stmts ...string) (*sql.DB, string) {
	t.Helper()
	targetURL, cleanTarget, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanTarget)
	db, err := sql.Open(pgtest.DriverName, targetURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range stmts {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db, targetURL
}

func testExecutable(t *testing.T) string {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return self
}

// A golang-migrate project with a custom ledger (x-migrations-table), fully
// migrated, has no forge schema_migrations table. Seeding it is not refused,
// apply and reset leave the tool's history untouched, and a database that is
// behind its migrations is still refused: by the tool's ledger when the schema
// matches (a pending data-only migration, a migration that failed after its
// DDL), and by the schema when a table or column is missing.
func TestSeedOfAHookedProjectWithACustomLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("applies migrations to a real postgres through a subprocess; skipped under -short")
	}
	ctx := context.Background()
	widgets := `CREATE TABLE widgets (id TEXT PRIMARY KEY, label TEXT NOT NULL)`
	dir, migDir := hookedSeedProject(t,
		fmt.Sprintf("    tool: golang-migrate\n    apply: [%q, \"-database\", \"postgres://h/db?x-migrations-table=app_migrations\", \"up\"]\n", testExecutable(t)),
		map[string]string{"1_widgets.up.sql": widgets})

	// The target, migrated by the tool: its ledger holds its history, and
	// there is no schema_migrations.
	db, dsn := seedTarget(t, widgets,
		`CREATE TABLE app_migrations (version bigint PRIMARY KEY, dirty boolean NOT NULL)`,
		`INSERT INTO app_migrations VALUES (1, false)`)

	if block, err := seedMigrationsPending(ctx, db, dsn, dir, migDir); err != nil || block != nil {
		t.Fatalf("a fully migrated hooked database is refused: %+v, %v", block, err)
	}
	tables, err := shadowdb.Tables(dir, migDir)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := seedplan.BuildLivePlanFromTables(ctx, db, tables, migDir, seedplan.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedplan.Apply(ctx, db, plan); err != nil {
		t.Fatalf("seed apply: %v", err)
	}
	if _, err := seedplan.Reset(ctx, db, plan); err != nil {
		t.Fatalf("seed reset: %v", err)
	}
	var history string
	if err := db.QueryRowContext(ctx, `SELECT string_agg(version::text || ':' || dirty::text, ',' ORDER BY version) FROM app_migrations`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != "1:false" {
		t.Errorf("the tool's history after seed apply and reset = %q, want it untouched (1:false)", history)
	}
	var widgetRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM widgets`).Scan(&widgetRows); err != nil || widgetRows == 0 {
		t.Errorf("seeding planned no widgets rows (%d, %v): the plan checked nothing", widgetRows, err)
	}

	// A pending data-only migration leaves the schema exactly as it was:
	// the ledger is what says the database is behind.
	if err := os.WriteFile(filepath.Join(migDir, "2_backfill.up.sql"), []byte(`UPDATE widgets SET label = upper(label)`), 0o644); err != nil {
		t.Fatal(err)
	}
	block, err := seedMigrationsPending(ctx, db, dsn, dir, migDir)
	if err != nil || block == nil || !strings.Contains(block.Reason, "app_migrations has not recorded migration 2") {
		t.Fatalf("a database behind a data-only migration: block %+v, err %v; want a refusal naming migration 2", block, err)
	}

	// A migration that failed part-way (golang-migrate's dirty flag) is
	// refused even though its DDL may already be in place.
	if _, err := db.ExecContext(ctx, `UPDATE app_migrations SET version = 2, dirty = true`); err != nil {
		t.Fatal(err)
	}
	block, err = seedMigrationsPending(ctx, db, dsn, dir, migDir)
	if err == nil || block != nil || !strings.Contains(err.Error(), "migration 2 as failed part-way") {
		t.Fatalf("a dirty database: block %+v, err %v; want a refusal naming the failed migration 2", block, err)
	}

	// Caught up again, it is seedable.
	if _, err := db.ExecContext(ctx, `UPDATE app_migrations SET dirty = false`); err != nil {
		t.Fatal(err)
	}
	if block, err := seedMigrationsPending(ctx, db, dsn, dir, migDir); err != nil || block != nil {
		t.Fatalf("a caught-up hooked database is refused: %+v, %v", block, err)
	}

	// A ledger that claims every migration over a schema missing a column is
	// still refused, by the schema.
	if _, err := db.ExecContext(ctx, `ALTER TABLE widgets DROP COLUMN label`); err != nil {
		t.Fatal(err)
	}
	block, err = seedMigrationsPending(ctx, db, dsn, dir, migDir)
	if err != nil || block == nil || !strings.Contains(block.Reason, "column public.widgets.label") {
		t.Fatalf("a database behind its migrations: block %+v, err %v; want a refusal naming public.widgets.label", block, err)
	}
}

// A custom tool has no ledger forge can read, so seeding asks its declared
// `check:` argv, and refuses outright when it declares none.
func TestSeedOfAHookedProjectWithACustomTool(t *testing.T) {
	if testing.Short() {
		t.Skip("applies migrations to a real postgres through a subprocess; skipped under -short")
	}
	ctx := context.Background()
	widgets := `CREATE TABLE widgets (id TEXT PRIMARY KEY, label TEXT NOT NULL)`
	apply := fmt.Sprintf("    tool: custom\n    apply: [%q, \"up\"]\n", testExecutable(t))
	files := map[string]string{"1_widgets.up.sql": widgets}

	dir, migDir := hookedSeedProject(t, apply, files)
	db, dsn := seedTarget(t, widgets)
	_, err := seedMigrationsPending(ctx, db, dsn, dir, migDir)
	if err == nil || !strings.Contains(err.Error(), "database.migrations.check") {
		t.Fatalf("a custom tool without check: err %v; want a refusal asking for database.migrations.check", err)
	}

	dir, migDir = hookedSeedProject(t, apply+"    check: [sh, -c, \"exit 0\"]\n", files)
	if block, err := seedMigrationsPending(ctx, db, dsn, dir, migDir); err != nil || block != nil {
		t.Fatalf("a custom tool whose check passes is refused: %+v, %v", block, err)
	}

	dir, migDir = hookedSeedProject(t, apply+"    check: [sh, -c, \"exit 3\"]\n", files)
	block, err := seedMigrationsPending(ctx, db, dsn, dir, migDir)
	if err != nil || block == nil || !strings.Contains(block.Reason, "not fully migrated") {
		t.Fatalf("a custom tool whose check fails: block %+v, err %v; want a refusal", block, err)
	}
}

// A flyway project's repeatable migrations (R__*.sql) have no version: flyway
// re-runs one whenever its content changes, and records each run's checksum.
// A database whose repeatable last ran at other content, or whose last run of
// it failed, is behind or broken however current its versioned history and
// its schema look, so seeding refuses it.
func TestSeedOfAFlywayProjectChecksItsRepeatables(t *testing.T) {
	if testing.Short() {
		t.Skip("applies migrations to a real postgres through a subprocess; skipped under -short")
	}
	ctx := context.Background()
	widgets := `CREATE TABLE widgets (id TEXT PRIMARY KEY, label TEXT NOT NULL)`
	labels := `UPDATE widgets SET label = upper(label)`
	dir, migDir := hookedSeedProject(t,
		fmt.Sprintf("    tool: flyway\n    apply: [%q, \"flyway\"]\n", testExecutable(t)),
		map[string]string{"V1__widgets.sql": widgets, "R__labels.sql": labels})
	history := func(stmts ...string) []string {
		return append([]string{widgets, flywayHistory}, stmts...)
	}
	current := fmt.Sprintf(`INSERT INTO flyway_schema_history VALUES (1, '1', 'SQL', 'V1__widgets.sql', %d, true), (2, NULL, 'SQL', 'R__labels.sql', %d, true)`,
		flywayChecksum(widgets), flywayChecksum(labels))

	db, dsn := seedTarget(t, history(current)...)
	if block, err := seedMigrationsPending(ctx, db, dsn, dir, migDir); err != nil || block != nil {
		t.Fatalf("a fully migrated flyway database is refused: %+v, %v", block, err)
	}

	// The repeatable last ran at other content (it changed since; a
	// data-only change leaves the schema as it was).
	db, dsn = seedTarget(t, history(fmt.Sprintf(`INSERT INTO flyway_schema_history VALUES (1, '1', 'SQL', 'V1__widgets.sql', %d, true), (2, NULL, 'SQL', 'R__labels.sql', 12345, true)`, flywayChecksum(widgets)))...)
	block, err := seedMigrationsPending(ctx, db, dsn, dir, migDir)
	if err != nil || block == nil || !strings.Contains(block.Reason, "repeatable R__labels.sql") {
		t.Fatalf("a changed repeatable: block %+v, err %v; want a refusal naming repeatable R__labels.sql", block, err)
	}

	// A baselined database that never ran the repeatable.
	db, dsn = seedTarget(t, history(`INSERT INTO flyway_schema_history VALUES (1, '1', 'BASELINE', '<< Flyway Baseline >>', NULL, true)`)...)
	block, err = seedMigrationsPending(ctx, db, dsn, dir, migDir)
	if err != nil || block == nil || !strings.Contains(block.Reason, "repeatable R__labels.sql") {
		t.Fatalf("a baseline without the repeatable: block %+v, err %v; want a refusal naming repeatable R__labels.sql", block, err)
	}

	// The repeatable's last run failed.
	db, dsn = seedTarget(t, history(current, fmt.Sprintf(`INSERT INTO flyway_schema_history VALUES (3, NULL, 'SQL', 'R__labels.sql', %d, false)`, flywayChecksum(labels)))...)
	_, err = seedMigrationsPending(ctx, db, dsn, dir, migDir)
	if err == nil || !strings.Contains(err.Error(), "repeatable R__labels.sql as failed part-way") {
		t.Fatalf("a failed repeatable: err %v; want a refusal naming it as failed", err)
	}

	// A versioned migration applied from content other than the file's.
	db, dsn = seedTarget(t, history(fmt.Sprintf(`INSERT INTO flyway_schema_history VALUES (1, '1', 'SQL', 'V1__widgets.sql', 54321, true), (2, NULL, 'SQL', 'R__labels.sql', %d, true)`, flywayChecksum(labels)))...)
	_, err = seedMigrationsPending(ctx, db, dsn, dir, migDir)
	if err == nil || !strings.Contains(err.Error(), "migration 1 as applied from different content") {
		t.Fatalf("an edited applied migration: err %v; want a refusal naming migration 1", err)
	}
}
