// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// newPolicyGateClient returns a daemon client wired with buffered channels, so
// handleDaemonCommand can run inline and its response be read back.
func newPolicyGateClient(t *testing.T) *daemonClient {
	t.Helper()
	d := newTestDaemonClient("daemon-policy", "user-policy")
	d.sendCh = make(chan *reliantv1.DaemonMessage, 4)
	d.sessionDone = make(chan struct{})
	return d
}

// runCommand dispatches req and returns the response the daemon sent back.
func runCommand(t *testing.T, d *daemonClient, req *reliantv1.DaemonCommandRequest) *reliantv1.DaemonCommandResponse {
	t.Helper()
	d.handleDaemonCommand(req)

	select {
	case msg := <-d.sendCh:
		resp := msg.GetDaemonCommandResponse()
		require.NotNil(t, resp, "expected a daemon command response")
		return resp
	case <-time.After(5 * time.Second):
		t.Fatal("daemon sent no response")
		return nil
	}
}

func mustPayload(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// TestPolicyGate_FirstPartyUnaffected is the regression guard for existing
// behavior: a request with no policy must execute exactly as it did before the
// gate existed.
func TestPolicyGate_FirstPartyUnaffected(t *testing.T) {
	d := newPolicyGateClient(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "hello.txt")
	require.NoError(t, os.WriteFile(target, []byte("hi"), 0o600))

	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "req-1",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": target}),
		// Policy intentionally unset — the first-party path.
	})

	require.True(t, resp.GetSuccess(), "unrestricted request failed: %s", resp.GetErrorMessage())
}

// TestPolicyGate_DeniesUngrantedCommand proves the gate refuses before the
// handler runs.
func TestPolicyGate_DeniesUngrantedCommand(t *testing.T) {
	d := newPolicyGateClient(t)
	dir := t.TempDir()

	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "req-2",
		CommandType: "fs.delete",
		Payload:     mustPayload(t, map[string]string{"path": filepath.Join(dir, "x.txt")}),
		Policy: &reliantv1.ConnectorPolicy{
			GrantId:      "grant-1",
			AllowedTools: []string{"fs.read_file"},
			PathRoot:     dir,
		},
	})

	require.False(t, resp.GetSuccess())
	require.Contains(t, resp.GetErrorMessage(), "not in this connector's allowed tools")
}

// TestPolicyGate_ConfinesPaths proves a granted command still cannot reach
// outside its root — the case that matters most for a connector.
func TestPolicyGate_ConfinesPaths(t *testing.T) {
	d := newPolicyGateClient(t)

	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("sensitive"), 0o600))

	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "req-3",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": secret}),
		Policy: &reliantv1.ConnectorPolicy{
			GrantId:      "grant-1",
			AllowedTools: []string{"fs.read_file"},
			PathRoot:     root,
		},
	})

	require.False(t, resp.GetSuccess())
	require.Contains(t, resp.GetErrorMessage(), "outside this connector's allowed directory")

	// The denial must not leak the file's contents through the payload.
	require.NotContains(t, string(resp.GetPayload()), "sensitive")
}

// TestPolicyGate_AllowsGrantedPathAndCommand confirms the gate is not simply
// refusing everything.
func TestPolicyGate_AllowsGrantedPathAndCommand(t *testing.T) {
	d := newPolicyGateClient(t)

	root := t.TempDir()
	target := filepath.Join(root, "readme.md")
	require.NoError(t, os.WriteFile(target, []byte("# hello"), 0o600))

	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "req-4",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": target}),
		Policy: &reliantv1.ConnectorPolicy{
			GrantId:      "grant-1",
			AllowedTools: []string{"fs.read_file"},
			PathRoot:     root,
		},
	})

	require.True(t, resp.GetSuccess(), "granted request failed: %s", resp.GetErrorMessage())
	require.Contains(t, string(resp.GetPayload()), "hello")
}

// TestPolicyGate_ExecAllowlist checks shell confinement end to end.
func TestPolicyGate_ExecAllowlist(t *testing.T) {
	root := t.TempDir()

	basePolicy := func() *reliantv1.ConnectorPolicy {
		return &reliantv1.ConnectorPolicy{
			GrantId:       "grant-exec",
			AllowedTools:  []string{"exec.run"},
			PathRoot:      root,
			ExecMode:      "allowlist",
			ExecAllowlist: []string{"echo"},
		}
	}

	t.Run("allowlisted command runs", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "req-5",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				"argv":        []string{"echo", "policy-ok"},
				"working_dir": root,
			}),
			Policy: basePolicy(),
		})
		require.True(t, resp.GetSuccess(), "allowlisted command failed: %s", resp.GetErrorMessage())
		require.Contains(t, string(resp.GetPayload()), "policy-ok")
	})

	t.Run("unlisted program is refused", func(t *testing.T) {
		d := newPolicyGateClient(t)
		marker := filepath.Join(root, "should-not-exist.txt")
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "req-6",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				// The classic escape: hand a shell the whole string. With an
				// allowlist there is no shell to hand it to, and "sh" is not
				// a permitted program.
				"argv":        []string{"sh", "-c", "echo hi > " + marker},
				"working_dir": root,
			}),
			Policy: basePolicy(),
		})

		require.False(t, resp.GetSuccess())
		require.Contains(t, resp.GetErrorMessage(), "not in this connector's allowed commands")

		// The refusal must have happened before execution, not after.
		_, err := os.Stat(marker)
		require.True(t, os.IsNotExist(err), "refused command still wrote to disk")
	})

	t.Run("exec denied when not granted", func(t *testing.T) {
		d := newPolicyGateClient(t)
		p := basePolicy()
		p.ExecMode = ""
		p.ExecAllowlist = nil

		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "req-7",
			CommandType: "exec.run",
			Payload:     mustPayload(t, map[string]any{"argv": []string{"echo", "hi"}, "working_dir": root}),
			Policy:      p,
		})
		require.False(t, resp.GetSuccess())
		require.Contains(t, resp.GetErrorMessage(), "shell execution is not granted")
	})
}

