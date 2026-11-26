package cli

import (
	"strings"
	"testing"
)

// The group's name is the one `forge test` was removed under (ci_test_run.go),
// so its help says first, before anything else, that it runs no suite.
func TestTestGroupSaysItRunsNoSuiteOfItsOwn(t *testing.T) {
	// Not parallel: newTestRootCmd resets package state (the recorded route).
	cmd := newTestCmd()
	first, _, _ := strings.Cut(cmd.Long, "\n")
	for name, text := range map[string]string{"Short": cmd.Short, "the first line of Long": first} {
		if !strings.Contains(text, "project's own test lanes") || !strings.Contains(text, "never runs a test suite of its own") {
			t.Errorf("%s = %q: say it records the project's own lanes and never runs a suite of its own", name, text)
		}
	}
	for _, path := range [][]string{{"test", "begin"}, {"test", "capture"}} {
		found, _, err := newTestRootCmd(t).Find(path)
		if err != nil || found.Name() != path[1] {
			t.Errorf("forge %s is not registered: %v", strings.Join(path, " "), err)
		}
	}
}
