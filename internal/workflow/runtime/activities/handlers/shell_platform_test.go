// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// fakeDaemonReader is a hand-rolled stand-in for the two repository reads the
// resolver performs. It is deliberately tiny: the interface it satisfies is
// declared at the consumer, so a fake needs no knowledge of db.Repository.
type fakeDaemonReader struct {
	byID    map[string]*db.Daemon
	byUser  map[string][]*db.Daemon
	getErr  error
	listErr error
}

func (f *fakeDaemonReader) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	d, ok := f.byID[id]
	if !ok {
		return nil, errors.New("no such daemon")
	}
	return d, nil
}

func (f *fakeDaemonReader) ListDaemonsByUserID(_ context.Context, userID string) ([]*db.Daemon, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.byUser[userID], nil
}

func daemonOn(id, platform string) *db.Daemon {
	p := platform
	return &db.Daemon{ID: id, Platform: &p}
}

func strPtr(s string) *string { return &s }

// THE test this change exists for: a Windows daemon record, read on a server
// that is not Windows, must produce PowerShell guidance. Before this change the
// description came from the server's build tags, so this machine would have
// told the model it had bash.
func TestWindowsDaemonYieldsPowerShellGuidanceFromNonWindowsServer(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the bug is a NON-Windows server describing a Windows daemon")
	}

	repo := &fakeDaemonReader{
		byID: map[string]*db.Daemon{"daemon-win": daemonOn("daemon-win", "windows")},
	}
	chat := &db.Chat{ID: "chat-1", UserID: "user-1", ActiveDaemonID: strPtr("daemon-win")}

	platform := resolveShellPlatform(context.Background(), repo, chat, "")
	if platform != tools.ShellPlatformWindows {
		t.Fatalf("resolved platform = %q, want windows", platform)
	}

	// Carry it the rest of the way, exactly as call_llm does, and assert on
	// what the model would actually be handed.
	desc := tools.NewToolsFactory(&tools.ToolsOptions{}).
		WithShellPlatform(platform).
		Shell().
		Description()

	if !strings.Contains(desc, "PowerShell") {
		t.Errorf("description handed to the model lacks PowerShell guidance:\n%s", desc)
	}
	if strings.Contains(desc, "Uses bash -c") {
		t.Errorf("description handed to the model still claims bash -c:\n%s", desc)
	}
}

// The worktree's owning daemon wins, because ExecuteTools routes there first.
// Describing the chat's pinned daemon while the command runs on the worktree's
// daemon would be the same mismatch in a new place.
func TestWorktreeDaemonOutranksChatPinnedDaemon(t *testing.T) {
	t.Parallel()
	repo := &fakeDaemonReader{
		byID: map[string]*db.Daemon{
			"daemon-win":   daemonOn("daemon-win", "windows"),
			"daemon-linux": daemonOn("daemon-linux", "linux"),
		},
	}
	chat := &db.Chat{ID: "c", UserID: "u", ActiveDaemonID: strPtr("daemon-linux")}

	if got := resolveShellPlatform(context.Background(), repo, chat, "daemon-win"); got != tools.ShellPlatformWindows {
		t.Errorf("got %q, want windows (the worktree's daemon)", got)
	}
}

func TestFallsBackToSoleUserDaemon(t *testing.T) {
	t.Parallel()
	repo := &fakeDaemonReader{
		byUser: map[string][]*db.Daemon{"u": {daemonOn("d1", "darwin")}},
	}
	chat := &db.Chat{ID: "c", UserID: "u"}

	if got := resolveShellPlatform(context.Background(), repo, chat, ""); got != tools.ShellPlatformUnix {
		t.Errorf("got %q, want unix", got)
	}
}

// A mixed fleet has no single right answer, so guessing one and asserting it as
// fact is worse than admitting the unknown.
func TestMixedFleetDegradesToUnknown(t *testing.T) {
	t.Parallel()
	repo := &fakeDaemonReader{
		byUser: map[string][]*db.Daemon{"u": {
			daemonOn("d1", "linux"),
			daemonOn("d2", "windows"),
		}},
	}
	chat := &db.Chat{ID: "c", UserID: "u"}

	if got := resolveShellPlatform(context.Background(), repo, chat, ""); got != tools.ShellPlatformUnknown {
		t.Errorf("got %q, want unknown for a mixed fleet", got)
	}
}

// Every degenerate input degrades to unknown rather than erroring or assuming
// bash. A tool description is advisory; it must never fail an LLM call.
func TestResolveShellPlatformDegradesSafely(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chat := &db.Chat{ID: "c", UserID: "u", ActiveDaemonID: strPtr("missing")}

	for name, repo := range map[string]daemonPlatformReader{
		"daemon lookup fails":   &fakeDaemonReader{getErr: errors.New("boom"), listErr: errors.New("boom")},
		"listing fails":         &fakeDaemonReader{listErr: errors.New("boom")},
		"no daemons at all":     &fakeDaemonReader{},
		"daemon has nil platfm": &fakeDaemonReader{byUser: map[string][]*db.Daemon{"u": {{ID: "d1"}}}},
		"unrecognized GOOS":     &fakeDaemonReader{byUser: map[string][]*db.Daemon{"u": {daemonOn("d1", "plan9")}}},
	} {
		if got := resolveShellPlatform(ctx, repo, chat, ""); got != tools.ShellPlatformUnknown {
			t.Errorf("%s: got %q, want unknown", name, got)
		}
	}

	if got := resolveShellPlatform(ctx, nil, chat, ""); got != tools.ShellPlatformUnknown {
		t.Errorf("nil repo: got %q, want unknown", got)
	}
	if got := resolveShellPlatform(ctx, &fakeDaemonReader{}, nil, ""); got != tools.ShellPlatformUnknown {
		t.Errorf("nil chat: got %q, want unknown", got)
	}
}
