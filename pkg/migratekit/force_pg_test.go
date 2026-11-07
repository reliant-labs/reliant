package migratekit

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
)

// THE DIRTY-SCHEMA RECOVERY, END TO END. A run applies 1 and 2, and 3 fails.
// golang-migrate leaves the schema dirty at 3. The recovery is the one every
// refusal names: repair the schema, `force 3`, run Up. It used to be refused
// at the last step — Up found 1, 2 and 3 at or below the schema's version
// with no applied row and reported all three MISSING, telling the operator to
// rename migrations that had run.
var (
	// brokenThird fails at its INSERT. golang-migrate runs a file as one
	// implicit transaction, so the CREATE before it rolls back too.
	brokenThird = map[string]string{
		"3_ledger.up.sql": "CREATE TABLE ledger (id INT PRIMARY KEY);\nINSERT INTO no_such_table VALUES (1);",
	}
	// fixedThird is the same migration, repaired before the next release.
	fixedThird = map[string]string{
		"3_ledger.up.sql": "CREATE TABLE ledger (id INT PRIMARY KEY);",
	}
	fourth = map[string]string{
		"4_audit.up.sql": "CREATE TABLE audit (id INT PRIMARY KEY);",
	}
)

// failedAtThird returns a database whose one Up applied 1 and 2 and failed at
// 3, plus the error that Up returned.
func failedAtThird(t *testing.T) (string, error) {
	t.Helper()
	dsn := requirePG(t)
	mg, err := Open(Options{FS: release(merged(olderFiles, brokenThird)), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = mg.Close() }()
	_, upErr := mg.Up()
	if upErr == nil {
		t.Fatal("Up over a broken migration succeeded; the test premise no longer holds")
	}
	if st, _ := mg.State(); st.Version != 3 || !st.Dirty {
		t.Fatalf("schema after the failed run = %+v; want dirty at 3", st)
	}
	return dsn, upErr
}

func appliedVersions(t *testing.T, dsn string) map[uint]string {
	t.Helper()
	got, err := onSession(t, openRecordDB(t, dsn), readApplied)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func force(t *testing.T, files map[string]string, dsn string, version int) {
	t.Helper()
	mg, err := Open(Options{FS: release(files), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = mg.Close() }()
	if err := mg.Force(version); err != nil {
		t.Fatalf("force %d: %v", version, err)
	}
	if st, _ := mg.State(); st.Version != uint(version) || st.Dirty {
		t.Fatalf("schema after force %d = %+v; want clean at %d", version, st, version)
	}
}

// The versions a failed run DID apply are recorded; the one that failed is not.
func TestUpRecordsTheVersionsAppliedBeforeAFailure(t *testing.T) {
	dsn, _ := failedAtThird(t)
	got := appliedVersions(t, dsn)
	if want := map[uint]string{1: "accounts", 2: "plans"}; !maps.Equal(got, want) {
		t.Errorf("record after a run that failed at 3 = %v; want %v", got, want)
	}
}

// Repair forward: finish 3 by hand, force 3, and the next release's Up applies
// 4 on top.
func TestForceThenUpRecoversAMigrationThatFailedPartWay(t *testing.T) {
	dsn, _ := failedAtThird(t)
	if _, err := openRecordDB(t, dsn).Exec(fixedThird["3_ledger.up.sql"]); err != nil {
		t.Fatalf("repair 3 by hand: %v", err)
	}
	force(t, merged(olderFiles, brokenThird), dsn, 3)

	res := mustUp(t, release(merged(merged(olderFiles, brokenThird), fourth)), dsn)
	if !res.Changed || res.After.Version != 4 {
		t.Fatalf("up after force 3 = %+v; want 4 applied", res)
	}
	want := map[uint]string{1: "accounts", 2: "plans", 3: "ledger", 4: "audit"}
	if got := appliedVersions(t, dsn); !maps.Equal(got, want) {
		t.Errorf("record = %v; want %v", got, want)
	}
}

// Undo: nothing of 3 landed, so force the version back to 2 and let the
// repaired release apply 3 itself.
func TestForceBackThenUpReappliesTheFailedMigration(t *testing.T) {
	dsn, _ := failedAtThird(t)
	force(t, merged(olderFiles, brokenThird), dsn, 2)

	res := mustUp(t, release(merged(olderFiles, fixedThird)), dsn)
	if !res.Changed || res.After.Version != 3 {
		t.Fatalf("up after force 2 = %+v; want 3 applied", res)
	}
	want := map[uint]string{1: "accounts", 2: "plans", 3: "ledger"}
	if got := appliedVersions(t, dsn); !maps.Equal(got, want) {
		t.Errorf("record = %v; want %v", got, want)
	}
}

// A dirty schema reaches golang-migrate's dirty refusal — the fact a caller's
// policy keys on — not a MISSING report for the version that failed.
func TestUpOnADirtySchemaReportsItDirty(t *testing.T) {
	dsn, _ := failedAtThird(t)
	mg, err := Open(Options{FS: release(merged(olderFiles, brokenThird)), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = mg.Close() }()
	_, err = mg.Up()
	var dirty migrate.ErrDirty
	if !errors.As(err, &dirty) || dirty.Version != 3 {
		t.Fatalf("Up on a schema dirty at 3 = %v; want migrate.ErrDirty{3}", err)
	}
}

// Forcing BELOW recorded versions drops their rows: the schema no longer claims
// them, and a different file under one of those numbers must apply as new
// rather than trip as a mismatch against a migration that was un-claimed.
func TestForceBelowDropsTheRowsAboveIt(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(merged(olderFiles, fixedThird)), dsn)
	force(t, merged(olderFiles, fixedThird), dsn, 1)
	if got, want := appliedVersions(t, dsn), map[uint]string{1: "accounts"}; !maps.Equal(got, want) {
		t.Errorf("record after force 1 = %v; want %v", got, want)
	}
}

// Forcing AHEAD records each version the force asserts; versions below the
// settled one keep whatever they had — a force asserts the schema's new
// position, not that an older gap was filled.
func TestForceAheadRecordsTheVersionsItAsserts(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(olderFiles), dsn)
	files := merged(merged(olderFiles, fixedThird), fourth)
	force(t, files, dsn, 4)
	want := map[uint]string{1: "accounts", 2: "plans", 3: "ledger", 4: "audit"}
	if got := appliedVersions(t, dsn); !maps.Equal(got, want) {
		t.Errorf("record after force 4 = %v; want %v", got, want)
	}
	if res := mustUp(t, release(files), dsn); res.Changed {
		t.Errorf("up after force 4 = %+v; want a no-op", res)
	}
}

// A database with no record (migrated before it existed, or by a tool that
// keeps none) is not given one by a force: creating it would skip the
// bootstrap, and every older version would read as missing. The next Up
// bootstraps it from the forced version.
func TestForceLeavesAnUnrecordedDatabaseToTheBootstrap(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(olderFiles), dsn)
	db := openRecordDB(t, dsn)
	if _, err := db.Exec("DROP TABLE " + appliedTable); err != nil {
		t.Fatal(err)
	}
	files := merged(olderFiles, fixedThird)
	force(t, files, dsn, 2)
	var exists bool
	if err := db.QueryRow("SELECT to_regclass('" + appliedTable + "') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("force created the applied record; the next Up's bootstrap would then skip its backfill")
	}
	if res := mustUp(t, release(files), dsn); res.After.Version != 3 {
		t.Fatalf("up after force = %+v; want 3 applied", res)
	}
	got := appliedVersions(t, dsn)
	if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, []uint{1, 2, 3}) {
		t.Errorf("record versions = %v; want 1, 2 (bootstrapped) and 3 (applied)", keys)
	}
}

