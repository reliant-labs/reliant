// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/cgroupmem"
)

// realStartError runs a command that cannot start and returns the genuine
// error the os/exec package produces, so these tests classify real error
// values rather than hand-built stand-ins.
func realStartError(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected %v to fail", cmd.Args)
	}
	return err
}

func TestClassifyExecOutcome(t *testing.T) {
	var noSnap cgroupmem.OOMSnapshot

	t.Run("success leaves stderr untouched", func(t *testing.T) {
		out := ClassifyExecOutcome(nil, nil, "warning: noisy but fine", nil, noSnap)
		if out.ExitCode != 0 || out.TimedOut || out.OOMKilled {
			t.Errorf("outcome = %+v, want a clean zero outcome", out)
		}
		if out.Stderr != "warning: noisy but fine" {
			t.Errorf("Stderr = %q, want the captured stderr verbatim", out.Stderr)
		}
	})

	t.Run("exit status is reported without annotation", func(t *testing.T) {
		// A real *exec.ExitError: the child ran, so its own stderr explains it
		// and we must not append anything.
		err := realStartError(t, exec.Command("false"))
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("expected an *exec.ExitError, got %T", err)
		}
		out := ClassifyExecOutcome(err, nil, "boom", nil, noSnap)
		if out.ExitCode != 1 {
			t.Errorf("ExitCode = %d, want 1", out.ExitCode)
		}
		if out.Stderr != "boom" {
			t.Errorf("Stderr = %q, want the child's own stderr unmodified", out.Stderr)
		}
	})

	// The regression this whole file exists for: a failure that is NOT an
	// *exec.ExitError means the child never ran, so its stderr pipe is empty.
	// Discarding the error returns {stderr:"", exit_code:1} for every command
	// in that environment and leaves the caller nothing to diagnose with.
	t.Run("binary-not-found surfaces the reason", func(t *testing.T) {
		err := realStartError(t, exec.Command("definitely-not-a-real-binary-xyz"))
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("precondition: binary-not-found must not be an *exec.ExitError, got %v", err)
		}

		out := ClassifyExecOutcome(err, nil, "", nil, noSnap)
		if out.ExitCode != 1 {
			t.Errorf("ExitCode = %d, want 1", out.ExitCode)
		}
		if out.Stderr == "" {
			t.Fatal("Stderr is empty — the start failure was discarded")
		}
		if !strings.Contains(out.Stderr, "definitely-not-a-real-binary-xyz") {
			t.Errorf("Stderr = %q, want it to name the binary that could not be found", out.Stderr)
		}
	})

	t.Run("start failure is appended to captured stderr, not replacing it", func(t *testing.T) {
		err := realStartError(t, exec.Command("definitely-not-a-real-binary-xyz"))
		out := ClassifyExecOutcome(err, nil, "earlier output", nil, noSnap)
		if !strings.Contains(out.Stderr, "earlier output") {
			t.Errorf("Stderr = %q, want the captured stderr preserved", out.Stderr)
		}
		if !strings.Contains(out.Stderr, "definitely-not-a-real-binary-xyz") {
			t.Errorf("Stderr = %q, want the start failure appended", out.Stderr)
		}
	})

	t.Run("start failure with empty stderr does not lead with a blank line", func(t *testing.T) {
		err := realStartError(t, exec.Command("definitely-not-a-real-binary-xyz"))
		out := ClassifyExecOutcome(err, nil, "", nil, noSnap)
		if strings.HasPrefix(out.Stderr, "\n") {
			t.Errorf("Stderr = %q, want no leading blank line", out.Stderr)
		}
	})

	t.Run("timeout without an exit status reports the timeout code", func(t *testing.T) {
		err := realStartError(t, exec.Command("definitely-not-a-real-binary-xyz"))
		out := ClassifyExecOutcome(err, context.DeadlineExceeded, "", nil, noSnap)
		if !out.TimedOut {
			t.Error("TimedOut = false, want true")
		}
		if out.ExitCode != TimeoutExitCode {
			t.Errorf("ExitCode = %d, want %d", out.ExitCode, TimeoutExitCode)
		}
		if out.Stderr == "" {
			t.Error("Stderr is empty — a timed-out start still owes the caller a reason")
		}
	})

	t.Run("oom explanation is appended when the checker attributes the kill", func(t *testing.T) {
		err := realStartError(t, exec.Command("false"))
		out := ClassifyExecOutcome(err, nil, "child said this", stubOOM{msg: "killed: out of memory"}, noSnap)
		if !out.OOMKilled {
			t.Error("OOMKilled = false, want true")
		}
		if !strings.Contains(out.Stderr, "child said this") || !strings.Contains(out.Stderr, "killed: out of memory") {
			t.Errorf("Stderr = %q, want both the child stderr and the OOM explanation", out.Stderr)
		}
	})

	t.Run("timeout keeps precedence over oom attribution", func(t *testing.T) {
		err := realStartError(t, exec.Command("false"))
		out := ClassifyExecOutcome(err, context.DeadlineExceeded, "", stubOOM{msg: "killed: out of memory"}, noSnap)
		if out.OOMKilled {
			t.Error("OOMKilled = true, want false — a timeout SIGKILL is not an OOM")
		}
	})
}

// stubOOM always attributes a failure to the OOM killer, standing in for a
// cgroup whose oom_kill counter advanced. Real cgroup accounting is covered in
// internal/cgroupmem.
type stubOOM struct{ msg string }

func (s stubOOM) CheckOOMKill(int, cgroupmem.OOMSnapshot) (bool, string) { return true, s.msg }
