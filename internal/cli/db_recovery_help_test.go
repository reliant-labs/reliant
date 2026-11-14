// File: internal/cli/db_recovery_help_test.go
//
// Two commands that already existed went unfound across two dogfood runs.
// `forge db seed reset` was never located at all — one run hand-rolled
// `DROP DATABASE ... WITH (FORCE)` four separate times instead — and
// `forge db migrate force` was found only after the author had already
// repaired schema_migrations with raw SQL.
//
// Both were visible in `forge db --help` the whole time, as one-word entries
// in a command list. That is the gap these tests pin: a stuck user is not
// scanning for a NOUN they do not know to want, they are looking for the
// situation they are in. So the help has to name the situation — "a migration
// failed part-way", "the dev database is wedged" — and say which command
// answers it.

package cli

import (
	"strings"
	"testing"
)

// helpText renders a command's long help the way a user reading --help sees it.
func helpText(t *testing.T, path ...string) string {
	t.Helper()
	cmd := newDBCmd()
	for _, name := range path {
		found := false
		for _, sub := range cmd.Commands() {
			if sub.Name() == name {
				cmd, found = sub, true
				break
			}
		}
		if !found {
			t.Fatalf("no subcommand %q under %q", name, cmd.CommandPath())
		}
	}
	return cmd.Long + "\n" + cmd.Short
}

// `forge db --help` is where someone goes when the database is wedged and they
// do not yet know which command they need. It must name the recovery path, not
// merely list the commands that implement it.
func TestDBHelp_NamesTheRecoveryPath(t *testing.T) {
	help := helpText(t)

	for _, want := range []string{
		// The situation, in the words a stuck user would use.
		"dirty",
		// The two commands that were missed.
		"forge db migrate force",
		"forge db seed reset",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("`forge db --help` must mention %q so a wedged user can find the way out; got:\n%s", want, help)
		}
	}
}

// `migrate force` is the escape hatch that existed and was not named. Its own
// help must say WHEN to reach for it and what it does not do — forcing records
// a version without running SQL, so the schema repair is still the author's
// job.
func TestMigrateForceHelp_SaysWhenToReachForIt(t *testing.T) {
	help := helpText(t, "migrate", "force")

	for _, want := range []string{"dirty", "without running"} {
		if !strings.Contains(strings.ToLower(help), want) {
			t.Errorf("`migrate force --help` must mention %q; got:\n%s", want, help)
		}
	}
}

// `seed reset` is the command two runs never found. Its help must name the
// situation it answers — and must not imply it fixes a dirty database, which
// it does not: it runs the same migration-state check and refuses identically.
func TestSeedResetHelp_NamesTheSituation(t *testing.T) {
	help := helpText(t, "seed", "reset")
	if !strings.Contains(strings.ToLower(help), "drop") {
		t.Errorf("`seed reset --help` must name itself as the alternative to dropping the database by hand; got:\n%s", help)
	}
}
