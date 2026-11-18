package migratekit

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// A forge history squashed into one baseline, in miniature: two migrations
// built the database; `forge db squash` replaced them with a baseline that
// declares them, and the release adds one migration on top.
//
// As in adopt_pg_test.go, the baseline's DDL is NOT idempotent, so every
// adoption below also proves the baseline's SQL did not run.
var (
	forgeHistory = map[string]string{
		"20260101000001_accounts.up.sql": "CREATE TABLE accounts (id INT PRIMARY KEY);",
		"20260101000002_plans.up.sql":    "CREATE TABLE plans (id INT PRIMARY KEY, account_id INT REFERENCES accounts (id));",
	}
	forgeSquashSQL = "CREATE TABLE accounts (id INT PRIMARY KEY);\n" +
		"CREATE TABLE plans (id INT PRIMARY KEY, account_id INT REFERENCES accounts (id));\n"
	// forgeSquashDecl is what `forge db squash` writes above the dump.
	forgeSquashDecl = "-- forge:adopts forge 20260101000001 20260101000002\n-- forge:backward-compatible\n"
	afterSquash     = map[string]string{"20260202000000_add_orgs.up.sql": "CREATE TABLE orgs (id INT PRIMARY KEY);"}
)

// squashRelease is the release that replaced forgeHistory with one baseline
// named name, declaring the history or not, plus the migration after it.
func squashRelease(name string, declared bool) map[string]string {
	body := forgeSquashSQL
	if declared {
		body = forgeSquashDecl + body
	}
	return merged(map[string]string{name: body}, afterSquash)
}

// forgeMigrated builds the database the release before the squash left: the
// history applied by forge's migrator, plus a row the adoption must not touch.
func forgeMigrated(t *testing.T, history map[string]string) (dsn string, db *sql.DB) {
	t.Helper()
	dsn = requirePG(t)
	mustUp(t, release(history), dsn)
	db = openRecordDB(t, dsn)
	if _, err := db.Exec("INSERT INTO accounts (id) VALUES (42)"); err != nil {
		t.Fatal(err)
	}
	return dsn, db
}

func schemaVersion(t *testing.T, db *sql.DB) (uint, bool) {
	t.Helper()
	var (
		v     int64
		dirty bool
	)
	if err := db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&v, &dirty); err != nil {
		t.Fatal(err)
	}
	return uint(v), dirty
}

// assertForgeUntouched is "NOTHING WAS APPLIED", checked: the version, the record
// and the schema are as the history left them.
func assertForgeUntouched(t *testing.T, db *sql.DB, version uint) {
	t.Helper()
	if v, dirty := schemaVersion(t, db); v != version || dirty {
		t.Errorf("schema_migrations = %d (dirty=%v); want %d, clean", v, dirty, version)
	}
	rec := appliedRecord(t, db)
	for _, v := range []uint{20260201000000, 1, 20260202000000} {
		if _, ok := rec[v]; ok {
			t.Errorf("a refused run recorded version %d as applied", v)
		}
	}
	if tableExists(t, db, "orgs") {
		t.Error("a refused run applied the migration after the baseline")
	}
}

// Numbered above the history (what squash recommends): adopted, the baseline
// becomes the schema's version, and the migration after it applies.
func TestAdoptForgeHistory_BaselineAboveTheHistory(t *testing.T) {
	t.Parallel()
	dsn, db := forgeMigrated(t, forgeHistory)

	res, err := up(t, squashRelease("20260201000000_baseline.up.sql", true), dsn)
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	a := res.Adopted
	if a == nil || a.Tool != "forge" || a.Version != 20260201000000 || a.Versions != 2 {
		t.Fatalf("Adopted = %+v; want the baseline adopted with 2 forge versions verified", a)
	}
	if res.Before.Version != 20260201000000 || res.After.Version != 20260202000000 || !res.Changed {
		t.Errorf("result = %+v; want 20260201000000 -> 20260202000000 (add_orgs applied after the adoption)", res)
	}
	if !tableExists(t, db, "orgs") {
		t.Error("the migration after the baseline did not apply")
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM accounts WHERE id = 42").Scan(&n); err != nil || n != 1 {
		t.Errorf("the adoption touched existing rows (count=%d, err=%v)", n, err)
	}
	rec := appliedRecord(t, db)
	for _, v := range []uint{20260101000001, 20260101000002, 20260201000000, 20260202000000} {
		if _, ok := rec[v]; !ok {
			t.Errorf("applied record lacks %d: %v", v, rec)
		}
	}
	if rec[20260201000000] != "baseline" {
		t.Errorf("the baseline's applied row = %q; want its descriptor", rec[20260201000000])
	}

	// Idempotent: the next run finds a database at a version it carries.
	again, err := up(t, squashRelease("20260201000000_baseline.up.sql", true), dsn)
	if err != nil || again.Adopted != nil || again.Changed {
		t.Errorf("second up = %+v, %v; want nothing to do", again, err)
	}
}

