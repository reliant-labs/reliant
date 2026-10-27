// Copyright (c) 2025 Reliant Labs
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/shellaudit"
)

// lockedBuffer is a log sink the parallel tests in this package can share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) entriesFor(t *testing.T, chatID string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("audit line is not JSON: %q", line)
		}
		if e["chat_id"] == chatID {
			out = append(out, e)
		}
	}
	return out
}

type exitingDaemon struct {
	daemon.Client
	exit int
}

func (d *exitingDaemon) RunCommand(_ context.Context, _ *daemon.RunCommandRequest) (*daemon.CommandResult, error) {
	return &daemon.CommandResult{ExitCode: d.exit, DurationMs: 3}, nil
}

func (d *exitingDaemon) StartBackground(_ context.Context, _ *daemon.RunCommandRequest) (string, error) {
	return "proc-9", nil
}

// Not parallel: it installs the process-wide audit log for its duration. Every
// other test in this package runs with none installed, and the entries here are
// told apart by chat id.
func TestShellTool_AuditsEveryInvocation(t *testing.T) {
	sink := &lockedBuffer{}
	restore := shellaudit.SetDefault(shellaudit.New(sink))
	t.Cleanup(func() { shellaudit.SetDefault(restore) })

	run := func(chatID string, d daemon.Client, p ShellParams) {
		t.Helper()
		tc := &rctx.ToolContext{
			Context: context.Background(),
			ChatID:  chatID,
			Thread:  "thread-" + chatID,
			Project: &db.Project{ID: "p", Path: "/work/p"},
			Daemon:  d,
		}
		if _, err := (&shellTool{}).Execute(tc, p); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	}

	// A foreground command: exit code, duration, cwd, ids; secrets redacted.
	run("audit-fg", &exitingDaemon{exit: 3}, ShellParams{
		Command: "GITHUB_TOKEN=ghp_0123456789abcdefghij0123456789abcdef git push --password=hunter2hunter2",
	})
	got := sink.entriesFor(t, "audit-fg")
	if len(got) != 1 {
		t.Fatalf("want one audit line, got %d: %v", len(got), got)
	}
	e := got[0]
	if e["thread"] != "thread-audit-fg" || e["cwd"] != "/work/p" || e["outcome"] != "completed" {
		t.Errorf("identity fields wrong: %v", e)
	}
	if e["exit_code"] != float64(3) {
		t.Errorf("exit_code = %v, want 3", e["exit_code"])
	}
	if _, ok := e["duration_ms"]; !ok {
		t.Errorf("no duration_ms: %v", e)
	}
	if cmd := e["command"].(string); strings.Contains(cmd, "ghp_0123") || strings.Contains(cmd, "hunter2") || !strings.Contains(cmd, "git push") {
		t.Errorf("command not redacted or mangled: %q", cmd)
	}

	// A refused command is recorded too — it is the evidence that someone tried.
	run("audit-refused", &exitingDaemon{}, ShellParams{Command: "go clean -cache"})
	got = sink.entriesFor(t, "audit-refused")
	if len(got) != 1 || got[0]["outcome"] != "refused" || got[0]["refusal"] != "shared_state" {
		t.Fatalf("refused command not audited as refused: %v", got)
	}
	if _, ok := got[0]["exit_code"]; ok {
		t.Errorf("a command that never ran has no exit code: %v", got[0])
	}

	// A background command is logged when it starts.
	run("audit-bg", &exitingDaemon{}, ShellParams{Command: "sleep 100", RunInBackground: true})
	got = sink.entriesFor(t, "audit-bg")
	if len(got) != 1 || got[0]["outcome"] != "backgrounded" || got[0]["background"] != true || got[0]["process_id"] != "proc-9" {
		t.Fatalf("background command not audited: %v", got)
	}
}
