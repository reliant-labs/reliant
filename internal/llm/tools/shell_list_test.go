// Copyright (c) 2026 Reliant Labs
package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// listingDaemon answers ListProcesses with a fixed table and records touches.
type listingDaemon struct {
	daemon.Client
	processes []*daemon.ProcessInfo
	touched   []string
}

func (d *listingDaemon) ListProcesses(context.Context) ([]*daemon.ProcessInfo, error) {
	return d.processes, nil
}

func (d *listingDaemon) TouchProcesses(_ context.Context, ids []string) error {
	d.touched = append(d.touched, ids...)
	return nil
}

// shell_list lists the current workspace's processes, and listing a running
// one is using it: it is touched, so the idle reaper's clock restarts. An
// unscoped list would let any chat on the machine keep every other
// workspace's leftovers alive. Each running process says how idle it is and
// when it could be stopped.
func TestShellList_ScopedToTheWorkspaceTouchesWhatItShowsAndSaysWhenItStops(t *testing.T) {
	workspace := t.TempDir()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	lastActive := now.Add(-90 * time.Minute)
	stopsAfter := lastActive.Add(4 * time.Hour)
	ended := now
	d := &listingDaemon{processes: []*daemon.ProcessInfo{
		{ID: "p-dev", Command: "npm run dev", Status: "running", StartTime: lastActive,
			WorkingDir: filepath.Join(workspace, "frontends", "web"), LastActive: lastActive, StopsAfter: &stopsAfter},
		{ID: "p-server", Command: "serve", Status: "running", StartTime: lastActive,
			WorkingDir: workspace, LastActive: lastActive, StopExempt: "keep_alive"},
		{ID: "p-done", Command: "go test", Status: "completed", StartTime: lastActive, EndTime: &ended, WorkingDir: workspace},
		{ID: "p-elsewhere", Command: "vite preview", Status: "running", StartTime: lastActive,
			WorkingDir: workspace + "-other", LastActive: lastActive, StopsAfter: &stopsAfter},
	}}
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1", Daemon: d, Worktree: &rctx.WorktreeInfo{Path: workspace}}

	resp, err := (&shellListTool{clock: func() time.Time { return now }}).Execute(tc, ShellListParams{})
	if err != nil {
		t.Fatal(err)
	}
	out := resp.Content
	for _, want := range []string{"p-dev", "p-server", "Idle for: 1 hours, 30 minutes",
		"Auto-stop: after 4 hours, 0 minutes idle once its chat has no run in progress; not before 2026-11-02T16:00:00Z",
		"Auto-stop: never (keep_alive)"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"p-elsewhere", "p-done"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("listing shows %s:\n%s", unwanted, out)
		}
	}
	if got := strings.Join(d.touched, ","); got != "p-dev,p-server" {
		t.Errorf("touched %q, want the running processes it showed (p-dev,p-server)", got)
	}
}