// Numbered below the history (squash's default stem): adopted too. The
// version moves DOWN to the baseline's, which golang-migrate then applies
// forward from.
func TestAdoptForgeHistory_BaselineBelowTheHistory(t *testing.T) {
	t.Parallel()
	dsn, db := forgeMigrated(t, forgeHistory)

	res, err := up(t, squashRelease("00001_baseline.up.sql", true), dsn)
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if res.Adopted == nil || res.Adopted.Version != 1 {
		t.Fatalf("Adopted = %+v; want 00001_baseline adopted", res.Adopted)
	}
	if res.After.Version != 20260202000000 || !tableExists(t, db, "orgs") {
		t.Errorf("result = %+v; want add_orgs applied after the adoption", res)
	}
}

// Behind the squash: a database that never applied a listed version cannot
// be finished by this release, which no longer carries that migration.
func TestAdoptForgeHistory_BehindIsRefused(t *testing.T) {
	t.Parallel()
	dsn, db := forgeMigrated(t, map[string]string{"20260101000001_accounts.up.sql": forgeHistory["20260101000001_accounts.up.sql"]})

	_, err := up(t, squashRelease("20260201000000_baseline.up.sql", true), dsn)
	var adoptErr *AdoptionError
	if !errors.As(err, &adoptErr) || adoptErr.Tool != "forge" || len(adoptErr.Missing) != 1 || adoptErr.Missing[0] != 20260101000002 {
		t.Fatalf("up err = %v; want a forge *AdoptionError missing 20260101000002", err)
	}
	assertForgeUntouched(t, db, 20260101000001)
}

// Past the squash: the database applied a migration the baseline does not
// list — the squash was cut before it merged.
func TestAdoptForgeHistory_UnlistedVersionIsRefused(t *testing.T) {
	t.Parallel()
	dsn, db := forgeMigrated(t, merged(forgeHistory, map[string]string{
		"20260101000003_retire.up.sql": "ALTER TABLE plans ADD COLUMN retired_at TIMESTAMPTZ;",
	}))

	_, err := up(t, squashRelease("20260201000000_baseline.up.sql", true), dsn)
	var adoptErr *AdoptionError
	if !errors.As(err, &adoptErr) || len(adoptErr.Unexpected) != 1 || adoptErr.Unexpected[0] != 20260101000003 {
		t.Fatalf("up err = %v; want a forge *AdoptionError naming 20260101000003", err)
	}
	assertForgeUntouched(t, db, 20260101000003)
}

// Without the declaration, the database the history built is refused as a
// history this binary does not carry — wherever the baseline is numbered.
// It used to read "AHEAD" above the history and "MISSING" below it.
func TestAdoptForgeHistory_UndeclaredIsAForeignHistory(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"20260201000000_baseline.up.sql", "00001_baseline.up.sql"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dsn, db := forgeMigrated(t, forgeHistory)
			_, err := up(t, squashRelease(name, false), dsn)
			var foreign *ForeignHistoryError
			if !errors.As(err, &foreign) || foreign.Version != 20260101000002 {
				t.Fatalf("up err = %v; want a *ForeignHistoryError at 20260101000002", err)
			}
			for _, not := range []string{"AHEAD", "can NEVER be applied"} {
				if strings.Contains(err.Error(), not) {
					t.Errorf("the refusal says %q: %v", not, err)
				}
			}
			assertForgeUntouched(t, db, 20260101000002)
		})
	}
}

