// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The "push to background" button reaches a RUNNING command through this path,
// and this is the path an LLM tool call actually takes.
//
// The daemon runtime hands its local tool executor a LocalClient
// (runtime.go: localExec.SetDaemonClient(daemon.NewLocalClient())), so the
// shell tool's rctx.Daemon.RunCommand lands HERE — not in the exec.run command
// handler. Until this test existed only exec.run honoured the detach request,
// so the button marked the row BACKGROUNDED, told the UI it worked, and left
// the command running in the foreground still blocking the workflow.
//
// Both exec paths must observe the same probe, or the feature works only for
// whichever path the caller happened to take.
func TestRunCommand_BackgroundDetach(t *testing.T) {
	askToBackground := make(chan struct{})
	consumed := false

	ctx := WithBackgroundProbe(context.Background(), func() (string, bool) {
		if consumed {
			return "", false
		}
		select {
		case <-askToBackground:
			consumed = true
			return "toolu_test", true
		default:
			return "", false
		}
	})

	// Ask for the detach shortly after the command starts. The command sleeps
	// far longer than that, so if RunCommand returns promptly it can only be
	// because it detached.
	go func() {
		time.Sleep(300 * time.Millisecond)
		close(askToBackground)
	}()

	start := time.Now()
	res, err := NewLocalClient().RunCommand(ctx, &RunCommandRequest{
		Command:    "sleep 30",
		WorkingDir: t.TempDir(),
		TimeoutMs:  60000,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunCommand returned a transport error: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("RunCommand blocked for %v — it waited for the command instead of "+
			"detaching it, which is exactly the bug: the button reports success "+
			"while the workflow stays blocked", elapsed)
	}
	if !res.Backgrounded {
		t.Fatalf("Backgrounded = false, want true — the command was still running "+
			"when the user asked to detach it; result was %+v", res)
	}
	if strings.TrimSpace(res.ProcessID) == "" {
		t.Fatalf("ProcessID is empty — shell_output and shell_kill have nothing to "+
			"address, and the tool_calls row gets a NULL background_process_id; "+
			"result was %+v", res)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 for a successful handoff", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, res.ProcessID) {
		t.Errorf("Stdout = %q, want it to name the process id %q so the model can "+
			"read the output it was handed", res.Stdout, res.ProcessID)
	}
}

// A command that finishes before anyone asks to background it must report its
// real output, not a fabricated handoff.
func TestRunCommand_NoBackgroundRequest_ReportsNormally(t *testing.T) {
	ctx := WithBackgroundProbe(context.Background(), func() (string, bool) {
		return "", false
	})

	res, err := NewLocalClient().RunCommand(ctx, &RunCommandRequest{
		Command:    "echo hello",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RunCommand returned a transport error: %v", err)
	}
	if res.Backgrounded {
		t.Errorf("Backgrounded = true for a command nobody asked to detach")
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Errorf("Stdout = %q, want the command's real output", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

// No probe installed (every non-daemon caller and most tests) must behave
// exactly as before.
func TestRunCommand_NoProbe_Unaffected(t *testing.T) {
	res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
		Command:    "echo hello",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RunCommand returned a transport error: %v", err)
	}
	if res.Backgrounded {
		t.Errorf("Backgrounded = true with no probe installed")
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Errorf("Stdout = %q, want the command's real output", res.Stdout)
	}
}
