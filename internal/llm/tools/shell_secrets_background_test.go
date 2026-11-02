package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
)

// A command carrying a run step's secrets never backgrounds: the daemon masks
// the result it returns, but a background process's output is buffered,
// streamed and read back by shell_output unmasked. Neither form — a trailing &
// or run_in_background — starts anything.
func TestShellTool_SecretBearingCommandRefusesTheBackground(t *testing.T) {
	for name, p := range map[string]ShellParams{
		"trailing &":        {Command: `echo "$TRADER_KEY" &`},
		"run_in_background": {Command: `echo "$TRADER_KEY"`, RunInBackground: true},
	} {
		t.Run(name, func(t *testing.T) {
			d := &requestRecordingDaemon{}
			tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1", Project: &rctx.Project{ID: "p", Checkout: "/work/p"},
				Daemon: d, RunStep: true, MasksSecrets: true}
			resp, err := (&shellTool{}).Execute(tc, p)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !resp.IsError || !strings.Contains(resp.Content, "cannot run in the background") {
				t.Fatalf("not refused: %+v", resp)
			}
			if len(d.reqs) != 0 {
				t.Fatalf("a secret-bearing command reached the daemon: %+v", d.reqs)
			}
		})
	}
}

// Without secrets a trailing & still backgrounds, as before.
func TestShellTool_TrailingAmpersandBackgroundsWithoutSecrets(t *testing.T) {
	d, resp := runShellParams(t, ShellParams{Command: "sleep 5 &"})
	if resp.IsError || len(d.reqs) != 1 {
		t.Fatalf("resp=%+v reqs=%d", resp, len(d.reqs))
	}
}
