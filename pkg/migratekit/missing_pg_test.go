package migratekit

import (
	"errors"

	"strings"
	"testing"
)

// The out-of-order merge, in timestamp versions. Branch B allocated
// ...120000 and merged and deployed first. Branch A allocated ...110000 —
// EARLIER — and merges after. Main now carries a migration at a version the
// database has already passed.
var (
	branchBLate = map[string]string{
		"20260101120000_add_orgs.up.sql": "CREATE TABLE orgs (id INT PRIMARY KEY);",
	}
	branchAEarly = map[string]string{
		"20260101110000_add_teams.up.sql": "CREATE TABLE teams (id INT PRIMARY KEY);",
	}
)

// TestUpRefusesAMigrationTheSchemaHasAlreadyPassed is the whole point of the
// check. The database deployed B (version 20260101120000). A, versioned
// EARLIER, merges afterwards. golang-migrate reads only versions above the
// current one, so A's SQL would never run and every later `up` would report
// the schema current — the failure surfacing much later as a missing table.
//
// Up must refuse, name the version and the file, and apply nothing.
func TestUpRefusesAMigrationTheSchemaHasAlreadyPassed(t *testing.T) {
	dsn := requirePG(t)
	// The deploy that happened first: older files plus B.
	mustUp(t, release(merged(olderFiles, branchBLate)), dsn)

	// A merges. Main now embeds older + B + A, where A sorts below B.
	mg, err := Open(Options{FS: release(merged(merged(olderFiles, branchBLate), branchAEarly)), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = mg.Close() }()

	_, err = mg.Up()
	var missing *MissingMigrationError
	if !errors.As(err, &missing) {
		t.Fatalf("Up err = %v; want a *MissingMigrationError", err)
	}
	if len(missing.Missing) != 1 || missing.Missing[0].Version != 20260101110000 {
		t.Errorf("missing = %+v; want exactly version 20260101110000 (branch A)", missing.Missing)
	}
	for _, want := range []string{
		"20260101110000_add_teams.up.sql", // the file to fix
		"NOTHING WAS APPLIED",
		"rebase",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not carry %q — it is the fix instruction:\n%v", want, err)
		}
	}

	// A refusal changes nothing: teams must not exist.
	db := openRecordDB(t, dsn)
	var exists bool
	if err := db.QueryRow("SELECT to_regclass('teams') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("a refused Up created the table anyway — nothing may be applied")
	}
}

// TestUpProceedsOnceTheMissingMigrationIsReVersioned follows the error's own
// instruction: the late migration is renamed to a version NEWER than the
// schema has reached. It then applies as an ordinary pending migration.
//
// This is the fix being a rename, which is what makes refusing cheap: the
// migration never ran anywhere, so nothing has to be reconciled.
func TestUpProceedsOnceTheMissingMigrationIsReVersioned(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(merged(olderFiles, branchBLate)), dsn)

	// A re-versioned to sort after B, same SQL, same name.
	reVersioned := map[string]string{
		"20260101130000_add_teams.up.sql": branchAEarly["20260101110000_add_teams.up.sql"],
	}
	res := mustUp(t, release(merged(merged(olderFiles, branchBLate), reVersioned)), dsn)
	if !res.Changed || res.After.Version != 20260101130000 {
		t.Fatalf("up after re-versioning = %+v; want 20260101130000 applied", res)
	}

	db := openRecordDB(t, dsn)
	var exists bool
	if err := db.QueryRow("SELECT to_regclass('teams') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("the re-versioned migration did not apply")
	}
}

