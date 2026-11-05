//go:build integration

// File: internal/cli/db_squash_integration_test.go
//
// `forge db squash` end to end: squash a history in squash's own ephemeral
// postgres container, then run the baseline and the next migration through
// migratekit in one run (assertSquashedReleaseApplies, in
// db_squash_dump_test.go).
//
// Tagged integration because it runs the real docker: the default build's
// host-tool tripwire (hermetic_host_test.go) refuses that, and rightly — a
// unit test does not start containers on a shared machine. The container is
// squash's own, named forge-db-squash-<nanos>, started with --rm and stopped
// by squash on every exit path.

package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// squashHistory is the history testdata/squash/pg_dump_16.15_inserts.sql was
// dumped from.
var squashHistory = map[string]string{
	"20260101000001_init.up.sql": `CREATE TABLE accounts (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL, note TEXT);
COMMENT ON TABLE accounts IS 'customer accounts; SET x = 1';
CREATE TYPE plan_kind AS ENUM ('free', 'pro');
CREATE FUNCTION touch() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
SET LOCAL search_path = public;
  NEW.name := NEW.name || $$x$$;
  NEW.note := coalesce(NEW.note, 'a
\restrict not-a-meta');
  RETURN NEW;
END
$fn$;
CREATE FUNCTION account_count() RETURNS bigint LANGUAGE sql AS $$
SELECT count(*) FROM accounts
$$;
CREATE TRIGGER accounts_touch BEFORE INSERT ON accounts FOR EACH ROW EXECUTE FUNCTION touch();
`,
	"20260101000002_plans.up.sql": `CREATE TABLE plans (id INT PRIMARY KEY, kind plan_kind NOT NULL, account_id BIGINT REFERENCES accounts (id));
INSERT INTO accounts (name, note) VALUES ('acme', E'line one\nSET search_path = evil;\n\\restrict nope\nend');
`,
}

func TestDBSquash_BaselineAndTheNextMigrationApplyInOneMigratekitRun(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a postgres container for squash and a pgtest database")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
	history := t.TempDir()
	for name, body := range squashHistory {
		if err := os.WriteFile(filepath.Join(history, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	release := t.TempDir()
	if err := runDBSquash(context.Background(), squashOptions{
		FromDir:  history,
		Baseline: "20260102000000_baseline",
		OutDir:   release,
		Image:    "postgres:16-alpine",
		DBName:   "forge_squash",
		DBUser:   "postgres",
		DBPass:   "forge_squash",
	}); err != nil {
		t.Fatalf("squash: %v", err)
	}
	if err := os.WriteFile(filepath.Join(release, "20260103000000_after.up.sql"), []byte(afterSquash), 0o644); err != nil {
		t.Fatal(err)
	}
	assertSquashedReleaseApplies(t, os.DirFS(release))
}

// The baseline squash writes is adopted by the database its history built:
// the real squash, then `migrate up` of the release over a database the
// squashed migrations migrated. Its SQL must not run — CREATE TYPE and CREATE
// TABLE would fail there — and the migration after it must.
func TestDBSquash_TheDatabaseItsHistoryBuiltAdoptsTheBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a postgres container for squash and a pgtest database")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
	history := t.TempDir()
	for name, body := range squashHistory {
		if err := os.WriteFile(filepath.Join(history, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dsn := scratchDatabase(t)
	var out bytes.Buffer
	if err := runMigrateUp(&out, dsn, history); err != nil {
		t.Fatalf("build the database from the history: %v\n%s", err, out.String())
	}

	release := t.TempDir()
	if err := runDBSquash(context.Background(), squashOptions{
		FromDir:  history,
		Baseline: "20260102000000_baseline",
		OutDir:   release,
		Image:    "postgres:16-alpine",
		DBName:   "forge_squash",
		DBUser:   "postgres",
		DBPass:   "forge_squash",
	}); err != nil {
		t.Fatalf("squash: %v", err)
	}
	if err := os.WriteFile(filepath.Join(release, "20260103000000_after.up.sql"), []byte(afterSquash), 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := runMigrateUp(&out, dsn, release); err != nil {
		t.Fatalf("migrate up of the squashed release over the database its history built: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"ADOPTED a database the forge migrations 20260102000000_baseline.up.sql squashed had built",
		"2 forge versions verified",
		"migrations applied: 20260102000000 -> 20260103000000",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("migrate up output lacks %q:\n%s", want, out.String())
		}
	}
	db := openScratch(t, dsn)
	if n := countRows(t, db, `SELECT count(*) FROM accounts`); n != 1 {
		t.Errorf("accounts holds %d rows, want the history's one: the baseline's INSERT ran on an adopted database", n)
	}
}

// Two squashes of one history write the same bytes. The header used to carry
// the wall clock and the absolute --from-dir, so a re-cut never reproduced the
// file and a reviewer could not tell "re-cut" from "changed" (reliant stripped
// the header with `tail -n +6`). Each run here squashes from a different
// directory, as two checkouts would.
func TestDBSquash_IsByteForByteReproducible(t *testing.T) {
	if testing.Short() {
		t.Skip("boots two postgres containers for squash")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
	var baselines [2][]byte
	for i := range baselines {
		history := t.TempDir()
		for name, body := range squashHistory {
			if err := os.WriteFile(filepath.Join(history, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		release := t.TempDir()
		if err := runDBSquash(context.Background(), squashOptions{
			FromDir:  history,
			Baseline: "20260102000000_baseline",
			OutDir:   release,
			Image:    "postgres:16-alpine",
			DBName:   "forge_squash",
			DBUser:   "postgres",
			DBPass:   "forge_squash",
		}); err != nil {
			t.Fatalf("squash %d: %v", i+1, err)
		}
		b, err := os.ReadFile(filepath.Join(release, "20260102000000_baseline.up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(history)) {
			t.Errorf("squash %d wrote its source directory into the baseline", i+1)
		}
		baselines[i] = b
	}
	if !bytes.Equal(baselines[0], baselines[1]) {
		a, b := strings.Split(string(baselines[0]), "\n"), strings.Split(string(baselines[1]), "\n")
		for i := 0; i < len(a) && i < len(b); i++ {
			if a[i] != b[i] {
				t.Fatalf("two squashes of one history differ, first at line %d:\n  %s\n  %s", i+1, a[i], b[i])
			}
		}
		t.Fatalf("two squashes of one history differ in length: %d vs %d lines", len(a), len(b))
	}
}
