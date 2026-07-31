// Copyright (c) 2025 Reliant Labs
package shell

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestStartProcess_MissingWorkingDirNamesTheDirectory pins the third exec path
// to the same rule as LocalClient.RunCommand and the daemon's exec.run handler:
// a command that cannot start must fail with the reason, naming the directory
// that is actually missing.
//
// Go performs the chdir inside the forked child, so without a pre-flight check
// this comes back as "fork/exec /bin/bash: no such file or directory" — which
// sends the caller looking for a missing shell instead of a missing directory.
func TestStartProcess_MissingWorkingDirNamesTheDirectory(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "does", "not", "exist")

	proc, err := newTestBGManager().StartProcess(context.Background(), StartProcessOptions{
		Command:    "echo hello",
		WorkingDir: missingDir,
	})
	if err == nil {
		t.Fatalf("StartProcess succeeded (process %v), want an error for a missing working directory", proc)
	}
	if !strings.Contains(err.Error(), missingDir) {
		t.Errorf("error = %q, want it to name the missing working directory %q", err, missingDir)
	}
	if strings.Contains(err.Error(), "bash") || strings.Contains(err.Error(), "powershell") {
		t.Errorf("error = %q, must not blame the shell binary for a missing directory", err)
	}
}

// TestStartProcess_HealthyWorkingDirUnaffected is the control: a real directory
// still starts.
func TestStartProcess_HealthyWorkingDirUnaffected(t *testing.T) {
	m := newTestBGManager()
	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    "echo hello",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	t.Cleanup(func() { _ = m.KillProcess(proc.ID) })
	if proc.ID == "" {
		t.Error("StartProcess returned a process with no ID")
	}
}

// newTestBGManager builds an unregistered manager so these tests do not share
// the process table with the package singleton.
func newTestBGManager() *BackgroundManager {
	return &BackgroundManager{processes: make(map[string]*BackgroundProcess)}
}
