// Copyright (c) 2025 Reliant Labs
//go:build !windows

package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// GetProcessStatus answers "has this process ended?", which callers poll (the
// daemon's process-output stream asks after every read). It must read the
// lifecycle fields only: going through GetProcess ran a whole-host `ps` +
// `lsof` port scan for a running process on every poll, and nobody read the
// ports.
//
// The record is synthetic, with this test's own pid, so nothing is started or
// killed; `ps` and `lsof` are spies that record a call and print nothing, so no
// real scan runs even when the bug is present.
func TestGetProcessStatus_NeverScansPorts(t *testing.T) {
	spies := installPortScanSpies(t)
	m := newStatusTestManager(t)

	status, complete, exitCode, err := m.GetProcessStatus("running")
	if err != nil {
		t.Fatalf("GetProcessStatus(running): %v", err)
	}
	if status != "running" || complete || exitCode != nil {
		t.Errorf("running = (%q, %v, %v), want (\"running\", false, nil)", status, complete, exitCode)
	}

	status, complete, exitCode, err = m.GetProcessStatus("done")
	if err != nil {
		t.Fatalf("GetProcessStatus(done): %v", err)
	}
	if status != "completed" || !complete || exitCode == nil || *exitCode != 3 {
		t.Errorf("done = (%q, %v, %v), want (\"completed\", true, 3)", status, complete, exitCode)
	}

	status, complete, exitCode, err = m.GetProcessStatus("missing")
	if err == nil || err.Error() != "process missing not found" {
		t.Errorf("missing err = %v, want \"process missing not found\"", err)
	}
	if status != "" || complete || exitCode != nil {
		t.Errorf("missing = (%q, %v, %v), want zero values", status, complete, exitCode)
	}

	for _, name := range []string{"ps", "lsof"} {
		if got := spies.read(t, name); got != 0 {
			t.Errorf("%s invocations = %d, want 0: GetProcessStatus must not scan ports", name, got)
		}
	}
}

// The control for the test above: the spies are on PATH and GetProcess, which
// does report ports, still scans for the same synthetic record. Without it a
// spy that never ran would let a regression pass. On Linux the scan reads
// procfs and forks neither tool (wantPortScanForks is 0), so the spies, and
// the test above, bite only where the scan is `ps` + `lsof`.
func TestGetProcess_StillScansPortsOfARunningProcess(t *testing.T) {
	testkit.Slow(t, "execs the ps and lsof spy scripts")
	spies := installPortScanSpies(t)
	m := newStatusTestManager(t)

	if _, err := m.GetProcess("running"); err != nil {
		t.Fatalf("GetProcess(running): %v", err)
	}
	for _, name := range []string{"ps", "lsof"} {
		if got := spies.read(t, name); got != wantPortScanForks {
			t.Errorf("%s invocations = %d, want %d from GetProcess's port scan", name, got, wantPortScanForks)
		}
	}
}

// newStatusTestManager holds a running record whose pid is this test process
// (so refreshPorts would scan it) and a completed one.
func newStatusTestManager(t *testing.T) *BackgroundManager {
	t.Helper()
	exitCode := 3
	m := newTestBGManager()
	m.processes["running"] = &BackgroundProcess{
		ID:     "running",
		Status: "running",
		cmd:    &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}},
	}
	m.processes["done"] = &BackgroundProcess{
		ID:       "done",
		Status:   "completed",
		ExitCode: &exitCode,
	}
	return m
}

// installPortScanSpies puts `ps` and `lsof` scripts at the front of PATH that
// record each call and print nothing. Unlike installPortScanCounters they never
// exec the real tools, so a test using them neither depends on nor runs a host
// scan.
func installPortScanSpies(t *testing.T) *portScanCounters {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"ps", "lsof"} {
		script := "#!/bin/sh\nprintf 'call\\n' >> '" + filepath.Join(dir, name+".count") + "'\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatalf("write %s spy: %v", name, err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &portScanCounters{dir: dir}
}
