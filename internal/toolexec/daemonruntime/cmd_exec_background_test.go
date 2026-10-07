// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
)

// TestHandleExecRun_DetachedCommandKeepsRunning is the exec.run twin of
// daemon.TestRunCommand_DetachedCommandKeepsRunning: a command pushed to the
// background must survive the handoff. The handler used to run it under a
// context it cancelled on return, so os/exec SIGTERMed the process group the
// moment the handoff was reported, and the foreground timeout would have
// killed it anyway. Both exec paths share daemon.ExecDeadline so they cannot
// drift on this.
func TestHandleExecRun_DetachedCommandKeepsRunning(t *testing.T) {
	asked := false
	ctx := daemon.WithBackgroundProbe(context.Background(), func() (string, bool) {
		if asked {
			return "", false
		}
		asked = true
		return "toolu_exec_run_detach", true
	})

	payload, err := json.Marshal(daemon.RunCommandRequest{
		Command:    "sleep 2; echo survived",
		WorkingDir: t.TempDir(),
		TimeoutMs:  1000,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	raw, err := handleExecRun(ctx, payload)
	if err != nil {
		t.Fatalf("handleExecRun returned a transport error: %v", err)
	}
	var res daemon.CommandResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !res.Backgrounded {
		t.Fatalf("Backgrounded = false, want true; result was %+v", res)
	}

	bgm := shell.GetBackgroundManager()
	deadline := time.Now().Add(15 * time.Second)
	for {
		status, done, exitCode, err := bgm.GetProcessStatus(res.ProcessID)
		if err != nil {
			t.Fatalf("GetProcessStatus(%s): %v", res.ProcessID, err)
		}
		if done {
			stdout, stderr, _ := bgm.GetOutput(res.ProcessID)
			if status != "completed" || exitCode == nil || *exitCode != 0 {
				code := "<nil>"
				if exitCode != nil {
					code = strconv.Itoa(*exitCode)
				}
				t.Fatalf("detached process ended with status=%q exit=%s, want completed/0 — "+
					"it was stopped after the handoff instead of being left to run "+
					"(stdout=%q stderr=%q)", status, code, stdout, stderr)
			}
			if !strings.Contains(stdout, "survived") {
				t.Fatalf("detached process stdout = %q, want it to contain %q", stdout, "survived")
			}
			return
		}
		if time.Now().After(deadline) {
			_ = bgm.KillProcess(res.ProcessID)
			t.Fatalf("detached process %s still %q after 15s", res.ProcessID, status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
