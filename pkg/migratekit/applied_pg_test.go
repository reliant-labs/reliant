package migratekit

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// branchThird is a feature branch's migration 3, applied to a shared dev
// database. mainThirdAndFourth is what main holds after that branch renumbered
// its migration to 4 and main's own 3 landed first — the same number, a
// different file.
var (
	branchThird = map[string]string{
		"3_byo_clusters.up.sql": "CREATE TABLE clusters (id INT PRIMARY KEY);",
	}
	mainThirdAndFourth = map[string]string{
		"3_retire_plans.up.sql": "ALTER TABLE plans ADD COLUMN retired_at TIMESTAMPTZ;",
		"4_byo_clusters.up.sql": "CREATE TABLE IF NOT EXISTS clusters (id INT PRIMARY KEY);",
	}
)

func openRecordDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx/v5", sqlDSN(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// onSession runs a record helper on one connection checked out of db, then
// gives the connection back. The helpers take a run's session, never a pool
// (runlock.go, ONE SESSION).
func onSession[T any](t *testing.T, db *sql.DB, read func(context.Context, *sql.Conn) (T, error)) (T, error) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	return read(context.Background(), conn)
}

// TestUpRefusesAVersionAppliedFromADifferentFile is the renumbered-branch
// collision: the database is "at 3", but its 3 is the branch's byo_clusters,
// not main's retire_plans. golang-migrate alone sees 3 == 3, applies only 4,
// and main's 3 never runs. Up must refuse — naming both files and the way
// out — and apply nothing.
func TestUpRefusesAVersionAppliedFromADifferentFile(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(merged(olderFiles, branchThird)), dsn)

	mg, err := Open(Options{FS: release(merged(olderFiles, mainThirdAndFourth)), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = mg.Close() }()
	_, err = mg.Up()
	var mismatch *MigrationMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Up err = %v; want a *MigrationMismatchError", err)
	}
	if len(mismatch.Mismatches) != 1 || mismatch.Mismatches[0] != (MigrationMismatch{Version: 3, Applied: "byo_clusters", Embedded: "retire_plans"}) {
		t.Errorf("mismatches = %+v; want exactly version 3: byo_clusters applied, retire_plans embedded", mismatch.Mismatches)
	}
	for _, want := range []string{`"byo_clusters"`, `"retire_plans"`, "NOTHING WAS APPLIED", "UPDATE " + appliedTable + " SET name ="} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not carry %q — it is the runbook for reconciling the schema:\n%v", want, err)
		}
	}
	if st, _ := mg.State(); st.Version != 3 || st.Dirty {
		t.Errorf("schema after a refused up = %+v; want version 3, clean — a refusal must change nothing", st)
	}
}

// TestUpProceedsOnceTheRecordIsReconciled follows the error's own runbook: a
// human applies main's 3 by hand, keeps the version at 3, and CORRECTS the
// stale record to name the file the schema now reflects. Up then applies 4.
//
// The correction is an UPDATE, not a DELETE, and the difference is
// load-bearing: a deleted row leaves version 3 at or below the schema's
// version with no record, which is precisely a MISSING migration
// (missing.go). Clearing the record would trade one refusal for another.
func TestUpProceedsOnceTheRecordIsReconciled(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(merged(olderFiles, branchThird)), dsn)
	db := openRecordDB(t, dsn)
	if _, err := db.Exec(mainThirdAndFourth["3_retire_plans.up.sql"]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE " + appliedTable + " SET name = 'retire_plans' WHERE version = 3"); err != nil {
		t.Fatal(err)
	}

	res := mustUp(t, release(merged(olderFiles, mainThirdAndFourth)), dsn)
	if !res.Changed || res.After.Version != 4 {
		t.Fatalf("up after reconciling = %+v; want 4 applied", res)
	}
	got, err := onSession(t, db, readApplied)
	if err != nil {
		t.Fatal(err)
	}
	// 3 carries the descriptor the human reconciled it to; this Up applied
	// only 4.
	want := map[uint]string{1: "accounts", 2: "plans", 3: "retire_plans", 4: "byo_clusters"}
	if len(got) != len(want) {
		t.Fatalf("record = %v, want %v", got, want)
	}
	for v, name := range want {
		if got[v] != name {
			t.Errorf("record[%d] = %q, want %q", v, got[v], name)
		}
	}
}