// TestUpProceedsOnceAMergedMissingMigrationIsReplaced follows the refusal's
// OTHER instruction, for the file that has already merged — the shape of
// reliant's 20261024024221, which merged below a version main and prod already
// held. Two databases exist by then:
//
//   - passed: deployed B before A merged, so A's version is behind it and A is
//     missing — the database the refusal fires on;
//   - fresh: created from main after A merged, so it applied A at its old
//     version, in order, and holds A's table already.
//
// A rename alone would re-run A's SQL on fresh. The fix the message names is
// a replacement: delete A, add its SQL re-runnably at a new version. Both
// databases must then migrate cleanly, and fresh's record of the deleted
// version must not be held against it.
func TestUpProceedsOnceAMergedMissingMigrationIsReplaced(t *testing.T) {
	passed := requirePG(t)
	fresh := requirePG(t)
	mustUp(t, release(merged(olderFiles, branchBLate)), passed)
	mustUp(t, release(merged(merged(olderFiles, branchBLate), branchAEarly)), fresh)

	replaced := merged(merged(olderFiles, branchBLate), map[string]string{
		"20260101130000_add_teams.up.sql": "CREATE TABLE IF NOT EXISTS teams (id INT PRIMARY KEY);",
	})
	for name, dsn := range map[string]string{"passed": passed, "fresh": fresh} {
		res := mustUp(t, release(replaced), dsn)
		if res.After.Version != 20260101130000 {
			t.Errorf("%s: up after replacing = %+v; want the schema at 20260101130000", name, res)
		}
		db := openRecordDB(t, dsn)
		var exists bool
		if err := db.QueryRow("SELECT to_regclass('teams') IS NOT NULL").Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("%s: teams does not exist after the replacement applied", name)
		}
		// And the next release boots on it: nothing is missing, nothing
		// mismatched, nothing re-applied.
		if again := mustUp(t, release(replaced), dsn); again.Changed {
			t.Errorf("%s: second up = %+v; want no change", name, again)
		}
	}
}

// TestUpAcceptsATimestampMigrationOnASequentialDatabase is the adoption
// path every existing forge project takes: a database migrated entirely with
// 5-digit sequential versions meets a release whose new migration is a
// timestamp.
//
// Nothing may be re-applied (the sequential versions are already recorded by
// the bootstrap), and the timestamp migration must apply as an ordinary
// pending one — it sorts after every sequential version, which is what makes
// adoption need no renaming.
func TestUpAcceptsATimestampMigrationOnASequentialDatabase(t *testing.T) {
	dsn := requirePG(t)
	sequential := map[string]string{
		"00001_accounts.up.sql": "CREATE TABLE accounts (id INT PRIMARY KEY);",
		"00002_plans.up.sql":    "CREATE TABLE plans (id INT PRIMARY KEY);",
	}
	mustUp(t, release(sequential), dsn)

	timestamped := merged(sequential, map[string]string{
		"20260101120000_add_orgs.up.sql": "CREATE TABLE orgs (id INT PRIMARY KEY);",
	})
	res := mustUp(t, release(timestamped), dsn)
	if !res.Changed {
		t.Fatalf("up = %+v; want the timestamp migration applied", res)
	}
	if res.Before.Version != 2 || res.After.Version != 20260101120000 {
		t.Errorf("up moved %d -> %d; want 2 -> 20260101120000", res.Before.Version, res.After.Version)
	}

	// Re-running is a clean no-op: nothing is re-applied.
	if again := mustUp(t, release(timestamped), dsn); again.Changed {
		t.Errorf("second up = %+v; want no change", again)
	}
}

// TestBootstrapAdoptsAPreRecordDatabaseWithoutRefusing is the upgrade that
// would otherwise break every existing database on the first deploy of this
// check.
//
// A database migrated before schema_migrations_applied existed has NO rows,
// so every version it applied looks missing. The bootstrap must record those
// versions when it creates the table, from the schema's own high-water mark,
// and Up must then proceed normally.
func TestBootstrapAdoptsAPreRecordDatabaseWithoutRefusing(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(olderFiles), dsn)

	// Simulate a database migrated by a binary that predates the record.
	db := openRecordDB(t, dsn)
	if _, err := db.Exec("DROP TABLE " + appliedTable); err != nil {
		t.Fatalf("simulate a pre-record database: %v", err)
	}

	// The same binary runs again: 1 and 2 are applied but unrecorded, and
	// must NOT be reported missing.
	res := mustUp(t, release(olderFiles), dsn)
	if res.Changed {
		t.Errorf("up over a bootstrapped database = %+v; want no change", res)
	}

	records, err := onSession(t, db, readApplied)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []uint{1, 2} {
		name, ok := records[v]
		if !ok {
			t.Errorf("version %d has no bootstrap row — it would be reported missing", v)
		}
		// The descriptor is deliberately empty: an old database cannot
		// vouch for WHICH file it ran under a number.
		if name != "" {
			t.Errorf("bootstrap row %d = %q; want an empty descriptor, never a guess from this binary's filenames", v, name)
		}
	}
}

