// File: internal/cli/db_migrate_dirty_test.go
//
// The numbered dirty-migration recovery path shipped attached only to the seed
// commands. But the command an operator actually hits the wall with is
// `forge db migrate up`, and that one passed golang-migrate's raw string
// straight through:
//
//	error: Dirty database version 6. Fix and force version.
//	Error: migrate up failed: exit status 1
//
// That text names no forge command, never mentions `forge db migrate force`,
// and never says that forcing runs no SQL. So the dead end the seed message
// closed was still wide open on the most likely path into it.
//
// Two things are pinned here, and the second matters as much as the first:
//
//  1. A database that is ALREADY dirty when `migrate up`/`down` is invoked
//     gets the same recovery path the seed commands give.
//  2. A CLEAN failure — a genuinely broken migration, run against a database
//     with no dirty flag — does NOT. Routing someone to `force` when their SQL
//     is simply wrong tells them to record a migration as applied that never
//     ran, which is worse than the raw golang-migrate string.

package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/seedplan"
)

// The migrate-path refusal has to carry the same load-bearing content as the
// seed one: the command, the version to force to, and the fact that forcing
// runs no SQL (which is why the schema repair comes first).
func TestDirtyMigrationMessage_NamesMigrateForceAndVersion(t *testing.T) {
	msg := dirtyMigrationMessage(&seedplan.MigrationBlock{
		Dirty:   true,
		Version: "6",
		Latest:  "11",
	}, "up")

	for _, want := range []string{
		"dirty",
		"forge db migrate force 6",
		"WITHOUT running any SQL",
		"forge db introspect",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the dirty `migrate up` refusal must contain %q; got:\n%s", want, msg)
		}
	}

	// Same ordering rule as the seed message: `migrate up` is what the user
	// just ran and what fails again, so it may appear as the follow-up step
	// but must never lead.
	force := strings.Index(msg, "forge db migrate force")
	up := strings.Index(msg, "forge db migrate up")
	if up >= 0 && up < force {
		t.Errorf("the refusal leads with `migrate up`, which is the command that just failed; got:\n%s", msg)
	}
}

// THE MISROUTING GUARD. A migration whose SQL is simply broken fails against a
// database with a clean flag. golang-migrate then MARKS it dirty, which is why
// this check runs before the migration rather than after: a post-hoc dirty
// query cannot tell "was already wedged" from "I just wedged it", and would
// advise forcing past a migration that never ran.
func TestDirtyMigrationMessage_CleanStateGetsNoRecoveryText(t *testing.T) {
	cases := map[string]*seedplan.MigrationBlock{
		"fully migrated (no block at all)": nil,
		"pending, nothing broken": {
			Version: "6",
			Latest:  "11",
			Reason:  "applied version 6 is behind latest on disk 11",
		},
		"no migrations applied yet": {
			Latest: "11",
			Reason: "no migrations applied; latest on disk is 11",
		},
	}

	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			if msg := dirtyMigrationMessage(block, "up"); msg != "" {
				t.Errorf("a clean migration state must pass the raw failure through untouched, not advise forcing a version; got:\n%s", msg)
			}
		})
	}
}

// The seed path and the migrate path must render from ONE source. Two copies
// of this advice would drift into two different recommendations for one
// situation, which is the failure mode that produced this bug in the first
// place (the seed copy was fixed; the migrate path had no copy at all).
func TestDirtyRecovery_SeedAndMigrateShareOneSource(t *testing.T) {
	block := &seedplan.MigrationBlock{Dirty: true, Version: "9", Latest: "11"}
	seed := seedBlockedMessage(block)
	migrate := dirtyMigrationMessage(block, "up")

	for _, shared := range []string{
		"forge db migrate force 9",
		"WITHOUT running any SQL",
		"forge db introspect",
	} {
		if !strings.Contains(seed, shared) || !strings.Contains(migrate, shared) {
			t.Errorf("both refusals must carry %q — they render from one source; seed:\n%s\n\nmigrate:\n%s", shared, seed, migrate)
		}
	}

	// The seed-specific NUMBERED steps belong to seed only. A `migrate up`
	// caller has not asked to seed anything, so a "re-seed" step would be noise.
	if strings.Contains(migrate, "forge db seed apply") {
		t.Errorf("the migrate refusal should not tell the user to re-seed; got:\n%s", migrate)
	}
	if !strings.Contains(seed, "forge db seed apply") {
		t.Errorf("the seed refusal lost its re-seed step; got:\n%s", seed)
	}
}

// `forge db reset` is the one exit that cannot be refused by the state the user
// is trying to escape: it DROPs and rebuilds, so there is no dirty flag left to
// clear and no partial schema left to repair. Every other escape — including
// `seed reset`, which runs this same check — can refuse on the very condition
// being escaped. It belongs on both paths, and it must stay BELOW the numbered
// repair steps: it discards data, so a reader whose database matters has to see
// the repair route first.
func TestDirtyRecovery_OffersResetAsTheUnrefusableExit(t *testing.T) {
	block := &seedplan.MigrationBlock{Dirty: true, Version: "9", Latest: "11"}

	for name, msg := range map[string]string{
		"migrate": dirtyMigrationMessage(block, "up"),
		"seed":    seedBlockedMessage(block),
	} {
		t.Run(name, func(t *testing.T) {
			reset := strings.Index(msg, "forge db reset")
			if reset < 0 {
				t.Fatalf("the dirty refusal must name `forge db reset`; got:\n%s", msg)
			}
			force := strings.Index(msg, "forge db migrate force")
			if reset < force {
				t.Errorf("`forge db reset` discards data — it must come AFTER the repair steps; got:\n%s", msg)
			}
		})
	}
}
