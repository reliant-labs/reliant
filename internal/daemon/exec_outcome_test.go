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
		out := ClassifyExecOutcome(nil, nil, "", "warning: noisy but fine", nil, noSnap, nil)
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
		out := ClassifyExecOutcome(err, nil, "", "boom", nil, noSnap, nil)
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

		out := ClassifyExecOutcome(err, nil, "", "", nil, noSnap, nil)
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
		out := ClassifyExecOutcome(err, nil, "", "earlier output", nil, noSnap, nil)
		if !strings.Contains(out.Stderr, "earlier output") {
			t.Errorf("Stderr = %q, want the captured stderr preserved", out.Stderr)
		}
		if !strings.Contains(out.Stderr, "definitely-not-a-real-binary-xyz") {
			t.Errorf("Stderr = %q, want the start failure appended", out.Stderr)
		}
	})

	t.Run("start failure with empty stderr does not lead with a blank line", func(t *testing.T) {
		err := realStartError(t, exec.Command("definitely-not-a-real-binary-xyz"))
		out := ClassifyExecOutcome(err, nil, "", "", nil, noSnap, nil)
		if strings.HasPrefix(out.Stderr, "\n") {
			t.Errorf("Stderr = %q, want no leading blank line", out.Stderr)
		}
	})

	t.Run("timeout without an exit status reports the timeout code", func(t *testing.T) {
		err := realStartError(t, exec.Command("definitely-not-a-real-binary-xyz"))
		out := ClassifyExecOutcome(err, context.DeadlineExceeded, "", "", nil, noSnap, nil)
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
		out := ClassifyExecOutcome(err, nil, "", "child said this", stubOOM{msg: "killed: out of memory"}, noSnap, nil)
		if !out.OOMKilled {
			t.Error("OOMKilled = false, want true")
		}
		if !strings.Contains(out.Stderr, "child said this") || !strings.Contains(out.Stderr, "killed: out of memory") {
			t.Errorf("Stderr = %q, want both the child stderr and the OOM explanation", out.Stderr)
		}
	})

	t.Run("timeout keeps precedence over oom attribution", func(t *testing.T) {
		err := realStartError(t, exec.Command("false"))
		out := ClassifyExecOutcome(err, context.DeadlineExceeded, "", "", stubOOM{msg: "killed: out of memory"}, noSnap, nil)
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

// A build that died on a full disk must say which volume is full: the bare
// "no space left on device" names neither the volume nor what fills it, and an
// agent left with it retries the build against the same full disk.
func TestClassifyExecOutcome_NoSpace(t *testing.T) {
	var noSnap cgroupmem.OOMSnapshot
	disk := &stubDisk{note: "[disk full] The machine's build caches & docker volume is out of space"}

	t.Run("an ENOSPC failure gets the volume explanation", func(t *testing.T) {
		err := realStartError(t, exec.Command("false"))
		// go prints its cache write failure to stdout in `go test` output.
		out := ClassifyExecOutcome(err, nil, "write /var/cache/workspace/go-build/39/x-d: no space left on device", "FAIL", nil, noSnap, disk)
		if !strings.Contains(out.Stderr, "FAIL") || !strings.Contains(out.Stderr, disk.note) {
			t.Fatalf("Stderr = %q, want the child's stderr plus the disk explanation", out.Stderr)
		}
		if !strings.Contains(disk.seen, "no space left on device") {
			t.Fatalf("the explainer must see stdout too; saw %q", disk.seen)
		}
	})

	t.Run("a successful command is never annotated", func(t *testing.T) {
		disk.seen = ""
		out := ClassifyExecOutcome(nil, nil, "no space left on device (handled)", "", nil, noSnap, disk)
		if out.Stderr != "" || disk.seen != "" {
			t.Fatalf("Stderr = %q, explainer saw %q; a success is not a disk failure", out.Stderr, disk.seen)
		}
	})

	t.Run("nothing to blame leaves stderr as the child wrote it", func(t *testing.T) {
		err := realStartError(t, exec.Command("false"))
		out := ClassifyExecOutcome(err, nil, "", "boom", nil, noSnap, &stubDisk{})
		if out.Stderr != "boom" {
			t.Fatalf("Stderr = %q, want it unmodified", out.Stderr)
		}
	})
}

// stubDisk stands in for *diskwatch.Watcher; the real attribution is covered
// in internal/diskwatch.
type stubDisk struct {
	note string
	seen string
}

func (s *stubDisk) ExplainNoSpace(text string) string {
	s.seen = text
	if !strings.Contains(text, "no space left on device") {
		return ""
	}
	return s.note
}
