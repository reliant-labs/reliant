// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// commandRecordingDaemon stands in for the user's machine. Only RunCommand is
// reachable from a foreground shell call; every command that gets this far is
// recorded instead of run.
type commandRecordingDaemon struct {
	daemon.Client
	ran []string
}

func (d *commandRecordingDaemon) RunCommand(_ context.Context, req *daemon.RunCommandRequest) (*daemon.CommandResult, error) {
	d.ran = append(d.ran, req.Command)
	return &daemon.CommandResult{ExitCode: 0}, nil
}

func runShellAgainst(t *testing.T, workingDir, command string) (*commandRecordingDaemon, ToolResponse) {
	t.Helper()
	d := &commandRecordingDaemon{}
	tc := &rctx.ToolContext{
		Context: context.Background(),
		ChatID:  "chat-1",
		Project: &db.Project{ID: "p", Path: workingDir},
		Daemon:  d,
	}
	resp, err := (&shellTool{}).Execute(tc, ShellParams{Command: command})
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	return d, resp
}

// The backstop for the parallel-compete P0: whatever renders a command, a
// sync-with-delete or a recursive remove aimed at the filesystem root, a system
// directory, or a home directory never reaches the machine.
func TestShellTool_RefusesDeletingCommandsAimedAtRoots(t *testing.T) {
	t.Parallel()
	refused := []string{
		// The command that ran on 2026-10-06, verbatim in shape.
		"rsync -av --delete --exclude='.git' --exclude='node_modules' --exclude='.dev-ports.sh' --exclude='data' --exclude='.env.local' \\\n" +
			"  \"/Users/u/.reliant/worktrees/p/compete-impl-2-abc/\" \\\n  \"/\"",
		`rsync -a --delete src/ /`,
		`rsync -a --delete-after src/ //`,
		`rsync -a --del src/ "$HOME/"`,
		`rsync -a --delete src/ ~`,
		`rsync -a --delete src/ /Users/u`,
		`rsync -a --delete src/ /home/u/`,
		`rsync -a --delete src/ /usr`,
		`cd /tmp && rsync -a --delete x/ /`,
		`rm -rf /`,
		`rm -rf "/"`,
		`rm -fr ~/`,
		`rm -r -f $HOME`,
		`rm --recursive --force /Users/u`,
		`sudo rm -rf /`,
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
		})
	}
}

// Narrow by design: the same tools aimed at anything below those roots run.
func TestShellTool_AllowsDeletingCommandsInsideAProject(t *testing.T) {
	t.Parallel()
	allowed := []string{
		`rsync -a --delete build/ /Users/u/project/dist/`,
		`rsync -a --delete src/ dest/`,
		`rsync -a src/ /`, // no --delete: rsync cannot remove anything
		`rm -rf node_modules`,
		`rm -rf /Users/u/project/tmp`,
		`rm -f /`, // not recursive; fails on its own
		`echo "rm -rf /"`,
	}
	for _, cmd := range allowed {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			d, resp := runShellAgainst(t, "/Users/u/project", cmd)
			if resp.IsError {
				t.Fatalf("refused a scoped command: %q", resp.Content)
			}
			if len(d.ran) != 1 {
				t.Fatalf("command did not reach the daemon: %v", d.ran)
			}
		})
	}
}

// A project rooted at "/" is a broken context, not a project: every relative
// path in a command then means the whole machine.
func TestShellTool_RefusesToRunWithRootAsTheProjectDirectory(t *testing.T) {
	t.Parallel()
	d, resp := runShellAgainst(t, "/", "ls")
	if len(d.ran) != 0 || !resp.IsError {
		t.Fatalf("a command ran with / as its working directory: ran=%v resp=%q", d.ran, resp.Content)
	}
}
