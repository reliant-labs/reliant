// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// The habit this exists for: an agent in ~/projects/x/reliant running
// `git worktree add ../reliant-feature -b feature` puts a checkout inside the
// user's project folder that Reliant never sees.
func TestShellTool_RefusesHandMadeGitWorktrees(t *testing.T) {
	t.Parallel()
	refused := []string{
		`git worktree add ../reliant-agent-credentials -b feat/agent-credentials`,
		`git worktree add -b feat/x ../repo-x main`,
		`git worktree add --detach ../repo-x`,
		`git worktree add -f --lock --reason "agent" ../repo-x`,
		`git -C /Users/u/projects/p/reliant worktree add ../reliant-x -b x`,
		`git --git-dir=/Users/u/p/.git worktree add /Users/u/p-x`,
		`git -c core.hooksPath=/dev/null worktree add ~/wt/x`,
		`cd reliant && git fetch && git worktree add ../reliant-x origin/main`,
		`GIT_TRACE=1 git worktree add ../x`,
		"git worktree add \\\n  ../reliant-x \\\n  -b x",
		`git worktree add "$HOME/.reliant/worktrees/p/x"`,
		`git worktree add $WT -b x`,
		`git worktree add`,
	}
	for _, cmd := range refused {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			d, resp := runShellAgainst(t, "/Users/u/project", cmd)
			if len(d.ran) != 0 {
				t.Fatalf("command reached the daemon: %q", d.ran)
			}
			if !resp.IsError || !strings.Contains(resp.Content, "refused") {
				t.Fatalf("expected a refusal, got IsError=%v content=%q", resp.IsError, resp.Content)
			}
			if !strings.Contains(resp.Content, `"action": "create"`) {
				t.Fatalf("refusal does not name the worktree tool call to make instead: %q", resp.Content)
			}
		})
	}
}

// Throwaway checkouts in a temp dir, every other worktree subcommand, and text
// that merely mentions the command all run unchanged.
func TestShellTool_AllowsTempWorktreesAndOtherWorktreeCommands(t *testing.T) {
	t.Parallel()
	allowed := []string{
		`git worktree add /tmp/inspect-v1 v1.0.0`,
		`git worktree add --detach "${TMPDIR:-/tmp}/old" abc123`,
		`git worktree add "$TMPDIR/old" HEAD~3`,
		`git worktree add /var/folders/xy/T/old HEAD`,
		`git worktree add "$(mktemp -d)/old" HEAD`,
		`git worktree add -b scratch /private/tmp/scratch`,
		`git worktree list`,
		`git worktree remove /tmp/inspect-v1`,
		`git worktree prune`,
		`git worktree lock ../x`,
		`git -C reliant worktree list --porcelain`,
		`git log --oneline -- worktree add`,
		`echo "git worktree add ../x"`,
		`rg -n 'git worktree add' internal/`,
	}
	for _, cmd := range allowed {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			d, resp := runShellAgainst(t, "/Users/u/project", cmd)
			if resp.IsError {
				t.Fatalf("refused an allowed command: %q", resp.Content)
			}
			if len(d.ran) != 1 {
				t.Fatalf("command did not reach the daemon: %v", d.ran)
			}
		})
	}
}

func runShellWithCaps(t *testing.T, caps *Capabilities, command string) (*commandRecordingDaemon, ToolResponse) {
	t.Helper()
	d := &commandRecordingDaemon{}
	tc := &rctx.ToolContext{
		Context: WithCapabilities(context.Background(), caps),
		ChatID:  "chat-1",
		Project: &db.Project{ID: "p", Path: "/Users/u/project"},
		Daemon:  d,
	}
	resp, err := (&shellTool{}).Execute(tc, ShellParams{Command: command})
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	return d, resp
}

// The refusal always holds; its remedy names the worktree tool only when
// load_tool would grant it, and otherwise quotes load_tool's own reason.
func TestShellTool_WorktreeRefusalRemedyFollowsReachability(t *testing.T) {
	t.Parallel()
	const cmd = `git worktree add ../x -b x`
	declared := ResolveToolAccess(nil, []string{"shell"}, nil)
	cases := []struct {
		name      string
		in        CapabilityInputs
		reachable bool
	}{
		{"loadable all", CapabilityInputs{Access: ResolveToolAccess(nil, []string{"*"}, nil), Permission: PermissionMutating}, true},
		{"not declared loadable", CapabilityInputs{Access: declared, Permission: PermissionMutating}, false},
		{"no machine", CapabilityInputs{Access: ResolveToolAccess(nil, []string{"*"}, nil), Permission: PermissionMutating, NoMachine: true}, false},
		{"unattended, loadable all", CapabilityInputs{Access: ResolveToolAccess(nil, []string{"*"}, nil), Permission: PermissionMutating, Unattended: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			caps := ResolveCapabilities(tc.in)
			d, resp := runShellWithCaps(t, caps, cmd)
			if len(d.ran) != 0 || !resp.IsError || !strings.Contains(resp.Content, "refused") {
				t.Fatalf("expected a refusal that never reaches the daemon, got %q", resp.Content)
			}
			hasTool := strings.Contains(resp.Content, `load_tool(name="worktree")`)
			if hasTool != tc.reachable {
				t.Fatalf("remedy names the worktree tool = %v, want %v: %q", hasTool, tc.reachable, resp.Content)
			}
			if tc.reachable {
				return
			}
			if why := caps.LoadRefusal(ToolWorktree); !strings.Contains(resp.Content, why) {
				t.Fatalf("refusal does not quote load_tool's reason %q: %q", why, resp.Content)
			}
			for _, want := range []string{"not available in this step", "Reliant UI", "${TMPDIR:-/tmp}"} {
				if !strings.Contains(resp.Content, want) {
					t.Fatalf("missing %q: %q", want, resp.Content)
				}
			}
			if strings.Contains(resp.Content, "git switch") || strings.Contains(resp.Content, "checkout -b") {
				t.Fatalf("suggests switching branches in the shared checkout: %q", resp.Content)
			}
		})
	}
}
