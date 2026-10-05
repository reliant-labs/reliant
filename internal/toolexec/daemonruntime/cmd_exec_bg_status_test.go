// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
)

// exec.bg_status is how the server learns a backgrounded tool call's process
// ended. It must report a finished process's real outcome, and report a
// process this daemon has no record of as Unknown — the in-memory registry
// forgets everything on restart, and the server closes those calls rather than
// leaving them "running" forever.
func TestHandleExecBGStatus_ReportsOutcomeAndForgottenProcesses(t *testing.T) {
	mgr := shell.GetBackgroundManager()
	process, err := mgr.StartProcess(context.Background(), shell.StartProcessOptions{
		Command:    "exit 3",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}

	// Wait for the process to exit; completion is recorded asynchronously.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if status, done, _, _ := mgr.GetProcessStatus(process.ID); done {
			_ = status
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background process never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}

	payload, _ := json.Marshal(daemon.ProcessStatusRequest{ProcessIDs: []string{process.ID, "never-started"}})
	raw, err := handleExecBGStatus(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleExecBGStatus: %v", err)
	}
	var resp daemon.ProcessStatusResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(resp.Processes) != 1 || resp.Processes[0].ID != process.ID {
		t.Fatalf("Processes = %+v, want exactly the tracked process", resp.Processes)
	}
	got := resp.Processes[0]
	if got.Status != "failed" {
		t.Errorf("Status = %q, want failed for a non-zero exit", got.Status)
	}
	if got.ExitCode == nil || *got.ExitCode != 3 {
		t.Errorf("ExitCode = %v, want 3", got.ExitCode)
	}
	if got.EndTime == nil {
		t.Error("EndTime is nil for a process that ended")
	}
	if len(resp.Unknown) != 1 || resp.Unknown[0] != "never-started" {
		t.Errorf("Unknown = %v, want [never-started]", resp.Unknown)
	}
}
