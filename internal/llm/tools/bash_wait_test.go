// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// scriptedDaemon stands in for the machine running a background process. Each
// ListProcesses call advances a step, so a test can say "running, running, then
// exited" without sleeping for real time.
type scriptedDaemon struct {
	daemon.Client

	mu     sync.Mutex
	steps  []*daemon.ProcessInfo
	calls  int
	output string
	// listErrAfter injects a transient listing failure at step N (-1 = never),
	// so the retry path can be exercised.
	listErrAfter int
}

func (d *scriptedDaemon) ListProcesses(_ context.Context) ([]*daemon.ProcessInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	step := d.calls
	d.calls++

	if d.listErrAfter >= 0 && step == d.listErrAfter {
		return nil, fmt.Errorf("transient daemon listing failure")
	}
	if step >= len(d.steps) {
		step = len(d.steps) - 1
	}
	if d.steps[step] == nil {
		return []*daemon.ProcessInfo{}, nil
	}
	return []*daemon.ProcessInfo{d.steps[step]}, nil
}

func (d *scriptedDaemon) GetProcessOutput(_ context.Context, _ string, _ *daemon.OutputOpts) (*daemon.ProcessOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &daemon.ProcessOutput{Output: d.output, TotalBytes: len(d.output)}, nil
}

func (d *scriptedDaemon) listCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func running(id string) *daemon.ProcessInfo {
	return &daemon.ProcessInfo{ID: id, Command: "npm test", Status: "running", StartTime: time.Now()}
}

func exited(id string, code int) *daemon.ProcessInfo {
	now := time.Now()
	return &daemon.ProcessInfo{
		ID: id, Command: "npm test", Status: "completed",
		ExitCode: &code, StartTime: now, EndTime: &now,
	}
}

func waitCtx(t *testing.T, d daemon.Client) *rctx.ToolContext {
	t.Helper()
	return &rctx.ToolContext{
		Daemon:   d,
		Context:  context.Background(),
		ChatID:   "chat-1",
		Worktree: &rctx.WorktreeInfo{Path: t.TempDir()},
	}
}

func runWait(t *testing.T, d daemon.Client, params BashWaitParams) (ToolResponse, BashWaitResponseMetadata) {
	t.Helper()
	resp, err := (&bashWaitTool{}).Execute(waitCtx(t, d), params)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var meta BashWaitResponseMetadata
	if resp.Metadata != "" {
		if err := json.Unmarshal([]byte(resp.Metadata), &meta); err != nil {
			t.Fatalf("unmarshal metadata %q: %v", resp.Metadata, err)
		}
	}
	return resp, meta
}

// A process that has already finished must return immediately with its exit
// code — the whole point is that waiting costs no model round-trips.
func TestBashWait_ReturnsExitCodeWhenProcessExits(t *testing.T) {
	t.Parallel()
	d := &scriptedDaemon{
		steps:        []*daemon.ProcessInfo{running("p1"), running("p1"), exited("p1", 0)},
		output:       "ok 42 tests passed",
		listErrAfter: -1,
	}

	resp, meta := runWait(t, d, BashWaitParams{ProcessID: "p1"})

	if !meta.HasExited {
		t.Error("has_exited must be true once the process is done")
	}
	if meta.TimedOut {
		t.Error("a process that exited did not time out")
	}
	if meta.ExitCode == nil || *meta.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", meta.ExitCode)
	}
	// The exit code alone usually is not the answer — a failing suite needs its
	// output. Returning it here is what removes the follow-up bash_output call.
	if !strings.Contains(resp.Content, "42 tests passed") {
		t.Errorf("output tail must accompany the exit code, got: %s", resp.Content)
	}
	if d.listCalls() < 3 {
		t.Errorf("expected polling until exit, got %d list calls", d.listCalls())
	}
}

