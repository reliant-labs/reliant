package migratekit

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// These tests boot real postgres (pkg/pgtest), so they are skipped under
// -short per the repo's testing tiers.
func requirePG(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	return dsn
}

// release is one binary's embedded migration set: the files a release was
// built with. `newer` is v1.7.0-shaped (it adds 3), `older` is the previous
// release that tops out at 2 — still serving while `newer` rolls out.
func release(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, sql := range files {
		fsys["migrations/"+name] = &fstest.MapFile{Data: []byte(sql)}
	}
	return fsys
}

var (
	olderFiles = map[string]string{
		"1_accounts.up.sql": "CREATE TABLE accounts (id INT PRIMARY KEY);",
		"2_plans.up.sql":    "CREATE TABLE plans (id INT PRIMARY KEY);",
	}
	// 3 is ADDITIVE and says so: the older release never reads retired_at.
	compatibleThird = map[string]string{
		"3_retire.up.sql": "-- forge:backward-compatible\nALTER TABLE plans ADD COLUMN retired_at TIMESTAMPTZ;",
	}
	// 3 drops a table the older release still reads, and does NOT declare
	// itself compatible — exactly what the previous release must refuse to
	// serve over.
	incompatibleThird = map[string]string{
		"3_drop.up.sql": "DROP TABLE accounts;",
	}
)

func merged(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func mustUp(t *testing.T, fsys fstest.MapFS, dsn string) Result {
	t.Helper()
	mg, err := Open(Options{FS: fsys, Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = mg.Close() }()
	res, err := mg.Up()
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	return res
}

// TestUpToleratesASchemaAheadThatDeclaredItselfCompatible is the previous
// release meeting the next one's schema — every rolling deploy, at the
// migrator.
//
// The newer release migrates the database to 3 while the OLDER release is
// still serving, and an old pod that starts (reschedule, scale-up) runs its
// `db migrate up` against a schema one version past the newest migration it
// embeds. golang-migrate fails that with "no migration found for version 3",
// crash-looping the release that is carrying the traffic.
//
// Migration 3 declared itself backward-compatible, and the newer migrator
// recorded that in the database when it applied it. So the older binary can
// PROVE its code runs against this schema: `up` must succeed, change nothing,
// and leave the version at 3.
func TestUpToleratesASchemaAheadThatDeclaredItselfCompatible(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(merged(olderFiles, compatibleThird)), dsn)

	res := mustUp(t, release(olderFiles), dsn)
	if res.Changed {
		t.Errorf("the older binary applied something to a schema ahead of it: %+v", res)
	}
	if res.After.Version != 3 {
		t.Errorf("schema version after the older binary's up = %d, want 3 (untouched)", res.After.Version)
	}
	if res.Ahead == nil || res.Ahead.Latest != 2 || len(res.Ahead.Versions) != 1 || res.Ahead.Versions[0] != 3 {
		t.Errorf("Result.Ahead = %+v; want the schema-ahead fact reported (latest 2, unknown [3]) — "+
			"a schema-ahead boot that silently reads as 'no pending migrations' hides the one thing the operator must see", res.Ahead)
	}
}

// TestUpRefusesASchemaAheadThatIsNotDeclaredCompatible is the other half: the
// newer release dropped a table the older code still reads, and nobody
// declared that safe. Running the older release there is running code against
// a schema it was never written for, so the migrate step must FAIL — loudly,
// as the typed error, naming the version and the way out — and change nothing.
func TestUpRefusesASchemaAheadThatIsNotDeclaredCompatible(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(merged(olderFiles, incompatibleThird)), dsn)

	mg, err := Open(Options{FS: release(olderFiles), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = mg.Close() }()
	_, err = mg.Up()
	var ahead *SchemaAheadError
	if !errors.As(err, &ahead) {
		t.Fatalf("Up err = %v; want a *SchemaAheadError", err)
	}
	if ahead.Version != 3 || ahead.Latest != 2 || len(ahead.Incompatible) != 1 || ahead.Incompatible[0] != 3 || ahead.Unrecorded {
		t.Errorf("SchemaAheadError = %+v; want version 3, latest 2, incompatible [3], recorded", ahead)
	}
	for _, want := range []string{"AHEAD", "NOTHING WAS APPLIED", "rolls forward only", "forge:backward-compatible"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not carry %q — it is the runbook an operator reads mid-deploy:\n%v", want, err)
		}
	}
	if st, _ := mg.State(); st.Version != 3 || st.Dirty {
		t.Errorf("schema after a refused up = %+v; want version 3, clean — a refusal must change nothing", st)
	}
}

