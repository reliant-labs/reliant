package cli

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/seedplan"
)

// dirtyRecoveryMessage renders the recovery path for a database whose
// migration state golang-migrate has marked dirty. It is the ONE source for
// that advice: every command that can hit the wall renders from here, so they
// cannot drift into two different recommendations for one situation.
//
// That drift is not hypothetical — it is this bug. The numbered path below
// shipped attached only to the seed commands, while `forge db migrate up`, the
// command an operator is far more likely to hit first, passed golang-migrate's
// raw "Dirty database version 6. Fix and force version." straight through: no
// forge command named, no mention that forcing runs no SQL.
//
// lead is the command-specific refusal prefix. steps are the numbered actions
// AFTER the two shared ones (inspect, then force) — they differ per command,
// because a `migrate up` caller wants to re-run `up` while a seed caller wants
// to catch up and then re-seed. tail is optional command-specific closing
// advice.
//
// Ordering is load-bearing in the shared part. The schema repair is step 1
// because forcing asserts a fact rather than verifying one: forge cannot know
// how much of the failed migration landed, so anything that clears the flag
// before the schema is repaired records a lie.
func dirtyRecoveryMessage(lead, version string, steps []string, tail string) string {
	if version == "" {
		version = "<version>"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%smigration %s failed part-way and is marked dirty, so the schema is in an unknown state and no further migration will run until that flag is cleared.\n\nTo recover:\n\n", lead, version)
	fmt.Fprintf(&b, "  1. Inspect what migration %s actually applied (`forge db introspect`), and finish or undo it by hand so the schema matches what %s intended.\n", version, version)
	fmt.Fprintf(&b, "  2. Clear the flag:  forge db migrate force %s\n", version)
	for i, step := range steps {
		fmt.Fprintf(&b, "  %d. %s\n", i+3, step)
	}
	fmt.Fprintf(&b, "\nStep 2 is the one that unwedges this: golang-migrate will not run a migration over a dirty version, so re-running the same command on its own refuses again. Forcing records %s as applied WITHOUT running any SQL — which is why step 1 comes first: forge cannot know how much of %s landed, so you are asserting the schema is correct, not asking forge to verify it.\n", version, version)
	if tail != "" {
		fmt.Fprintf(&b, "\n%s", tail)
	}
	return b.String()
}

// dirtyMigrationMessage renders the refusal for `forge db migrate <action>`
// against an already-dirty database, or "" when the state is clean and the
// underlying failure should be passed through untouched.
//
// Returning "" for a clean state is the important half. A migration whose SQL
// is simply broken also fails — and golang-migrate marks it dirty as it does
// so. Advising `force` there would tell the author to record a migration as
// applied when its SQL never ran, leaving the schema permanently behind what
// forge believes is applied. That is strictly worse than the raw error, so the
// recovery text is attached only to a database that was ALREADY wedged before
// the command ran.
func dirtyMigrationMessage(block *seedplan.MigrationBlock, action string) string {
	if block == nil || !block.Dirty {
		return ""
	}
	retry := strings.TrimSpace("forge db migrate " + action)
	return dirtyRecoveryMessage(
		fmt.Sprintf("refusing to run `migrate %s`: ", action),
		block.Version,
		[]string{"Re-run:          " + retry},
		"If this is a scratch dev database whose contents do not matter, skip all of it: `forge db reset` DROPs the database, recreates it, migrates to head and seeds. There is no dirty flag to clear and no schema to repair when the database is new — which is why it is the one exit that cannot be refused by the state you are trying to escape.",
	)
}
