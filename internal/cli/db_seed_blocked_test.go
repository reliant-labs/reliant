// File: internal/cli/db_seed_blocked_test.go
//
// The seed commands refuse to run against a database whose migration state
// they cannot trust. There are TWO such states and they need different advice:
//
//   - PENDING — migrations exist on disk that the database has not applied.
//     `forge db migrate up` is exactly right.
//   - DIRTY — a migration failed part-way and golang-migrate recorded the
//     failure. `forge db migrate up` will refuse again, so advising it sends
//     the user in a circle.
//
// Measured, in a dogfood run: the dirty case advised `migrate up`, the user
// looped, and escaped by hand-editing schema_migrations with raw SQL
// (`UPDATE schema_migrations SET dirty = false, version = 9`) plus hand
// repairs to the rows. `forge db migrate force <version>` already existed —
// the message simply never named it. That is the failure this file pins: a
// refusal that states a problem and no next step is how a user ends up doing
// manual surgery on their own database.

package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/seedplan"
)

// The dirty-state refusal must name `forge db migrate force` AND the version
// to force to. Naming the command without the number still leaves the user
// guessing, which is most of the original dead end.
func TestSeedBlockedMessage_DirtyNamesMigrateForce(t *testing.T) {
	msg := seedBlockedMessage(&seedplan.MigrationBlock{
		Dirty:   true,
		Version: "9",
		Latest:  "11",
	})

	for _, want := range []string{
		"dirty",
		"forge db migrate force 9",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the dirty refusal must contain %q; got:\n%s", want, msg)
		}
	}

	// `migrate up` is the WRONG advice here — it fails again on a dirty
	// version. It may appear as the follow-up step once the flag is cleared,
	// but it must not be the instruction the user is left with.
	force := strings.Index(msg, "forge db migrate force")
	up := strings.Index(msg, "forge db migrate up")
	if up >= 0 && up < force {
		t.Errorf("the dirty refusal leads with `migrate up`, which fails again on a dirty version; got:\n%s", msg)
	}
}

// The pending case is a different problem with a different fix, and it must
// not inherit the dirty case's repair instructions — telling someone to force
// a version when nothing is broken invites them to skip a migration.
func TestSeedBlockedMessage_PendingKeepsMigrateUp(t *testing.T) {
	msg := seedBlockedMessage(&seedplan.MigrationBlock{
		Reason:  "applied version 9 is behind latest on disk 11",
		Version: "9",
		Latest:  "11",
	})

	if !strings.Contains(msg, "forge db migrate up") {
		t.Errorf("the pending refusal must name `forge db migrate up`; got:\n%s", msg)
	}
	if strings.Contains(msg, "migrate force") {
		t.Errorf("the pending refusal must NOT advise forcing a version — nothing is broken; got:\n%s", msg)
	}
}

// A wedged dev database has a second exit that two dogfood runs both failed to
// find: `forge db seed reset`. One of them hand-rolled
// `DROP DATABASE ... WITH (FORCE)` four separate times instead.
//
// But `seed reset` runs this very check (openSeedDB with checkPending), so on
// a DIRTY database it refuses identically. Naming it as the immediate fix
// would be a second dead end wearing the first one's clothes. It is named as
// the step AFTER the flag is cleared, and the ordering is the assertion.
func TestSeedBlockedMessage_DirtyOrdersForceBeforeReset(t *testing.T) {
	msg := seedBlockedMessage(&seedplan.MigrationBlock{Dirty: true, Version: "9", Latest: "11"})

	force := strings.Index(msg, "forge db migrate force")
	reset := strings.Index(msg, "forge db seed reset")
	if force < 0 || reset < 0 {
		t.Fatalf("the dirty refusal must name both `migrate force` and `seed reset`; got:\n%s", msg)
	}
	if reset < force {
		t.Errorf("`seed reset` runs this same check and refuses on a dirty database — it must come AFTER `migrate force`; got:\n%s", msg)
	}
}