// TestBootstrapDoesNotAbsolveALaterMissingMigration is the boundary the
// bootstrap has to get right, and the one a naive implementation gets wrong.
//
// Backfilling on every run would re-absolve a genuinely missing migration
// forever and silently disable the check. The bootstrap must happen ONCE, when
// the table is created; after that, an absent row means the version never ran.
func TestBootstrapDoesNotAbsolveALaterMissingMigration(t *testing.T) {
	dsn := requirePG(t)
	// A pre-record database, adopted by the bootstrap.
	mustUp(t, release(olderFiles), dsn)
	db := openRecordDB(t, dsn)
	if _, err := db.Exec("DROP TABLE " + appliedTable); err != nil {
		t.Fatal(err)
	}
	mustUp(t, release(olderFiles), dsn) // creates + backfills the table

	// Now deploy a later migration, then merge an EARLIER-versioned one.
	mustUp(t, release(merged(olderFiles, branchBLate)), dsn)
	mg, err := Open(Options{FS: release(merged(merged(olderFiles, branchBLate), branchAEarly)), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = mg.Close() }()

	_, upErr := mg.Up()
	var missing *MissingMigrationError
	if !errors.As(upErr, &missing) {
		t.Fatalf("Up err = %v; want a *MissingMigrationError — the bootstrap must not keep absolving new arrivals", upErr)
	}
}

// TestConcurrentMigratorsBootstrapSafely races replicas of the same release
// against one database, which is what a rolling deploy does. The bootstrap
// creates a table and inserts into it, and CREATE TABLE IF NOT EXISTS is not
// race-free in postgres — exactly one migrator may win, and none may error.
func TestConcurrentMigratorsBootstrapSafely(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(olderFiles), dsn)
	db := openRecordDB(t, dsn)
	if _, err := db.Exec("DROP TABLE " + appliedTable); err != nil {
		t.Fatal(err)
	}

	const replicas = 4
	fsys := release(merged(olderFiles, branchBLate))
	errs := make(chan error, replicas)
	for i := 0; i < replicas; i++ {
		go func() {
			mg, err := Open(Options{FS: fsys, Dir: "migrations", DSN: dsn})
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = mg.Close() }()
			_, err = mg.Up()
			errs <- err
		}()
	}
	for i := 0; i < replicas; i++ {
		if err := <-errs; err != nil {
			t.Errorf("replica %d: %v", i, err)
		}
	}

	// The winner applied B exactly once and the record is consistent.
	records, err := onSession(t, db, readApplied)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := records[20260101120000]; !ok {
		t.Errorf("record = %v; want branch B recorded as applied", records)
	}
}

// TestAutoMigrateRefusesAMigrationTheSchemaHasAlreadyPassed: a server booting
// with AUTO_MIGRATE reaches the same verdict `db migrate up` does, rather than
// serving with SQL in its image that will never run.
func TestAutoMigrateRefusesAMigrationTheSchemaHasAlreadyPassed(t *testing.T) {
	dsn := requirePG(t)
	db := openRecordDB(t, dsn)
	if err := AutoMigrate(release(merged(olderFiles, branchBLate)), "migrations")(db, nil); err != nil {
		t.Fatalf("first boot: %v", err)
	}
	err := AutoMigrate(release(merged(merged(olderFiles, branchBLate), branchAEarly)), "migrations")(db, nil)
	var missing *MissingMigrationError
	if !errors.As(err, &missing) {
		t.Fatalf("boot err = %v; want a *MissingMigrationError", err)
	}
}

// TestFreshDatabaseAppliesEverything pins that the check does not fire on a
// brand-new database, where nothing is applied and nothing can be missing.
func TestFreshDatabaseAppliesEverything(t *testing.T) {
	dsn := requirePG(t)
	res := mustUp(t, release(merged(olderFiles, branchBLate)), dsn)
	if !res.Changed || res.After.Version != 20260101120000 {
		t.Errorf("fresh up = %+v; want every migration applied", res)
	}
}