// TestUpIgnoresAChangeOfZeroPadding: renaming 1_accounts to 0001_accounts
// changes nothing golang-migrate records, and must not read as a different
// migration.
func TestUpIgnoresAChangeOfZeroPadding(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(olderFiles), dsn)
	padded := map[string]string{}
	for name, body := range olderFiles {
		padded["000"+name] = body
	}
	if res := mustUp(t, release(padded), dsn); res.Changed || res.After.Version != 2 {
		t.Errorf("up over re-padded filenames = %+v; want an ordinary no-op at 2", res)
	}
}

// TestUpDoesNotCheckVersionsWithNoRecord: a database migrated before the
// record existed has no rows, so nothing is known about which files it ran.
// That is not a mismatch — refusing would break every existing database on
// the first deploy of this check.
//
// The bootstrap gives those versions a row (they must not read as MISSING —
// see missing.go), but with an EMPTY descriptor, which is what keeps this
// test's original point true: version 3 was applied from the branch's
// byo_clusters while this binary embeds retire_plans under 3, and the
// bootstrap must NOT certify either. An empty descriptor asserts "applied,
// file unknown", so the mismatch check skips it rather than vouching for a
// schema no record describes.
func TestUpDoesNotCheckVersionsWithNoRecord(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(merged(olderFiles, branchThird)), dsn)
	db := openRecordDB(t, dsn)
	if _, err := db.Exec("DROP TABLE " + appliedTable); err != nil {
		t.Fatalf("simulate a pre-record database: %v", err)
	}
	res := mustUp(t, release(merged(olderFiles, mainThirdAndFourth)), dsn)
	if res.After.Version != 4 {
		t.Errorf("up over an unrecorded database = %+v; want 4 applied", res)
	}
	got, err := onSession(t, db, readApplied)
	if err != nil {
		t.Fatal(err)
	}
	// 4 is the version THIS Up applied, so it carries a real descriptor.
	if got[4] != "byo_clusters" {
		t.Errorf("record[4] = %q; want the descriptor of the migration this Up ran", got[4])
	}
	// 1, 2 and 3 were bootstrapped from the schema's high-water mark. They
	// must be present (else they read as missing) and EMPTY (else the
	// bootstrap has certified a file the database cannot vouch for — here
	// it would claim 3 was main's retire_plans when the branch's
	// byo_clusters is what actually ran).
	for _, v := range []uint{1, 2, 3} {
		name, ok := got[v]
		if !ok {
			t.Errorf("version %d has no row; the bootstrap must record what the schema version vouches for", v)
		}
		if name != "" {
			t.Errorf("record[%d] = %q; a bootstrapped version must carry NO descriptor — the one fact a pre-record database cannot supply", v, name)
		}
	}
}

// TestAutoMigrateRefusesAVersionAppliedFromADifferentFile: a server booting
// with AUTO_MIGRATE reaches the same verdict `db migrate up` does, rather than
// serving against a schema its version number misdescribes.
func TestAutoMigrateRefusesAVersionAppliedFromADifferentFile(t *testing.T) {
	dsn := requirePG(t)
	db := openRecordDB(t, dsn)
	if err := AutoMigrate(release(merged(olderFiles, branchThird)), "migrations")(db, nil); err != nil {
		t.Fatalf("branch boot: %v", err)
	}
	err := AutoMigrate(release(merged(olderFiles, mainThirdAndFourth)), "migrations")(db, nil)
	var mismatch *MigrationMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("main boot err = %v; want a *MigrationMismatchError", err)
	}
}
