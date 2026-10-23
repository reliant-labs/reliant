package migratekit

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// TestDiffAdoptionIsSetMembershipNotAHighWaterMark: goose applies out of
// order, so a database can hold the NEWEST listed version and still lack an
// older one. A comparison of maxima would adopt it; set membership must not.
func TestDiffAdoptionIsSetMembershipNotAHighWaterMark(t *testing.T) {
	declared := []int64{10, 20, 30}
	cases := []struct {
		name                string
		applied             []int64
		missing, unexpected []int64
	}{
		{name: "exactly the history", applied: []int64{30, 10, 20}},
		{name: "newest applied, a middle one skipped", applied: []int64{10, 30}, missing: []int64{20}},
		{name: "behind", applied: []int64{10}, missing: []int64{20, 30}},
		{name: "nothing applied", missing: []int64{10, 20, 30}},
		{name: "a version the baseline does not list", applied: []int64{10, 20, 25, 30}, unexpected: []int64{25}},
		{name: "both", applied: []int64{10, 40}, missing: []int64{20, 30}, unexpected: []int64{40}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applied := map[int64]bool{}
			for _, v := range tc.applied {
				applied[v] = true
			}
			missing, unexpected := diffAdoption(declared, applied)
			if !reflect.DeepEqual(missing, tc.missing) || !reflect.DeepEqual(unexpected, tc.unexpected) {
				t.Errorf("diff = missing %v, unexpected %v; want missing %v, unexpected %v", missing, unexpected, tc.missing, tc.unexpected)
			}
		})
	}
}

// TestAdoptionErrorIsTheRunbook: the refusal is read by whoever is holding a
// stalled rollout or a desktop app that will not start, so it must say what
// is wrong in each direction and how to fix it — and stay readable when a
// database is behind by an entire history.
func TestAdoptionErrorIsTheRunbook(t *testing.T) {
	behind := make([]int64, 30)
	for i := range behind {
		behind[i] = int64(i + 1)
	}
	err := &AdoptionError{
		Tool: "goose", Table: gooseTable, Baseline: "20260201000000_baseline.up.sql", Declared: 40,
		Missing: behind, Unexpected: []int64{99},
	}
	msg := err.Error()
	for _, want := range []string{
		"20260201000000_baseline.up.sql",
		"NOT applied 30 of the 40 goose migration(s)",
		"run the last release that still migrates with goose",
		"… and 18 more", // 30 missing, 12 printed
		"HAS applied 1 goose migration(s) the baseline does not list: 99",
		"DELETE FROM goose_db_version WHERE version_id IN",
		"NOTHING WAS APPLIED",
		"db/deploy-migrations",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("AdoptionError does not say %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, " 13,") {
		t.Errorf("AdoptionError printed past its cap:\n%s", msg)
	}
}

// TestForgeAdoptionErrorIsTheRunbook: a squashed forge history is refused
// with forge's remedies — the release before the squash finishes a history
// that is behind, and a list cut too early is re-cut with `forge db squash`,
// never goose's table and goose's release.
func TestForgeAdoptionErrorIsTheRunbook(t *testing.T) {
	behind := (&AdoptionError{
		Tool: "forge", Table: appliedTable, Baseline: "20260201000000_baseline.up.sql", Declared: 2,
		Missing: []int64{20260101000002},
	}).Error()
	for _, want := range []string{
		"built by forge migrations, and its history is not the one 20260201000000_baseline.up.sql squashed",
		"NOT applied 1 of the 2 migration(s) the baseline lists: 20260101000002",
		"run the release before the squash against this database first",
		"NOTHING WAS APPLIED",
		"Squashing a forge history",
	} {
		if !strings.Contains(behind, want) {
			t.Errorf("behind: AdoptionError does not say %q:\n%s", want, behind)
		}
	}
	unlisted := (&AdoptionError{
		Tool: "forge", Table: appliedTable, Baseline: "20260201000000_baseline.up.sql", Declared: 2,
		Unexpected: []int64{20260101000003},
	}).Error()
	for _, want := range []string{
		"its schema is at version 20260101000003, which the baseline does not list",
		"re-run `forge db squash` over the full history",
		"`-- forge:adopts forge` lines include it",
	} {
		if !strings.Contains(unlisted, want) {
			t.Errorf("unlisted: AdoptionError does not say %q:\n%s", want, unlisted)
		}
	}
	for _, msg := range []string{behind, unlisted} {
		if strings.Contains(msg, "goose") {
			t.Errorf("a forge refusal talks about goose:\n%s", msg)
		}
	}
}

// TestForeignHistoryErrorIsNotAheadOrMissing: the database a pre-squash
// history built, meeting a baseline that does not declare it, used to be
// told it was AHEAD (baseline above the history) or that the baseline was
// MISSING and needed a newer version (baseline below it). Both sent the
// reader the wrong way.
func TestForeignHistoryErrorIsNotAheadOrMissing(t *testing.T) {
	msg := (&ForeignHistoryError{Version: 20260101000002, First: "20260201000000_baseline.up.sql", Latest: 20260201000000, Embedded: 1}).Error()
	for _, want := range []string{
		"database schema is at version 20260101000002",
		"applied none of the 1 migration(s) this binary embeds",
		"a migration history this binary does not carry",
		"forge db squash",
		"`-- forge:adopts forge <version> ...`",
		"NOTHING WAS APPLIED",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("ForeignHistoryError does not say %q:\n%s", want, msg)
		}
	}
	for _, not := range []string{"AHEAD", "can NEVER be applied", "NEWER than"} {
		if strings.Contains(msg, not) {
			t.Errorf("ForeignHistoryError says %q:\n%s", not, msg)
		}
	}
}

