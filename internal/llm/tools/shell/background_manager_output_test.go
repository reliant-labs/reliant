// Copyright (c) 2025 Reliant Labs
//go:build !windows

package shell

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// awaitCompletion polls until the process is no longer running and returns
// its final status and exit code.
func awaitCompletion(t *testing.T, m *BackgroundManager, processID string, within time.Duration) (string, *int) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		status, complete, exitCode, err := m.GetProcessStatus(processID)
		if err != nil {
			t.Fatalf("GetProcessStatus: %v", err)
		}
		if complete {
			return status, exitCode
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %s still %q after %v", processID, status, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func combinedText(t *testing.T, m *BackgroundManager, processID string) []string {
	t.Helper()
	lines, err := m.GetCombinedOutput(processID)
	if err != nil {
		t.Fatalf("GetCombinedOutput: %v", err)
	}
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.Text
	}
	return texts
}

// TestStartProcess_FastCommandOutputIsCompleteAtCompletion pins the contract a
// bg_output caller relies on: once a process is reported finished, every line
// it wrote is in its output. A command as quick as `env` exits before the
// reader has run; cmd.Wait() used to close the output pipes underneath it
// ("it is incorrect to call Wait before all reads from the pipe have
// completed" — os/exec), so the process finished with some or ALL of its
// output silently gone. Many at once, because the loss needs the reader to
// lose the race against the process's exit.
func TestStartProcess_FastCommandOutputIsCompleteAtCompletion(t *testing.T) {
	m := newTestBGManager()
	const procs, linesEach = 40, 6
	var script strings.Builder
	for i := range linesEach {
		fmt.Fprintf(&script, "echo line-%d; ", i)
	}
	script.WriteString("echo to-stderr >&2")

	var wg sync.WaitGroup
	for range procs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			proc, err := m.StartProcess(context.Background(), StartProcessOptions{
				Command:    script.String(),
				WorkingDir: t.TempDir(),
			})
			if err != nil {
				t.Errorf("StartProcess: %v", err)
				return
			}
			status, exitCode := awaitCompletion(t, m, proc.ID, 30*time.Second)
			if status != "completed" || exitCode == nil || *exitCode != 0 {
				t.Errorf("status = %q exit = %v, want completed with exit 0", status, exitCode)
			}
			got := combinedText(t, m, proc.ID)
			if len(got) != linesEach+1 {
				t.Errorf("finished process has %d output lines, want %d: %q", len(got), linesEach+1, got)
				return
			}
			var stdoutLines []string
			for _, line := range got {
				if line != "to-stderr" {
					stdoutLines = append(stdoutLines, line)
				}
			}
			for i, line := range stdoutLines {
				if want := fmt.Sprintf("line-%d", i); line != want {
					t.Errorf("stdout line %d = %q, want %q (all: %q)", i, line, want, got)
				}
			}
		}()
	}
	wg.Wait()
}

// TestStartProcess_UnterminatedLastLineIsKept: output that does not end in a
// newline is still a line of output.
func TestStartProcess_UnterminatedLastLineIsKept(t *testing.T) {
	m := newTestBGManager()
	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    `printf 'first\r\nno newline at the end'`,
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	awaitCompletion(t, m, proc.ID, 30*time.Second)
	got := combinedText(t, m, proc.ID)
	if want := []string{"first", "no newline at the end"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestStartProcess_LingeringChildDoesNotHoldCompletion: draining output to
// EOF must not mean waiting for a process the command left running with the
// inherited stdout. The shell's own exit decides completion, after at most
// BackgroundWaitDelay of draining, and the bound expiring is not a failure.
func TestStartProcess_LingeringChildDoesNotHoldCompletion(t *testing.T) {
	prev := BackgroundWaitDelay
	BackgroundWaitDelay = 500 * time.Millisecond
	t.Cleanup(func() { BackgroundWaitDelay = prev })

	m := newTestBGManager()
	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    `echo "work done"; sleep 25 & exit 0`,
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	// The sleep shares the shell's process group; end it once the test is done.
	pgid := proc.GetPID()
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

	status, exitCode := awaitCompletion(t, m, proc.ID, 10*time.Second)
	if status != "completed" || exitCode == nil || *exitCode != 0 {
		t.Errorf("status = %q exit = %v, want completed with exit 0: the command itself succeeded", status, exitCode)
	}
	if got := combinedText(t, m, proc.ID); len(got) == 0 || got[0] != "work done" {
		t.Errorf("output = %q, want it to start with the line written before the bound expired", got)
	}
}