// A schema AHEAD of this binary — a newer release migrated it past the
// baseline — shares this binary's migrations, and is classifyAhead's to judge,
// not an adoption to refuse.
func TestAdoptForgeHistory_SchemaAheadIsNotAnAdoption(t *testing.T) {
	t.Parallel()
	dsn := requirePG(t)
	mustUp(t, release(squashRelease("20260201000000_baseline.up.sql", true)), dsn) // the newer release, fresh

	older := map[string]string{"20260201000000_baseline.up.sql": forgeSquashDecl + forgeSquashSQL}
	_, err := up(t, older, dsn)
	var ahead *SchemaAheadError
	if !errors.As(err, &ahead) || ahead.Version != 20260202000000 {
		t.Fatalf("up err = %v; want a *SchemaAheadError (add_orgs was not declared compatible)", err)
	}
}

// A database migrated before the applied record existed has only the high
// water mark, which golang-migrate never applies below: it vouches for the
// listed versions at or below it, and adoption writes the record bootstrap
// would have.
func TestAdoptForgeHistory_DatabaseThatPredatesTheRecord(t *testing.T) {
	t.Parallel()
	dsn, db := forgeMigrated(t, forgeHistory)
	if _, err := db.Exec("DROP TABLE " + appliedTable); err != nil {
		t.Fatal(err)
	}

	res, err := up(t, squashRelease("20260201000000_baseline.up.sql", true), dsn)
	if err != nil || res.Adopted == nil {
		t.Fatalf("up = %+v, %v; want the pre-record database adopted", res, err)
	}
	rec := appliedRecord(t, db)
	if rec[20260101000001] != "" || rec[20260101000002] != "" {
		t.Errorf("the vouched-for versions must be recorded without a descriptor, as bootstrap writes them: %v", rec)
	}
	if _, ok := rec[20260101000002]; !ok {
		t.Errorf("applied record lacks the history's versions: %v", rec)
	}
}

// Rolling BACK to the release before the squash keeps working on an adopted
// database: it finds its own versions recorded under its own files, and the
// baseline — declared backward-compatible, as squash writes it — is a
// compatible schema ahead of it.
func TestAdoptForgeHistory_ReleaseBeforeTheSquashStillRuns(t *testing.T) {
	t.Parallel()
	dsn, _ := forgeMigrated(t, forgeHistory)
	if _, err := up(t, merged(map[string]string{"20260201000000_baseline.up.sql": forgeSquashDecl + forgeSquashSQL}, nil), dsn); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	res, err := up(t, forgeHistory, dsn)
	if err != nil {
		t.Fatalf("the release before the squash refused the adopted database: %v", err)
	}
	if res.Ahead == nil || len(res.Ahead.Versions) != 1 || res.Ahead.Versions[0] != 20260201000000 {
		t.Errorf("result = %+v; want a compatible schema ahead at the baseline", res)
	}
}

// A crash between committing the baseline's applied row and Force leaves the
// schema at the history's version: the next run adopts again, and the row's
// insert is a no-op.
func TestAdoptForgeHistory_FinishesAnInterruptedAdoption(t *testing.T) {
	t.Parallel()
	dsn, db := forgeMigrated(t, forgeHistory)
	if _, err := db.Exec("INSERT INTO " + appliedTable + " (version, name) VALUES (20260201000000, 'baseline')"); err != nil {
		t.Fatal(err)
	}
	res, err := up(t, squashRelease("20260201000000_baseline.up.sql", true), dsn)
	if err != nil || res.Adopted == nil || res.After.Version != 20260202000000 {
		t.Fatalf("up = %+v, %v; want the interrupted adoption finished", res, err)
	}
}

// One session per run (#706): adoption reads and writes on the run's own
// connection, so it completes on a pool capped at one — through Apply, the
// boot path, with a deadline so a second session needed is a failure, not a
// hang.
func TestAdoptForgeHistory_OnOneConnection(t *testing.T) {
	t.Parallel()
	dsn, _ := forgeMigrated(t, forgeHistory)
	pool := openRecordDB(t, dsn)
	pool.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := Apply(ctx, pool, release(squashRelease("20260201000000_baseline.up.sql", true)), "migrations")
	if err != nil || res.Adopted == nil || res.After.Version != 20260202000000 {
		t.Fatalf("Apply on one connection = %+v, %v; want the history adopted and add_orgs applied", res, err)
	}
}
