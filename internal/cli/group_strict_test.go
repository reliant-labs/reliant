// File: internal/cli/group_strict_test.go
//
// Regression coverage for the exit-0-on-unknown-subcommand wart.
//
// Cobra's default for a command that hosts subcommands is to accept ANY
// leftover argument: a non-runnable parent prints help and returns nil, a
// runnable parent hands the token to its own RunE, which typically ignores
// it. Either way `forge <group> <typo>` reported SUCCESS while doing nothing
// the user asked for — the same defect as a success message that proves
// nothing, spelled with an exit code. A verb typed with a spelling forge no
// longer has exits 0 for exactly this reason.
//
// cmdutil.StrictGroup is the fix. These tests are the enforcement: the first
// walks the ASSEMBLED tree so a group added tomorrow cannot forget, and the
// second proves the behavior end to end through cobra's real dispatch.

package cli

import (
	"strings"
	"testing"
)

// permissiveParents lists parent commands that still carry cobra's default
// arg handling, with the reason. Every entry is a known gap, not an
// exemption: the test below fails BOTH when a command outside this list is
// permissive AND when a command inside it has been fixed, so the list can
// only shrink.
// EMPTY, and that is the goal state: every parent in the tree rejects an
// unknown subcommand. `forge doctor` was the last entry — it was runnable
// with a RunE that ignored positionals, so `forge doctor bogus` ran the full
// diagnostic and exited on the diagnostic's result. It now declares
// cobra.NoArgs. Add an entry here only with a reason and an owner, and delete
// it the moment the command is fixed; the test enforces both directions.
var permissiveParents = map[string]string{}

// TestEveryCommandGroupRejectsUnknownSubcommands walks the assembled tree
// and requires every command that hosts subcommands to declare an Args
// validator. A nil Args on a parent is cobra's permissive default.
func TestEveryCommandGroupRejectsUnknownSubcommands(t *testing.T) {
	tree := walkTree(t)

	seen := map[string]bool{}
	for path, cmd := range tree {
		if len(cmd.Commands()) == 0 {
			continue
		}
		_, allowed := permissiveParents[path]
		if allowed {
			seen[path] = true
		}
		switch {
		case cmd.Args == nil && !allowed:
			t.Errorf("`forge %s` hosts %d subcommands but declares no Args validator — "+
				"cobra will accept `forge %s <typo>` and exit 0. "+
				"Wrap the parent in cmdutil.StrictGroup (or declare Args if it has its own RunE).",
				path, len(cmd.Commands()), path)
		case cmd.Args != nil && allowed:
			t.Errorf("`forge %s` now declares Args — remove it from permissiveParents", path)
		}
	}
	for path := range permissiveParents {
		if !seen[path] {
			t.Errorf("permissiveParents lists %q, which is not a parent command in the tree — stale entry", path)
		}
	}
}

// TestUnknownSubcommandExitsNonZero drives the real cobra dispatch for a
// representative group from every construction style in the repo: a flat
// internal/cli group (`db`), a nested one (`db migrate`), a dir-nested
// factory group (`debug`), and the noun that started this (`project`).
//
// `test` used to be listed here as "the runnable parent that swallowed its
// argument". The command is gone (the suite lives in the project's
// Taskfile.yml) and what stands in its place only ever returns an error, so
// there is no swallow left to guard — see
// TestRemovedTestCommandNamesItsReplacement.
func TestUnknownSubcommandExitsNonZero(t *testing.T) {
	// t.Chdir keeps a regressed arg check from shelling out to a real
	// toolchain — outside a project there is nothing to run, and the
	// assertion below still catches the regression.
	t.Chdir(t.TempDir())

	for _, tc := range []struct {
		args []string
		want string // the token cobra must name back
	}{
		{[]string{"db", "bogus"}, "bogus"},
		{[]string{"db", "migrate", "bogus"}, "bogus"},
		{[]string{"env", "bogus"}, "bogus"},
		{[]string{"debug", "bogus"}, "bogus"},
		{[]string{"project", "bogus"}, "bogus"},
		// The RETIRED verb, deliberately spelled out: `add` is gone, and a
		// project scaffolded by an older forge (or an agent working from a
		// stale doc) must be told which token is wrong — not handed a bare
		// usage dump. This case only tests anything while it names the dead
		// spelling, so it must never be swept along by a rename.
		{[]string{"project", "add", "entity"}, "add"},
	} {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			out, err := execRoot(t, tc.args...)
			if err == nil {
				t.Fatalf("`forge %s` succeeded — an unknown subcommand must exit non-zero.\noutput:\n%s",
					strings.Join(tc.args, " "), out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("`forge %s` failed with %q, which never names the offending token %q",
					strings.Join(tc.args, " "), err, tc.want)
			}
		})
	}
}

// TestBareGroupStillPrintsHelp: hardening must not have turned the bare form
// into an error. The user asked for the menu and must get it, exit 0.
func TestBareGroupStillPrintsHelp(t *testing.T) {
	for _, group := range []string{"db", "env", "project", "skill", "tools"} {
		out, err := execRoot(t, group)
		if err != nil {
			t.Errorf("bare `forge %s` failed (%v) — it must print help and succeed", group, err)
			continue
		}
		if !strings.Contains(out, "Available Commands:") {
			t.Errorf("bare `forge %s` printed no command list:\n%s", group, out)
		}
	}
}

// TestStrictGroupSuggestsANearMiss: a typo'd subcommand gets cobra's
// "Did you mean this?" line, which the hand-rolled group validator has to
// reproduce because cobra only emits it for the root command.
func TestStrictGroupSuggestsANearMiss(t *testing.T) {
	_, err := execRoot(t, "db", "migrat")
	if err == nil {
		t.Fatal("`forge db migrat` succeeded")
	}
	if !strings.Contains(err.Error(), "Did you mean this?") || !strings.Contains(err.Error(), "migrate") {
		t.Errorf("`forge db migrat` failed without suggesting `migrate`:\n%v", err)
	}
}