func TestAdoptedString(t *testing.T) {
	forge := (&Adopted{Tool: "forge", Version: 20260201000000, Name: "20260201000000_baseline.up.sql", Versions: 2}).String()
	if want := "ADOPTED a database the forge migrations 20260201000000_baseline.up.sql squashed had built"; !strings.HasPrefix(forge, want) {
		t.Errorf("forge adoption line = %q; want it to start %q", forge, want)
	}
	if !strings.Contains(forge, "2 forge versions verified") {
		t.Errorf("forge adoption line does not count the versions: %q", forge)
	}
	goose := (&Adopted{Tool: "goose", Version: 20260201000000, Name: "b.up.sql", Versions: 3}).String()
	if want := "ADOPTED a database goose migrated: b.up.sql recorded as applied without running its SQL (3 goose versions verified; goose's own table left in place)"; goose != want {
		t.Errorf("goose adoption line = %q; want %q", goose, want)
	}
}

// TestOpenRefusesAForgeAdoptionListingACarriedVersion: a squash replaces the
// migrations it lists, so a listed version the binary still embeds — the
// baseline's own number, or a squashed file left beside it — is a
// contradiction the migrator refuses on every database, at Open.
func TestOpenRefusesAForgeAdoptionListingACarriedVersion(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"the baseline's own version": {
			"migrations/20260101000002_baseline.up.sql": {Data: []byte("-- forge:adopts forge 20260101000001 20260101000002\nCREATE TABLE a (id INT);")},
		},
		"a squashed file left beside it": {
			"migrations/20260101000000_baseline.up.sql": {Data: []byte("-- forge:adopts forge 20260101000001 20260101000002\nCREATE TABLE a (id INT);")},
			"migrations/20260101000002_plans.up.sql":    {Data: []byte("CREATE TABLE b (id INT);")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Open(Options{FS: fsys, Dir: "migrations", DSN: "postgres://unused@127.0.0.1:1/x"})
			if err == nil || !strings.Contains(err.Error(), "which this binary still carries") || !strings.Contains(err.Error(), "20260101000002") {
				t.Fatalf("Open err = %v; want a refusal naming the carried version", err)
			}
		})
	}
}

// TestOpenRefusesAnAdoptionAfterTheFirstMigration: a database another tool
// migrated has run none of this project's migrations, so only the first one
// can already be reflected in it. A declaration further down would record the
// earlier ones as applied on a database that never ran them. Refused at Open,
// before any database is touched, on every database — so it fails the first
// test run.
func TestOpenRefusesAnAdoptionAfterTheFirstMigration(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/20260101000000_first.up.sql":    {Data: []byte("CREATE TABLE a (id INT);")},
		"migrations/20260201000000_baseline.up.sql": {Data: []byte("-- forge:adopts goose 1\nCREATE TABLE b (id INT);")},
	}
	_, err := Open(Options{FS: fsys, Dir: "migrations", DSN: "postgres://unused@127.0.0.1:1/x"})
	if err == nil || !strings.Contains(err.Error(), "only the FIRST migration") || !strings.Contains(err.Error(), "20260201000000_baseline.up.sql") {
		t.Fatalf("Open err = %v; want a refusal naming the misplaced baseline", err)
	}
}