// A still-running process is NOT an error. Returning one would teach the model
// that a slow build failed, which is the opposite of what happened.
func TestBashWait_TimeoutIsNotAnError(t *testing.T) {
	t.Parallel()
	d := &scriptedDaemon{
		steps:        []*daemon.ProcessInfo{running("p1")},
		listErrAfter: -1,
	}

	resp, meta := runWait(t, d, BashWaitParams{ProcessID: "p1", TimeoutSeconds: 1})

	if resp.IsError {
		t.Error("a still-running process must not be reported as a tool error")
	}
	if !meta.TimedOut {
		t.Error("timed_out must be true when the budget elapses")
	}
	if meta.HasExited {
		t.Error("has_exited must be false — the process is still going")
	}
	if meta.ExitCode != nil {
		t.Errorf("no exit code exists yet, got %v", meta.ExitCode)
	}
	// The model has to know the process survived and that calling again is the
	// move; without this it may assume the wait killed it or that it failed.
	if !strings.Contains(resp.Content, "STILL RUNNING") || !strings.Contains(resp.Content, "not been killed") {
		t.Errorf("must say the process is alive and untouched, got: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "bash_wait again") {
		t.Errorf("must name the next action, got: %s", resp.Content)
	}
}

// A bad process id must fail fast. Blocking the full budget and then saying
// "not found" wastes exactly the time this tool exists to save.
func TestBashWait_UnknownProcessFailsFast(t *testing.T) {
	t.Parallel()
	d := &scriptedDaemon{steps: []*daemon.ProcessInfo{nil}, listErrAfter: -1}

	start := time.Now()
	resp, err := (&bashWaitTool{}).Execute(waitCtx(t, d), BashWaitParams{ProcessID: "nope"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	elapsed := time.Since(start)

	if !resp.IsError {
		t.Error("an unknown process id is a real error — nothing will ever arrive")
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %s to reject an unknown id; must not consume the wait budget", elapsed)
	}
	if !strings.Contains(resp.Content, "bash_list") {
		t.Errorf("must point at how to find valid ids, got: %s", resp.Content)
	}
}

// The budget cannot exceed what one tool call is allowed to take: the
// tool-execution context cancels every call at 5 minutes, so a wait that tried
// to outlast it would be killed mid-flight and report a hard timeout — the very
// failure `sleep 300` produced.
func TestBashWait_ClampsTimeoutBelowToolCeiling(t *testing.T) {
	t.Parallel()
	d := &scriptedDaemon{steps: []*daemon.ProcessInfo{exited("p1", 0)}, listErrAfter: -1}

	// Ask for an hour; the call must still return promptly.
	_, meta := runWait(t, d, BashWaitParams{ProcessID: "p1", TimeoutSeconds: 3600})
	if !meta.HasExited {
		t.Error("an already-exited process must be reported immediately")
	}

	// The invariant is relative, not a fixed number: whatever the ceiling is,
	// the max wait must stay under it so the tool answers "still running,
	// call again" instead of being cancelled mid-flight. Comparing against
	// the constant keeps the two from drifting apart the next time either
	// moves — this assertion previously hard-coded 5m and broke when the
	// ceiling was raised for long builds.
	// The invariant is relative, not a fixed number. toolexec.DefaultToolTimeout
	// is derived from MaxBlockingToolWait by adding headroom, and toolexec
	// imports this package, so the check is stated from this side: the wait
	// budget must equal the shared maximum, and the executor's ceiling adds to
	// it. This previously hard-coded 5m and broke when the ceiling was raised.
	if bashWaitMaxTimeout != MaxBlockingToolWait {
		t.Errorf("max wait %s must be the shared blocking-tool maximum %s, or the executor ceiling derived from it no longer leaves headroom",
			bashWaitMaxTimeout, MaxBlockingToolWait)
	}
}

// A transient listing failure must not abandon a wait that may be nearly done.
func TestBashWait_SurvivesTransientListingFailure(t *testing.T) {
	t.Parallel()
	d := &scriptedDaemon{
		steps:        []*daemon.ProcessInfo{running("p1"), running("p1"), exited("p1", 3)},
		output:       "FAILED",
		listErrAfter: 1, // fail the first refresh, then recover
	}

	_, meta := runWait(t, d, BashWaitParams{ProcessID: "p1", TimeoutSeconds: 10})

	if !meta.HasExited {
		t.Error("a transient listing error must not end the wait")
	}
	if meta.ExitCode == nil || *meta.ExitCode != 3 {
		t.Errorf("exit code = %v, want 3 — the real result must survive the blip", meta.ExitCode)
	}
}

// Cancelling the surrounding tool call must stop the loop rather than keep
// polling a daemon for a result nobody is waiting on.
func TestBashWait_StopsWhenToolCallCancelled(t *testing.T) {
	t.Parallel()
	d := &scriptedDaemon{steps: []*daemon.ProcessInfo{running("p1")}, listErrAfter: -1}

	ctx, cancel := context.WithCancel(context.Background())
	tc := &rctx.ToolContext{
		Daemon:   d,
		Context:  ctx,
		ChatID:   "chat-1",
		Worktree: &rctx.WorktreeInfo{Path: t.TempDir()},
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	resp, err := (&bashWaitTool{}).Execute(tc, BashWaitParams{ProcessID: "p1", TimeoutSeconds: 240})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("kept polling for %s after cancellation", elapsed)
	}
	if resp.IsError {
		t.Error("cancellation should report the last known state, not an error")
	}
}