// TestUpRefusesASchemaAheadWithNoRecord is a database migrated by a binary
// that predates the compatibility record (every database today). Nothing
// proves the extra versions are safe, so the older binary refuses, and says
// WHY it cannot tell — "not declared" and "never recorded" call for the same
// runbook but are different facts.
func TestUpRefusesASchemaAheadWithNoRecord(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(merged(olderFiles, compatibleThird)), dsn)
	db, err := sql.Open("pgx/v5", sqlDSN(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("DROP TABLE " + compatTable); err != nil {
		t.Fatalf("simulate a pre-record database: %v", err)
	}

	mg, err := Open(Options{FS: release(olderFiles), Dir: "migrations", DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = mg.Close() }()
	_, err = mg.Up()
	var ahead *SchemaAheadError
	if !errors.As(err, &ahead) || !ahead.Unrecorded {
		t.Fatalf("Up err = %v; want a *SchemaAheadError with Unrecorded set", err)
	}
	if !strings.Contains(err.Error(), "no compatibility record") {
		t.Errorf("error does not say the record is missing: %v", err)
	}
}

// TestAutoMigrateAppliesTheSameVerdictAtBoot covers a server booting with
// AUTO_MIGRATE — an old pod starting mid-rollout on an image that migrates on
// start. It must reach the same two verdicts `db migrate up` does, on the pool
// it is handed.
func TestAutoMigrateAppliesTheSameVerdictAtBoot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		third   map[string]string
		wantErr bool
	}{
		{"compatible schema ahead boots", compatibleThird, false},
		{"incompatible schema ahead refuses to start", incompatibleThird, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := requirePG(t)
			db, err := sql.Open("pgx/v5", sqlDSN(dsn))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if err := AutoMigrate(release(merged(olderFiles, tc.third)), "migrations")(db, nil); err != nil {
				t.Fatalf("newer release boot: %v", err)
			}
			err = AutoMigrate(release(olderFiles), "migrations")(db, nil)
			var ahead *SchemaAheadError
			if tc.wantErr != errors.As(err, &ahead) {
				t.Fatalf("older release boot err = %v; want SchemaAheadError=%v", err, tc.wantErr)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("older release boot err = %v; want nil", err)
			}
		})
	}
}

// TestAutoMigrateTrustsTheDeclarationsTheMigrateJobRecorded is the production
// shape: the NEWER release migrates through the scaffolded `db migrate up`
// (a pre-rollout Job, i.e. Migrator.Up), then an OLDER-release pod boots with
// AutoMigrate before the rollout completes. Boot can only accept the compatible
// version if Up recorded its declaration — a Migrator.Up that applies without
// recording leaves every Job-applied version "unrecorded", and the older
// release refuses to start over a migration its author declared safe.
func TestAutoMigrateTrustsTheDeclarationsTheMigrateJobRecorded(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(merged(olderFiles, compatibleThird)), dsn)

	db, err := sql.Open("pgx/v5", sqlDSN(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := AutoMigrate(release(olderFiles), "migrations")(db, nil); err != nil {
		t.Fatalf("older release boot after a Job-applied compatible migration: %v", err)
	}
}

// TestUpKeepsWorkingWithMigratorOptionsInTheDSN: golang-migrate reads `x-`
// query options (x-migrations-table and friends) out of the DSN. The record's
// own connection must strip them — postgres rejects an unknown run-time
// parameter — or every project that sets one loses `db migrate up` entirely.
func TestUpKeepsWorkingWithMigratorOptionsInTheDSN(t *testing.T) {
	dsn := requirePG(t)
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	res := mustUp(t, release(olderFiles), dsn+sep+"x-multi-statement=false")
	if res.After.Version != 2 {
		t.Errorf("version after up = %d, want 2", res.After.Version)
	}
}

// TestUpRecordsEveryEmbeddedDeclaration pins the forward path's side effect:
// the record is written even when nothing is pending (so a database migrated
// before the record existed gains it on the next deploy), and it reflects the
// declaration of each version.
func TestUpRecordsEveryEmbeddedDeclaration(t *testing.T) {
	dsn := requirePG(t)
	mustUp(t, release(merged(olderFiles, compatibleThird)), dsn)
	res := mustUp(t, release(merged(olderFiles, compatibleThird)), dsn) // nothing pending
	if res.Changed || res.Ahead != nil {
		t.Errorf("second up = %+v; want an ordinary no-op", res)
	}
	db, err := sql.Open("pgx/v5", sqlDSN(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	got, err := onSession(t, db, readCompat)
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint]bool{1: false, 2: false, 3: true}
	if len(got) != len(want) {
		t.Fatalf("record = %v, want %v", got, want)
	}
	for v, ok := range want {
		if got[v] != ok {
			t.Errorf("record[%d] = %v, want %v", v, got[v], ok)
		}
	}
}
