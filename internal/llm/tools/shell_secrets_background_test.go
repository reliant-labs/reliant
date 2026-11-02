package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
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
				Daemon: d, RunStep: true, SecretMask: []string{"trader-key-value"}}
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

// outputDaemon answers every command with fixed stdout and stderr.
type outputDaemon struct {
	requestRecordingDaemon
	stdout, stderr string
}

func (d *outputDaemon) RunCommand(_ context.Context, req *daemon.RunCommandRequest) (*daemon.CommandResult, error) {
	d.reqs = append(d.reqs, *req)
	return &daemon.CommandResult{Stdout: d.stdout, Stderr: d.stderr}, nil
}

// A secret echoed across the head+tail cut is masked before the cut. Masking
// only the returned result (the daemon's maskToolResult) saw the two
// fragments truncation left, neither of which matches the value, and they
// reached the result outbox and workflow history (reliant #2037 review).
func TestShellTool_SecretAcrossTheTruncationCutIsMaskedFirst(t *testing.T) {
	const secret = "sk-live-0123456789abcdefghijklmnopqrstuvwxyz"
	const limit = 200
	for name, place := range map[string]func(pad string) string{
		// The head keeps limit/2 bytes: the value starts just before the cut.
		"head cut": func(pad string) string { return pad[:limit/2-10] + secret + pad },
		// The tail keeps limit/2 bytes: the value ends just after its start.
		"tail cut": func(pad string) string { return pad + secret + pad[:limit/2-10] },
	} {
		t.Run(name, func(t *testing.T) {
			pad := strings.Repeat("x", 4*limit)
			out := place(pad)
			d := &outputDaemon{stdout: out, stderr: out}
			tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1", Project: &rctx.Project{ID: "p", Checkout: "/work/p"},
				Daemon: d, RunStep: true, SecretMask: []string{secret}}
			resp, err := (&shellTool{}).Execute(tc, ShellParams{Command: `echo "$TRADER_KEY"`, MaxOutput: limit})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			for _, frag := range []string{secret[:8], secret[8:16], secret[len(secret)-8:]} {
				if strings.Contains(resp.Content, frag) {
					t.Fatalf("a fragment %q of the secret survived the truncation: %s", frag, resp.Content)
				}
			}
			if !strings.Contains(resp.Content, "truncated") {
				t.Fatalf("the output must have been truncated for this test to mean anything: %s", resp.Content)
			}
		})
	}
	// Unmasked, the same output does leave fragments: the test above proves the
	// masking, not a quirk of where the cut falls.
	pad := strings.Repeat("x", 4*limit)
	d := &outputDaemon{stdout: pad[:limit/2-10] + secret + pad}
	tc := &rctx.ToolContext{Context: context.Background(), ChatID: "chat-1", Project: &rctx.Project{ID: "p", Checkout: "/work/p"}, Daemon: d}
	resp, err := (&shellTool{}).Execute(tc, ShellParams{Command: "echo", MaxOutput: limit})
	if err != nil || !strings.Contains(resp.Content, secret[:8]) {
		t.Fatalf("without a mask the head fragment is expected in the output (err %v): %s", err, resp.Content)
	}
}

// Without secrets a trailing & still backgrounds, as before.
func TestShellTool_TrailingAmpersandBackgroundsWithoutSecrets(t *testing.T) {
	d, resp := runShellParams(t, ShellParams{Command: "sleep 5 &"})
	if resp.IsError || len(d.reqs) != 1 {
		t.Fatalf("resp=%+v reqs=%d", resp, len(d.reqs))
	}
}
