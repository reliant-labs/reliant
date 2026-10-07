// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
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

// The detached command has to actually keep running. Reporting a process id
// is worthless if the process behind it is already dead.
//
// It was: RunCommand ran the command under a context it cancelled on return
// (`defer cancel()`), and os/exec answers a cancelled context by calling
// cmd.Cancel — the graceful SIGTERM to the process group. So the instant the
// handoff was reported, the handed-off command was terminated, and shell_output
// showed it "failed". Even without the deferred cancel, the foreground timeout
// would have killed it at TimeoutMs, which no longer applies to a background
// process.
//
// The command outlives its own foreground timeout on purpose: 1s timeout,
// 2s of work. A background process is not bound by the foreground budget.
func TestRunCommand_DetachedCommandKeepsRunning(t *testing.T) {
	asked := false
	ctx := WithBackgroundProbe(context.Background(), func() (string, bool) {
		if asked {
			return "", false
		}
		asked = true
		return "toolu_detach_survives", true
	})

	res, err := NewLocalClient().RunCommand(ctx, &RunCommandRequest{
		Command:    "sleep 2; echo survived",
		WorkingDir: t.TempDir(),
		TimeoutMs:  1000,
	})
	if err != nil {
		t.Fatalf("RunCommand returned a transport error: %v", err)
	}
	if !res.Backgrounded {
		t.Fatalf("Backgrounded = false, want true; result was %+v", res)
	}

	assertBackgroundProcessCompletes(t, res.ProcessID, "survived")
}

// assertBackgroundProcessCompletes waits for an adopted process to finish and
// requires that it finished on its own, with the output it was going to write.
func assertBackgroundProcessCompletes(t *testing.T, processID, wantOutput string) {
	t.Helper()
	bgm := shell.GetBackgroundManager()
	deadline := time.Now().Add(15 * time.Second)
	for {
		status, done, exitCode, err := bgm.GetProcessStatus(processID)
		if err != nil {
			t.Fatalf("GetProcessStatus(%s): %v", processID, err)
		}
		if done {
			stdout, stderr, _ := bgm.GetOutput(processID)
			if status != "completed" || exitCode == nil || *exitCode != 0 {
				code := "<nil>"
				if exitCode != nil {
					code = strconv.Itoa(*exitCode)
				}
				t.Fatalf("detached process ended with status=%q exit=%s, want completed/0 — "+
					"it was stopped after the handoff instead of being left to run "+
					"(stdout=%q stderr=%q)", status, code, stdout, stderr)
			}
			if !strings.Contains(stdout, wantOutput) {
				t.Fatalf("detached process stdout = %q, want it to contain %q", stdout, wantOutput)
			}
			return
		}
		if time.Now().After(deadline) {
			_ = bgm.KillProcess(processID)
			t.Fatalf("detached process %s still %q after 15s", processID, status)
		}
		time.Sleep(50 * time.Millisecond)
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
