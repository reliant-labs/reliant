// Copyright (c) 2026 Reliant Labs
package tools

import (
	"context"
	"fmt"
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

// reliant#2751: a machine that has run hundreds of workflow commands, each a
// multi-KB inline script, answered shell_list(all=true) with ~930 KB, cut off
// before it could say whether a given process was still running. The listing
// is bounded: running processes first, a page of at most `limit`, each command
// shortened, and a footer naming the next page. Only what was shown is touched.
func TestShellList_IsBoundedRunningFirstAndPages(t *testing.T) {
	workspace := t.TempDir()
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	script := "python3 - <<'EOF'\n" + strings.Repeat("x = 1\n", 2000) + "EOF"
	var procs []*daemon.ProcessInfo
	for i := 0; i < 300; i++ {
		ended := now.Add(-time.Duration(i) * time.Minute)
		procs = append(procs, &daemon.ProcessInfo{ID: fmt.Sprintf("done-%03d", i), Command: script, Status: "completed",
			StartTime: ended.Add(-time.Minute), EndTime: &ended, WorkingDir: workspace})
	}
	// Started long ago, listed last by the daemon: still on the first page.
	procs = append(procs, &daemon.ProcessInfo{ID: "the-server", Command: "npm run dev", Status: "running",
		StartTime: now.Add(-48 * time.Hour), WorkingDir: workspace})
	d := &listingDaemon{processes: procs}
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1", Daemon: d, Worktree: &rctx.WorktreeInfo{Path: workspace}}
	tool := &shellListTool{clock: func() time.Time { return now }}

	resp, err := tool.Execute(tc, ShellListParams{All: true})
	if err != nil {
		t.Fatal(err)
	}
	out := resp.Content
	if len(out) > 32<<10 {
		t.Fatalf("a default page is %d bytes; want it bounded (under 32 KiB)", len(out))
	}
	if !strings.HasPrefix(out, "=== Background Processes ===\n\nProcess ID: the-server\nStatus: running") {
		t.Errorf("the running process is not listed first:\n%.300s", out)
	}
	if got := strings.Count(out, "Process ID: "); got != 50 {
		t.Errorf("page shows %d processes, want 50", got)
	}
	for _, want := range []string{"… (", "Total: 301 processes (1 running)", "Showing 1-50 of 301. 251 more: shell_list(offset=50, all=true)."} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q", want)
		}
	}
	if got := strings.Join(d.touched, ","); got != "the-server" {
		t.Errorf("touched %q, want only the running process shown", got)
	}

	resp, err = tool.Execute(tc, ShellListParams{All: true, Offset: 290, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "Process ID: done-289\n") || !strings.Contains(resp.Content, "Process ID: done-299\n") || strings.Contains(resp.Content, "more: shell_list") ||
		!strings.Contains(resp.Content, "Showing 291-301 of 301.") {
		t.Errorf("the last page is wrong:\n%s", resp.Content)
	}
}
