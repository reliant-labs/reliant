package cli

import (
	"bytes"
	"strings"
	"testing"
)

// execRoot runs the assembled root command with args, capturing combined
// stdout+stderr cobra output and the returned error.
func execRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := newTestRootCmd(t)
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

// TestRootCmd_RuntimeErrorDoesNotDumpUsage pins that a command failing at
// RunE (a pipeline-step error, not a usage mistake) does NOT dump the
// cobra usage block. Before this, every `forge generate` / `forge scaffold`
// failure buried the real error under ~40 lines of flag help; main()
// prints the error itself, so the error must be the last thing the user
// sees.
func TestRootCmd_RuntimeErrorDoesNotDumpUsage(t *testing.T) {
	// `forge scaffold entity 9bad` fails validation inside RunE — a runtime
	// error, not a cobra arg/flag error — without touching the project.
	out, err := execRoot(t, "scaffold", "entity", "9bad")
	if err == nil {
		t.Fatal("expected an error from an invalid entity name")
	}
	if strings.Contains(out, "Usage:") {
		t.Errorf("RunE failure dumped the usage block, burying the real error:\n%s", out)
	}
	// Cobra must not print the error either (SilenceErrors): main() owns
	// the single, final "Error: ..." line. A cobra print here would
	// double-report.
	if strings.Contains(out, err.Error()) {
		t.Errorf("cobra printed the error itself — main() already does, so it appears twice:\n%s", out)
	}
}

// TestRootCmd_FlagErrorStillShowsUsage pins the counterpart: a genuine
// usage mistake (unknown flag) still gets the usage block — suppression
// is scoped to runtime errors only (SilenceUsage is set after flag
// parsing succeeds).
func TestRootCmd_FlagErrorStillShowsUsage(t *testing.T) {
	out, err := execRoot(t, "scaffold", "entity", "--definitely-not-a-flag")
	if err == nil {
		t.Fatal("expected an error from an unknown flag")
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("flag misuse should still print usage:\n%s", out)
	}
}

// TestRootCmd_ArgErrorStillShowsUsage: wrong arg count is also a usage
// mistake (cobra validates args before PersistentPreRun runs), so usage
// help must survive for it.
func TestRootCmd_ArgErrorStillShowsUsage(t *testing.T) {
	out, err := execRoot(t, "scaffold", "entity")
	if err == nil {
		t.Fatal("expected an error from missing args")
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("missing-arg misuse should still print usage:\n%s", out)
	}
}
