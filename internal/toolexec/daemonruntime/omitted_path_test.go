// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TestOmittedPathStaysInsideTheGrant covers the easiest thing a model can do
// wrong: leave an optional path argument out.
//
// Several handlers substitute a default when a path is absent — the daemon's
// working directory, or $HOME. The daemon is never chdir'd into a connector's
// grant, so those defaults point somewhere the connector was never given, and
// git_status/git_diff are in the DEFAULT read-only grant. Omission must resolve
// to the allowed root, not to whatever the daemon happens to be sitting in.
func TestOmittedPathStaysInsideTheGrant(t *testing.T) {
	// A real repo inside the grant, so a correctly-confined command succeeds
	// against it rather than failing for unrelated reasons.
	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(root, "inside.txt"), []byte("x"), 0o600))

	policy := func(tools ...string) *reliantv1.ConnectorPolicy {
		return &reliantv1.ConnectorPolicy{
			GrantId:       "grant-omitted",
			AllowedTools:  tools,
			PathRoot:      root,
			ExecMode:      "allowlist",
			ExecAllowlist: []string{"pwd"},
		}
	}

	t.Run("git_status with no path uses the grant root", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "omit-1",
			CommandType: "worktree.git_status",
			Payload:     mustPayload(t, map[string]any{"worktree_path": ""}),
			Policy:      policy("worktree.git_status"),
		})

		require.True(t, resp.GetSuccess(), "git_status failed: %s", resp.GetErrorMessage())

		// The untracked file proves it ran against the grant's repo rather
		// than the daemon's own working directory.
		require.Contains(t, string(resp.GetPayload()), "inside.txt",
			"git_status must run in the grant root when no path is supplied")
	})

	t.Run("exec.run with no working_dir uses the grant root", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "omit-2",
			CommandType: "exec.run",
			Payload:     mustPayload(t, map[string]any{"argv": []string{"pwd"}}),
			Policy:      policy("exec.run"),
		})

		require.True(t, resp.GetSuccess(), "pwd failed: %s", resp.GetErrorMessage())

		var result struct {
			Stdout string `json:"stdout"`
		}
		require.NoError(t, json.Unmarshal(resp.GetPayload(), &result))

		// Resolve both sides: on macOS the temp root is handed out under /var
		// while the process reports /private/var.
		resolvedRoot, err := filepath.EvalSymlinks(root)
		require.NoError(t, err)
		require.Contains(t, result.Stdout, resolvedRoot,
			"a command with no working_dir must run in the grant root, not the daemon's cwd")
	})
}
