// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
)

// exec.bg_status is how the server learns a backgrounded tool call's process
// ended. It must report a finished process's real outcome, and report a
// process this daemon has no record of as Unknown — the in-memory registry
// forgets everything on restart, and the server closes those calls rather than
// leaving them "running" forever.
func TestHandleExecBGStatus_ReportsOutcomeAndForgottenProcesses(t *testing.T) {
	t.Parallel()
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

// startSleeper starts a first-party agent background process that runs until
// the test ends.
func startSleeper(t *testing.T) *shell.BackgroundProcess {
	t.Helper()
	mgr := shell.GetBackgroundManager()
	process, err := mgr.StartProcess(context.Background(), shell.StartProcessOptions{
		Command:    "exec sleep 600",
		WorkingDir: t.TempDir(),
		ChatID:     "chat-1",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	t.Cleanup(func() { _ = mgr.KillProcess(process.ID) })
	return process
}

// The server's word on each process's chat rides on exec.bg_status for the
// daemon's idle reaper. Only the server's own (first-party) poll may give it:
// a connector that could say "finished" would talk the reaper into stopping
// the user's dev server.
func TestHandleExecBGStatus_RecordsChatStateFromTheServerOnly(t *testing.T) {
	t.Parallel()
	mgr := shell.GetBackgroundManager()
	process := startSleeper(t)
	ask := func(ctx context.Context, running bool) {
		t.Helper()
		payload, _ := json.Marshal(daemon.ProcessStatusRequest{
			ProcessIDs: []string{process.ID},
			Chats:      []daemon.ProcessChatState{{ProcessID: process.ID, Running: running}},
		})
		if _, err := handleExecBGStatus(ctx, payload); err != nil {
			t.Fatalf("handleExecBGStatus: %v", err)
		}
	}

	ask(daemonpolicy.NewContext(context.Background(), &daemonpolicy.Policy{GrantID: "grant-1"}), false)
	if got := mgr.ReapOutlook(process).ChatRunning; got != nil {
		t.Fatalf("a connector's word on the chat was recorded: running=%v", *got)
	}
	for _, running := range []bool{true, false} {
		ask(context.Background(), running)
		if got := mgr.ReapOutlook(process).ChatRunning; got == nil || *got != running {
			t.Fatalf("after the server said running=%v, the daemon holds %v", running, got)
		}
	}
}

// exec.bg_touch is an agent using a process: it resets the idle time, and a
// connector's reaches only its own processes.
func TestHandleExecBGTouch_ResetsIdleTimeWithinTheCallersGrant(t *testing.T) {
	t.Parallel()
	mgr := shell.GetBackgroundManager()
	process := startSleeper(t)
	before := mgr.ReapOutlook(process).LastActive
	touch := func(ctx context.Context) time.Time {
		t.Helper()
		time.Sleep(5 * time.Millisecond)
		payload, _ := json.Marshal(daemon.TouchProcessesRequest{ProcessIDs: []string{process.ID, "never-started"}})
		if _, err := handleExecBGTouch(ctx, payload); err != nil {
			t.Fatalf("handleExecBGTouch: %v", err)
		}
		return mgr.ReapOutlook(process).LastActive
	}

	if got := touch(daemonpolicy.NewContext(context.Background(), &daemonpolicy.Policy{GrantID: "grant-1"})); !got.Equal(before) {
		t.Fatalf("a connector touched a first-party process: last active %v -> %v", before, got)
	}
	if got := touch(context.Background()); !got.After(before) {
		t.Fatalf("touch did not reset the idle time: last active %v -> %v", before, got)
	}
}
