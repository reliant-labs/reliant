// File: internal/cli/db_migration_rebase_test.go
//
// THE DEFECT THESE TESTS PIN. Two separate messages told users to run
// `forge db migration rebase <file>` when no such subcommand existed: the
// migrator's missing-migration refusal (pkg/migratekit) and forge lint's
// version rules. Both fire at the worst possible moment — a deploy refusing
// to apply a migration, or a PR being reviewed — and a user who followed the
// advice got "unknown command for forge db migration".
//
// Prose that names a command is an API. These tests make it one: every
// suggested command is resolved against the real command tree, so advice and
// binary cannot drift apart again.

package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/migrationlint"
	"github.com/reliant-labs/forge/pkg/migratekit"
)

// resolves reports whether a space-separated forge command path (with the
// leading "forge" dropped) is a real command in the tree.
func resolves(t *testing.T, command string) bool {
	t.Helper()
	fields := strings.Fields(command)
	if len(fields) == 0 || fields[0] != "forge" {
		t.Fatalf("command %q must start with \"forge\"", command)
	}
	root := newTestRootCmd(t)
	found, _, err := root.Find(fields[1:])
	if err != nil {
		return false
	}
	// Find returns the deepest command it matched, so a path that ran out
	// partway resolves to an ancestor. Require the full path.
	return found.CommandPath() == command
}

// The command the advice names must exist. This is the whole defect: it did
// not.
func TestRebaseCommandExists(t *testing.T) {
	if !resolves(t, migrationlint.RebaseCommand) {
		t.Fatalf("%q does not resolve to a real command — the lint tells users to run it", migrationlint.RebaseCommand)
	}
}

// pkg/ cannot import forge's internal packages, so the command string is
// spelled twice. Two spellings drift; this is what stops them.
func TestMigratekitAndLintNameTheSameCommand(t *testing.T) {
	if migratekit.RebaseCommand != migrationlint.RebaseCommand {
		t.Fatalf("migratekit.RebaseCommand = %q but migrationlint.RebaseCommand = %q — the migrator and the lint must send users to one command",
			migratekit.RebaseCommand, migrationlint.RebaseCommand)
	}
	if !resolves(t, migratekit.RebaseCommand) {
		t.Fatalf("%q does not resolve to a real command — the migrator's refusal tells users to run it", migratekit.RebaseCommand)
	}
}

// AutoMigrate's dirty refusal names forge's force command. It used to name
// golang-migrate's CLI (`migrate force N`), which forge no longer uses.
func TestMigratekitForceCommandExists(t *testing.T) {
	if !resolves(t, migratekit.ForceCommand) {
		t.Fatalf("%q does not resolve to a real command — AutoMigrate's dirty refusal tells users to run it", migratekit.ForceCommand)
	}
}

// The refusal a user actually reads, checked as rendered text rather than as
// a constant. The constant being right is worthless if the message is built
// from a hardcoded copy of it, which is how this broke the first time.
func TestMissingMigrationErrorSuggestsTheRealCommand(t *testing.T) {
	err := &migratekit.MissingMigrationError{
		Missing: []migratekit.MissingMigration{{Version: 20260101120000, Name: "20260101120000_add_users.up.sql"}},
		Version: 20260501000000,
	}
	msg := err.Error()

	suggested := extractBacktickedForgeCommand(t, msg)
	if suggested == "" {
		t.Fatalf("refusal names no `forge ...` command, so a user has nothing to run; got:\n%s", msg)
	}
	if !resolves(t, suggested) {
		t.Errorf("refusal suggests %q, which is not a real command; got:\n%s", suggested, msg)
	}
	// The second fix — replacing a file that already merged — names the
	// create command, held to the same standard.
	if !strings.Contains(msg, "`"+migratekit.NewMigrationCommand+" <name>`") {
		t.Errorf("refusal does not carry the merged-file fix (`%s <name>`); got:\n%s", migratekit.NewMigrationCommand, msg)
	}
	if !resolves(t, migratekit.NewMigrationCommand) {
		t.Errorf("migratekit.NewMigrationCommand %q is not a real command", migratekit.NewMigrationCommand)
	}
}

// The lint remediation a reviewer reads, same check.
func TestVersionRemediationsSuggestTheRealCommand(t *testing.T) {
	for _, rule := range []string{
		migrationlint.RuleNonTimestampVersion,
		migrationlint.RuleDuplicateVersion,
		migrationlint.RuleNonTransactionalStatement,
		migrationlint.RuleEnumValueUsedBeforeCommit,
	} {
		remediation := migrationlint.RemediationFor(rule)
		suggested := extractBacktickedForgeCommand(t, remediation)
		if suggested == "" {
			t.Errorf("remediation for %q names no `forge ...` command; got:\n%s", rule, remediation)
			continue
		}
		if !resolves(t, suggested) {
			t.Errorf("remediation for %q suggests %q, which is not a real command; got:\n%s", rule, suggested, remediation)
		}
	}
}

// extractBacktickedForgeCommand pulls the first backticked `forge ...`
// command out of a message and strips any trailing <placeholder> arguments,
// leaving the command path to resolve.
func extractBacktickedForgeCommand(t *testing.T, msg string) string {
	t.Helper()
	for _, segment := range strings.Split(msg, "`") {
		if !strings.HasPrefix(segment, "forge ") {
			continue
		}
		var path []string
		for _, field := range strings.Fields(segment) {
			// Stop at the first argument or flag: <file>, --all-pending,
			// "$DATABASE_URL". What remains is the command path.
			if strings.HasPrefix(field, "<") || strings.HasPrefix(field, "-") || strings.HasPrefix(field, "\"") || strings.HasPrefix(field, "$") {
				break
			}
			path = append(path, field)
		}
		if len(path) > 1 {
			return strings.Join(path, " ")
		}
	}
	return ""
}

// `forge db migration --help` must not describe the numbering scheme it
// abandoned. It claimed migrations were "sequentially-numbered", "continuing
// the project's existing numbering (00001_, 00002_, …)", long after the
// allocator switched to UTC timestamps — advice that, followed, recreates the
// exact cross-branch collision timestamps exist to prevent.
func TestMigrationHelpDescribesTimestampVersions(t *testing.T) {
	help := helpText(t, "migration")

	for _, stale := range []string{"sequentially-numbered", "00001_", "00002_"} {
		if strings.Contains(help, stale) {
			t.Errorf("`forge db migration --help` still describes sequential numbering (%q); versions are UTC timestamps:\n%s", stale, help)
		}
	}
	for _, want := range []string{"UTC", "YYYYMMDDHHMMSS"} {
		if !strings.Contains(help, want) {
			t.Errorf("`forge db migration --help` must mention %q so nobody hand-types a version; got:\n%s", want, help)
		}
	}
	// The help must name rebase, since it is the other half of version
	// management and the thing users are sent here to find.
	if !strings.Contains(help, "rebase") {
		t.Errorf("`forge db migration --help` does not mention rebase; got:\n%s", help)
	}
}

// `rebase --help` has to say what it refuses and why, because the refusal is
// the surprising part: a user whose deploy is blocked wants the rename, and
// needs to understand why one specific file cannot have it.
func TestRebaseHelpNamesWhatItRefuses(t *testing.T) {
	help := helpText(t, "migration", "rebase")

	for _, want := range []string{"default branch", "refuses"} {
		if !strings.Contains(strings.ToLower(help), want) {
			t.Errorf("`migration rebase --help` must mention %q; got:\n%s", want, help)
		}
	}
}
