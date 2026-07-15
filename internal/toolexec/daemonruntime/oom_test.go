// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/cgroupmem"
	"github.com/reliant-labs/reliant/internal/daemon"
)

// fakeCgroup builds a cgroup v2 fixture dir and points the package-level
// memReader at it for the duration of the test.
func fakeCgroup(t *testing.T, oomKills string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"memory.current": "3865470566\n",
		"memory.max":     "4294967296\n",
		"memory.events":  "low 0\nhigh 0\nmax 0\noom 0\noom_kill " + oomKills + "\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	prev := memReader
	memReader = cgroupmem.NewReader(dir)
	t.Cleanup(func() { memReader = prev })
	return dir
}

func bumpFakeOOMKill(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "memory.events"),
		[]byte("low 0\nhigh 0\nmax 0\noom 1\noom_kill 1\n"), 0o644); err != nil {
		t.Fatalf("bumping oom_kill: %v", err)
	}
}

// TestHandleExecRunOOMClassification exercises the exec.run wiring: a
// SIGKILL-shaped exit (137) coinciding with an oom_kill increment in the
// (fake) cgroup must yield OOMKilled=true and the actionable message on
// Stderr, so both the LLM tool result and user RPC consumers see it.
func TestHandleExecRunOOMClassification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-based test")
	}
	dir := fakeCgroup(t, "0")

	// Bump the counter while the command is sleeping, between the pre-exec
	// snapshot and the post-kill check.
	go func() {
		time.Sleep(150 * time.Millisecond)
		bumpFakeOOMKill(t, dir)
	}()

	payload, _ := json.Marshal(daemon.RunCommandRequest{
		Command:   "sleep 0.6; exit 137",
		TimeoutMs: 10_000,
	})
	out, err := handleExecRun(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleExecRun: %v", err)
	}
	var resp daemon.CommandResult
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if resp.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137", resp.ExitCode)
	}
	if !resp.OOMKilled {
		t.Fatal("expected OOMKilled = true")
	}
	const wantMsg = "command was killed: workspace out of memory (used ~3.6 GiB of 4.0 GiB). Upgrade the machine size or reduce the command's memory usage."
	if !strings.Contains(resp.Stderr, wantMsg) {
		t.Errorf("Stderr = %q, want it to contain %q", resp.Stderr, wantMsg)
	}
	if !strings.Contains(resp.Combined, wantMsg) {
		t.Error("Combined output must also carry the OOM message")
	}
}

// TestHandleExecRunOrdinaryFailureNotOOM: a plain non-zero exit must never be
// classified as OOM, even when an unrelated oom_kill lands concurrently.
func TestHandleExecRunOrdinaryFailureNotOOM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-based test")
	}
	dir := fakeCgroup(t, "0")
	go func() {
		time.Sleep(100 * time.Millisecond)
		bumpFakeOOMKill(t, dir)
	}()

	payload, _ := json.Marshal(daemon.RunCommandRequest{
		Command:   "sleep 0.4; exit 1",
		TimeoutMs: 10_000,
	})
	out, err := handleExecRun(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleExecRun: %v", err)
	}
	var resp daemon.CommandResult
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if resp.OOMKilled {
		t.Error("exit 1 must not be classified as OOM")
	}
	if strings.Contains(resp.Stderr, "out of memory") {
		t.Errorf("unexpected OOM text in stderr: %q", resp.Stderr)
	}
}

// TestHandleExecRunNoCgroup: on hosts without cgroup files (mac/local
// daemons) a 137 exit stays a plain failure — no OOM text, no flag.
func TestHandleExecRunNoCgroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-based test")
	}
	prev := memReader
	memReader = cgroupmem.NewReader(filepath.Join(t.TempDir(), "missing"))
	t.Cleanup(func() { memReader = prev })

	payload, _ := json.Marshal(daemon.RunCommandRequest{
		Command:   "exit 137",
		TimeoutMs: 10_000,
	})
	out, err := handleExecRun(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleExecRun: %v", err)
	}
	var resp daemon.CommandResult
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if resp.OOMKilled || strings.Contains(resp.Stderr, "out of memory") {
		t.Errorf("no cgroup — must not classify as OOM: %+v", resp)
	}
	if resp.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137", resp.ExitCode)
	}
}

// TestWrapChildOOMKill covers the error decorator used by the fs/git command
// handlers' failure paths.
func TestWrapChildOOMKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-based test")
	}
	dir := fakeCgroup(t, "0")

	// Produce a real *exec.ExitError with exit code 137.
	cmd := exec.Command("bash", "-c", "exit 137")
	runErr := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("expected ExitError, got %v", runErr)
	}

	t.Run("oom recorded", func(t *testing.T) {
		snap := memReader.SnapshotOOMKills()
		bumpFakeOOMKill(t, dir)
		wrapped := wrapChildOOMKill(runErr, snap)
		if !strings.Contains(wrapped.Error(), "workspace out of memory") {
			t.Errorf("wrapped error = %q, want OOM message", wrapped.Error())
		}
		if !errors.As(wrapped, new(*exec.ExitError)) {
			t.Error("original ExitError must remain unwrappable")
		}
	})

	t.Run("nil error passthrough", func(t *testing.T) {
		if wrapChildOOMKill(nil, memReader.SnapshotOOMKills()) != nil {
			t.Error("nil must stay nil")
		}
	})

	t.Run("non-exit error passthrough", func(t *testing.T) {
		plain := errors.New("boom")
		if got := wrapChildOOMKill(plain, memReader.SnapshotOOMKills()); got != plain {
			t.Errorf("non-ExitError must pass through unchanged, got %v", got)
		}
	})
}
