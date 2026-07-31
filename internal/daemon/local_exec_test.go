// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunCommand_MissingWorkingDirSurfacesError pins the rule that makes a
// broken exec diagnosable from inside the agent loop: when the command never
// starts, the reason must ride back on Stderr.
//
// A WorkingDir that does not exist fails in cmd.Start() with an *os.PathError
// ("chdir <dir>: no such file or directory"), not an *exec.ExitError, and
// cmd.Wait() is never reached — so the captured stderr buffer is empty. If the
// start error is dropped, EVERY command in that directory returns
// {"stdout":"","stderr":"","exit_code":1}, including `echo hello`, and the
// caller has nothing to diagnose with.
func TestRunCommand_MissingWorkingDirSurfacesError(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "does", "not", "exist")

	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    "echo hello",
		WorkingDir: missingDir,
	})
	if err != nil {
		t.Fatalf("RunCommand returned a transport error, want a populated result: %v", err)
	}

	if res.ExitCode == 0 {
		t.Fatalf("ExitCode = 0, want non-zero for a command that never started")
	}
	if strings.TrimSpace(res.Stderr) == "" {
		t.Fatalf("Stderr is empty — the start failure was discarded; result was %+v", res)
	}
	// Assert the ENOENT surfaced, not an exact message: the offending path must
	// appear so the caller can see WHICH directory is missing.
	if !strings.Contains(res.Stderr, missingDir) {
		t.Errorf("Stderr = %q, want it to name the missing working directory %q", res.Stderr, missingDir)
	}
	if !strings.Contains(res.Combined, missingDir) {
		t.Errorf("Combined = %q, want it to carry the same explanation as Stderr", res.Combined)
	}
}

// TestRunCommand_HealthyCommandUnaffected is the control for the test above:
// the same client in a real directory still reports stdout and exit 0. It
// exists so a fix to the start-error path cannot be mistaken for "everything
// now reports an error".
func TestRunCommand_HealthyCommandUnaffected(t *testing.T) {
	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    "echo hello",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 (stderr=%q)", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "hello" {
		t.Errorf("Stdout = %q, want %q", res.Stdout, "hello")
	}
	if strings.TrimSpace(res.Stderr) != "" {
		t.Errorf("Stderr = %q, want empty for a successful command", res.Stderr)
	}
}

// TestRunCommand_NonZeroExitKeepsShellStderr pins that the shell's own stderr
// still wins on the *exec.ExitError path — the start-error annotation must not
// displace or duplicate real command output.
func TestRunCommand_NonZeroExitKeepsShellStderr(t *testing.T) {
	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    "definitely-not-a-real-binary-xyz",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if res.ExitCode != 127 {
		t.Errorf("ExitCode = %d, want 127 (shell 'command not found')", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "definitely-not-a-real-binary-xyz") {
		t.Errorf("Stderr = %q, want the shell's own not-found message", res.Stderr)
	}
}
