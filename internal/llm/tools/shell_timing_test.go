// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// stallDaemon stands in for the transport between the shell tool and the
// machine running the command: it reports a command duration of its own
// choosing and burns `stall` on top of it, which is exactly the shape of a
// slow NATS round trip or a queued request.
type stallDaemon struct {
	daemon.Client
	stall      time.Duration
	durationMs int64
}

func (d *stallDaemon) RunCommand(_ context.Context, _ *daemon.RunCommandRequest) (*daemon.CommandResult, error) {
	time.Sleep(d.stall)
	return &daemon.CommandResult{
		Stdout:     "hello\n",
		Combined:   "hello\n",
		DurationMs: d.durationMs,
	}, nil
}

func runShell(t *testing.T, d *stallDaemon) (BashOutput, ShellResponseMetadata) {
	t.Helper()
	tc := &rctx.ToolContext{
		Daemon:   d,
		Context:  context.Background(),
		ChatID:   "chat-1",
		Worktree: &rctx.WorktreeInfo{Path: t.TempDir()},
	}
	resp, err := (&shellTool{}).Execute(tc, ShellParams{Command: "echo hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out BashOutput
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil {
		t.Fatalf("unmarshal tool content %q: %v", resp.Content, err)
	}
	var meta ShellResponseMetadata
	if err := json.Unmarshal([]byte(resp.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal tool metadata %q: %v", resp.Metadata, err)
	}
	return out, meta
}

// TestShell_DurationAlwaysInMetadata pins the forensics half of the policy.
// ToolResponse.Metadata is persisted but never sent to the model (only
// ToolResult.Content reaches a provider), so recording the command's own
// runtime there is free of context cost and available on EVERY call. This is
// what makes a future "why did that take ten minutes?" answerable from the
// stored transcript instead of from four independent lines of evidence.
func TestShell_DurationAlwaysInMetadata(t *testing.T) {
	_, meta := runShell(t, &stallDaemon{stall: 0, durationMs: 1234})

	if meta.CommandDurationMs != 1234 {
		t.Errorf("metadata CommandDurationMs = %d, want 1234 — the daemon measures this "+
			"and it must not be discarded", meta.CommandDurationMs)
	}
}

// TestShell_NoTimingBlockWhenClocksAgree pins the model-facing half. A number
// on every result is baseline noise the model learns to skip and pure token
// cost across thousands of calls; the signal is the DISAGREEMENT, not the
// number.
func TestShell_NoTimingBlockWhenClocksAgree(t *testing.T) {
	out, _ := runShell(t, &stallDaemon{stall: 0, durationMs: 5})

	if out.Timing != nil {
		t.Errorf("Timing = %+v, want nil when the two clocks agree", out.Timing)
	}
}

// TestShell_TimingBlockWhenTransportDominates is the case that made the 608s
// gate unattributable: the tool call cost far more wall-clock than the command
// itself ran. When two independent clocks disagree, say so.
func TestShell_TimingBlockWhenTransportDominates(t *testing.T) {
	out, _ := runShell(t, &stallDaemon{stall: 1500 * time.Millisecond, durationMs: 20})

	if out.Timing == nil {
		t.Fatalf("Timing = nil, want a timing block when the tool call cost ~1.5s " +
			"for a command that ran 20ms")
	}
	if out.Timing.CommandMs != 20 {
		t.Errorf("CommandMs = %d, want 20", out.Timing.CommandMs)
	}
	if out.Timing.WallMs < 1500 {
		t.Errorf("WallMs = %d, want >= 1500", out.Timing.WallMs)
	}
	if out.Timing.Note == "" {
		t.Errorf("Note is empty — the numbers alone do not say which clock is which")
	}
}

// TestShell_NoTimingBlockForSmallOverheadOnSlowCommand is the noise guard: a
// genuinely long command with ordinary transport overhead must stay quiet,
// even though the absolute gap is larger than on a fast call.
func TestShell_NoTimingBlockForSmallOverheadOnSlowCommand(t *testing.T) {
	// 1.2s of stall against a command that ran 300s: a 0.4% overhead.
	out, _ := runShell(t, &stallDaemon{stall: 1200 * time.Millisecond, durationMs: 300_000})

	if out.Timing != nil {
		t.Errorf("Timing = %+v, want nil — %dms of overhead on a 300s command is normal",
			out.Timing, out.Timing.WallMs-out.Timing.CommandMs)
	}
}
