// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// detachingDaemon stands in for a daemon that detached the command mid-run
// because the user pushed it to the background: the command did NOT complete,
// so the result carries Backgrounded and a process id instead of real output.
type detachingDaemon struct {
	daemon.Client
	processID string
}

func (d *detachingDaemon) RunCommand(_ context.Context, _ *daemon.RunCommandRequest) (*daemon.CommandResult, error) {
	return &daemon.CommandResult{
		Stdout:       "Command detached into a background process.\nProcess ID: " + d.processID,
		ExitCode:     0,
		Backgrounded: true,
		ProcessID:    d.processID,
	}, nil
}

// A command detached mid-run must be reported as backgrounded, not as a
// completed command. The workflow keys the tool_calls row off
// ToolResponse.Backgrounded (execute_tools.go), and the model needs the process
// id to reach the still-running command with shell_output / shell_kill.
//
// Without this the tool reported a normal completion whose "output" was the
// handoff notice — a command that never finished, recorded as if it had, with a
// NULL background_process_id on the row.
func TestShell_MidRunDetachReportedAsBackgrounded(t *testing.T) {
	const processID = "bg-process-1"

	tc := &rctx.ToolContext{
		Daemon:   &detachingDaemon{processID: processID},
		Context:  context.Background(),
		ChatID:   "chat-1",
		Worktree: &rctx.WorktreeInfo{Path: t.TempDir()},
	}

	resp, err := (&shellTool{}).Execute(tc, ShellParams{Command: "sleep 300"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !resp.Backgrounded {
		t.Fatalf("ToolResponse.Backgrounded = false — the workflow will record a " +
			"completion for a command that is still running")
	}

	var out BashBackgroundOutput
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil {
		t.Fatalf("tool content %q is not the background envelope: %v", resp.Content, err)
	}
	if !out.Backgrounded {
		t.Errorf("content backgrounded = false, want true")
	}
	if out.ProcessID != processID {
		t.Errorf("content process_id = %q, want %q — the model cannot reach the "+
			"running command without it", out.ProcessID, processID)
	}

	var meta ShellResponseMetadata
	if err := json.Unmarshal([]byte(resp.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal metadata %q: %v", resp.Metadata, err)
	}
	if meta.ProcessID != processID {
		t.Errorf("metadata process_id = %q, want %q — this is what lands on the "+
			"durable tool_calls row", meta.ProcessID, processID)
	}
}