// AutoMigrate's dirty refusal names commands that exist: forge's force (its
// spelling pinned by a test in internal/cli) and the binary's own. It named
// `migrate force N`, golang-migrate's CLI, which forge no longer uses.
func TestAutoMigrateDirtyRefusalNamesARealForceCommand(t *testing.T) {
	dsn := requirePG(t)
	db := openRecordDB(t, dsn)
	files := release(merged(olderFiles, brokenThird))
	if err := AutoMigrate(files, "migrations")(db, nil); err == nil {
		t.Fatal("AutoMigrate over a broken migration succeeded")
	}
	err := AutoMigrate(files, "migrations")(db, nil)
	if err == nil {
		t.Fatal("AutoMigrate booted over a dirty schema")
	}
	for _, want := range []string{"dirty at version 3", "`" + ForceCommand + " 3`", "`db migrate force 3`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("dirty refusal does not carry %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "`migrate force") {
		t.Errorf("dirty refusal names golang-migrate's CLI:\n%v", err)
	}
}

// AutoMigrate records a failed run's applied versions too: a server booting
// with AUTO_MIGRATE is the other way a migration fails part-way.
func TestAutoMigrateRecordsTheVersionsAppliedBeforeAFailure(t *testing.T) {
	dsn := requirePG(t)
	db := openRecordDB(t, dsn)
	if err := AutoMigrate(release(merged(olderFiles, brokenThird)), "migrations")(db, nil); err == nil {
		t.Fatal("AutoMigrate over a broken migration succeeded")
	}
	if got, want := appliedVersions(t, dsn), map[uint]string{1: "accounts", 2: "plans"}; !maps.Equal(got, want) {
		t.Errorf("record after a boot that failed at 3 = %v; want %v", got, want)
	}
}