// TestPolicyGate_ArgvBypassesTheShell is the end-to-end proof that an exec
// allowlist is a real boundary rather than a string check racing an
// interpreter.
//
// Each case sends shell syntax through argv and asserts it was NOT
// interpreted: the metacharacters arrive as literal arguments to the program,
// so nothing chains, nothing expands, and nothing is written.
func TestPolicyGate_ArgvBypassesTheShell(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "planted.txt")

	policy := func() *reliantv1.ConnectorPolicy {
		return &reliantv1.ConnectorPolicy{
			GrantId:       "grant-argv",
			AllowedTools:  []string{"exec.run"},
			PathRoot:      root,
			ExecMode:      "allowlist",
			ExecAllowlist: []string{"echo"},
		}
	}

	t.Run("metacharacters are literal arguments", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "argv-1",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				// Under `bash -c` this would redirect into the file. Through
				// argv it is text that echo prints.
				"argv":        []string{"echo", "hello > " + marker},
				"working_dir": root,
			}),
			Policy: policy(),
		})

		require.True(t, resp.GetSuccess(), "argv command failed: %s", resp.GetErrorMessage())

		// Decode rather than substring-matching the raw JSON: the response
		// escapes '>' as \u003e, which would make a naive match fail against
		// output that is in fact correct.
		var result struct {
			Stdout string `json:"stdout"`
		}
		require.NoError(t, json.Unmarshal(resp.GetPayload(), &result))
		require.Contains(t, result.Stdout, "hello > ",
			"the redirection should have been printed as text, not performed")

		_, err := os.Stat(marker)
		require.True(t, os.IsNotExist(err), "argv execution must not have invoked a shell")
	})

	t.Run("command substitution is not evaluated", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "argv-2",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				"argv":        []string{"echo", "$(whoami)"},
				"working_dir": root,
			}),
			Policy: policy(),
		})

		require.True(t, resp.GetSuccess(), "argv command failed: %s", resp.GetErrorMessage())
		require.Contains(t, string(resp.GetPayload()), "$(whoami)",
			"command substitution must survive as literal text")
	})

	t.Run("a shell string is refused under an allowlist", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "argv-3",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				"command":     "echo hi",
				"working_dir": root,
			}),
			Policy: policy(),
		})

		require.False(t, resp.GetSuccess())
		require.Contains(t, resp.GetErrorMessage(), "separately")
	})

	t.Run("planted binary on PATH is refused", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "argv-4",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				"argv":        []string{"echo", "hi"},
				"working_dir": root,
				"env":         map[string]string{"PATH": "/tmp/planted"},
			}),
			Policy: policy(),
		})

		require.False(t, resp.GetSuccess(), "overriding PATH must be refused")
		require.Contains(t, resp.GetErrorMessage(), "PATH")
	})
}

// TestPolicyGate_HandlerReresolvesPaths proves the handler itself re-checks,
// not only the dispatch gate.
//
// This matters because the dispatch check inspects argument NAMES and so
// cannot cover every payload shape, and because a path that was in bounds when
// checked can be swapped for an escaping symlink before it is opened. The
// handler resolving through os.Root immediately before use closes both.
func TestPolicyGate_HandlerReresolvesPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on Windows")
	}

	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("sensitive"), 0o600))

	// A directory inside the root that will be swapped for a symlink out.
	victim := filepath.Join(root, "subdir")
	require.NoError(t, os.Mkdir(victim, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(victim, "secret.txt"), []byte("harmless"), 0o600))

	policy := &reliantv1.ConnectorPolicy{
		GrantId:      "grant-toctou",
		AllowedTools: []string{"fs.read_file"},
		PathRoot:     root,
	}
	target := filepath.Join(victim, "secret.txt")

	// In bounds to begin with.
	d := newPolicyGateClient(t)
	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "toctou-1",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": target}),
		Policy:      policy,
	})
	require.True(t, resp.GetSuccess(), "in-bounds read failed: %s", resp.GetErrorMessage())
	require.Contains(t, string(resp.GetPayload()), "harmless")

	// Swap the directory for a symlink pointing outside the root.
	require.NoError(t, os.RemoveAll(victim))
	require.NoError(t, os.Symlink(outside, victim))

	// The identical request must now be refused, and must not leak the file.
	d2 := newPolicyGateClient(t)
	resp2 := runCommand(t, d2, &reliantv1.DaemonCommandRequest{
		RequestId:   "toctou-2",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": target}),
		Policy:      policy,
	})
	require.False(t, resp2.GetSuccess(), "a path swapped for an escaping symlink must be refused")
	require.NotContains(t, string(resp2.GetPayload()), "sensitive")
}

// TestPolicyGate_ExpiredGrant confirms expiry is enforced at dispatch.
func TestPolicyGate_ExpiredGrant(t *testing.T) {
	d := newPolicyGateClient(t)
	root := t.TempDir()

	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "req-8",
		CommandType: "fs.read_file",
		Payload:     mustPayload(t, map[string]string{"path": filepath.Join(root, "a.txt")}),
		Policy: &reliantv1.ConnectorPolicy{
			GrantId:      "grant-expired",
			AllowedTools: []string{"fs.read_file"},
			PathRoot:     root,
			ExpiresAt:    time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		},
	})

	require.False(t, resp.GetSuccess())
	require.Contains(t, resp.GetErrorMessage(), "expired")
}
