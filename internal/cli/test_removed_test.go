package cli

import (
	"bytes"
	"strings"
	"testing"
)

// runRoot dispatches args through the real assembled root command with output
// captured, and returns the error. Used to assert on the actual CLI surface
// rather than on a hand-built command.
func runRoot(t *testing.T, args ...string) error {
	t.Helper()
	root := newTestRootCmd(t)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	return root.Execute()
}

// TestRemovedTestCommandNamesItsReplacement pins the whole point of the
// `forge test` stub: it must FAIL, and it must say what to run instead.
//
// The failure half matters as much as the message. `forge test` used to run
// the suite, so anything that still calls it — a stale script, an agent
// working from an old skill — must get a non-zero exit rather than a silent
// no-op that reads as "the tests passed".
func TestRemovedTestCommandNamesItsReplacement(t *testing.T) {
	// Outside a project, so a regression that restored real test-running
	// behaviour has nothing to run and still trips the assertions below.
	t.Chdir(t.TempDir())

	for _, tc := range []struct {
		name string
		args []string
		want string // the replacement the message must name
	}{
		{"bare", []string{"test"}, "task test"},
		{"unit", []string{"test", "unit"}, "task test"},
		{"integration", []string{"test", "integration"}, "task test:integration"},
		{"e2e", []string{"test", "e2e"}, "task test:e2e"},
		{"codemod", []string{"test", "migrate-tdd"}, "forge project migrate tdd"},
		// A package pattern is the shape `forge test unit ./internal/foo/...`
		// taught for scoped runs; the scoped Task form is the useful answer.
		{"package pattern", []string{"test", "./internal/foo/..."}, "task test -- ./internal/foo/..."},
		// Flags must not be parsed before RunE is reached: `--coverage` is not
		// a flag this command declares, and dying on "unknown flag" would
		// bury the replacement the user actually needs.
		{"unknown flag", []string{"test", "--coverage"}, "task test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runRoot(t, tc.args...)
			if err == nil {
				t.Fatalf("`forge %s` returned nil — the removed command must fail, "+
					"or a stale caller reads its exit 0 as a passing suite",
					strings.Join(tc.args, " "))
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("`forge %s` must point at %q; got:\n%s",
					strings.Join(tc.args, " "), tc.want, err.Error())
			}
		})
	}
}

// TestRemovedTestCommandIsHiddenFromHelp pins the surface reduction that
// motivated the removal: `forge test` must not appear in `forge --help`. A
// signpost that still advertises itself is just the old command with extra
// steps.
func TestRemovedTestCommandIsHiddenFromHelp(t *testing.T) {
	root := newTestRootCmd(t)
	for _, c := range root.Commands() {
		if c.Name() != "test" {
			continue
		}
		if !c.Hidden {
			t.Error("the `test` stub must stay Hidden — it exists to redirect stale " +
				"callers, not to keep `forge test` on the command list")
		}
		if c.IsAvailableCommand() {
			t.Error("the `test` stub is still listed as an available command, so it " +
				"renders in `forge --help`")
		}
		return
	}
	// Absent entirely is the intended END state (once the spelling stops
	// showing up in the wild), so it is not a failure — but say so, because
	// the assertions above then guard nothing.
	t.Log("no `test` command registered at all — the stub was fully removed; " +
		"this test can go with it")
}
