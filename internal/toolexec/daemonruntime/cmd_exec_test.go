// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
)

func runExec(t *testing.T, req daemon.RunCommandRequest) daemon.CommandResult {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	raw, err := handleExecRun(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleExecRun returned a transport error, want a populated result: %v", err)
	}
	var res daemon.CommandResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return res
}

// TestHandleExecRun_MissingWorkingDirSurfacesError pins the same contract on
// the daemon's exec.run handler that LocalClient.RunCommand carries: a command
// that never starts must come back with the reason on stderr, naming the
// directory that is actually missing. The two paths share
// daemon.ClassifyExecOutcome / daemon.ValidateWorkingDir precisely so they
// cannot drift — this test is the tripwire for that.
func TestHandleExecRun_MissingWorkingDirSurfacesError(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "does", "not", "exist")

	res := runExec(t, daemon.RunCommandRequest{Command: "echo hello", WorkingDir: missingDir})

	if res.ExitCode == 0 {
		t.Fatalf("ExitCode = 0, want non-zero for a command that never started")
	}
	if strings.TrimSpace(res.Stderr) == "" {
		t.Fatalf("Stderr is empty — the start failure was discarded; result was %+v", res)
	}
	if !strings.Contains(res.Stderr, missingDir) {
		t.Errorf("Stderr = %q, want it to name the missing working directory %q", res.Stderr, missingDir)
	}
	if strings.Contains(res.Stderr, "bash") || strings.Contains(res.Stderr, "powershell") {
		t.Errorf("Stderr = %q, must not blame the shell binary for a missing directory", res.Stderr)
	}
}

func TestHandleExecRun_HealthyCommandUnaffected(t *testing.T) {
	res := runExec(t, daemon.RunCommandRequest{Command: "echo hello", WorkingDir: t.TempDir()})

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

// TestHandleExecRun_StartFailureHasNoLeadingBlankLine covers the formatting
// regression the old fmt.Sprintf("%s\n%s", stderr, err) left behind: with no
// captured stderr it produced a stderr that began with a newline.
func TestHandleExecRun_StartFailureHasNoLeadingBlankLine(t *testing.T) {
	res := runExec(t, daemon.RunCommandRequest{
		Command:    "echo hello",
		WorkingDir: filepath.Join(t.TempDir(), "nope"),
	})
	if strings.HasPrefix(res.Stderr, "\n") {
		t.Errorf("Stderr = %q, want no leading blank line", res.Stderr)
	}
}
