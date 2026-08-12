// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
)

// startBackground starts a process under the given policy and returns its id.
func startBackground(t *testing.T, policy *reliantv1.ConnectorPolicy, root string, argv ...string) string {
	t.Helper()

	d := newPolicyGateClient(t)
	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "bg-start-" + time.Now().Format("150405.000000"),
		CommandType: "exec.bg_start",
		Payload: mustPayload(t, map[string]any{
			"argv":        argv,
			"working_dir": root,
		}),
		Policy: policy,
	})
	require.True(t, resp.GetSuccess(), "background start failed: %s", resp.GetErrorMessage())

	var out struct {
		ProcessID string `json:"process_id"`
	}
	require.NoError(t, json.Unmarshal(resp.GetPayload(), &out))
	require.NotEmpty(t, out.ProcessID)
	return out.ProcessID
}

func bgPolicy(grantID, root string) *reliantv1.ConnectorPolicy {
	return &reliantv1.ConnectorPolicy{
		GrantId:       grantID,
		AllowedTools:  []string{"exec.bg_start", "exec.bg_output", "exec.bg_kill"},
		PathRoot:      root,
		ExecMode:      "allowlist",
		ExecAllowlist: []string{"sleep", "echo"},
	}
}

// TestBackgroundProcessesAreGrantScoped is the check that made it safe to
// expose the background tools at all.
//
// The registry is process-global and keyed by an id alone, so without an
// ownership check a connector holding a guessed or enumerated id could read
// the output of — or kill — a process belonging to someone else. Build and
// test output routinely contains secrets.
func TestBackgroundProcessesAreGrantScoped(t *testing.T) {
	root := t.TempDir()

	victimID := startBackground(t, bgPolicy("grant-a", root), root, "sleep", "30")
	t.Cleanup(func() { _ = shell.GetBackgroundManager().KillProcess(victimID) })

	// A second connector, with its own grant, guesses the id.
	attacker := bgPolicy("grant-b", root)

	t.Run("cannot read another grant's output", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "bg-out-1",
			CommandType: "exec.bg_output",
			Payload:     mustPayload(t, map[string]any{"process_id": victimID}),
			Policy:      attacker,
		})
		require.False(t, resp.GetSuccess(), "a connector must not read another grant's process output")
		require.Contains(t, resp.GetErrorMessage(), "not found",
			"the refusal must not confirm the process exists")
	})

	t.Run("cannot kill another grant's process", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "bg-kill-1",
			CommandType: "exec.bg_kill",
			Payload:     mustPayload(t, map[string]any{"process_id": victimID}),
			Policy:      attacker,
		})
		require.False(t, resp.GetSuccess(), "a connector must not kill another grant's process")

		// Still running.
		proc, err := shell.GetBackgroundManager().GetProcess(victimID)
		require.NoError(t, err)
		require.Equal(t, "running", proc.Status)
	})

	t.Run("owner can read its own output", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "bg-out-2",
			CommandType: "exec.bg_output",
			Payload:     mustPayload(t, map[string]any{"process_id": victimID}),
			Policy:      bgPolicy("grant-a", root),
		})
		require.True(t, resp.GetSuccess(), "the owning grant must be able to read its own output: %s",
			resp.GetErrorMessage())
	})
}

// TestFirstPartyCanSeeAllBackgroundProcesses: the unconfined path must keep
// working exactly as it did, or the app's own process views break.
func TestFirstPartyCanSeeAllBackgroundProcesses(t *testing.T) {
	root := t.TempDir()

	id := startBackground(t, bgPolicy("grant-a", root), root, "sleep", "30")
	t.Cleanup(func() { _ = shell.GetBackgroundManager().KillProcess(id) })

	d := newPolicyGateClient(t)
	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "bg-out-3",
		CommandType: "exec.bg_output",
		Payload:     mustPayload(t, map[string]any{"process_id": id}),
		// No policy: the first-party path.
	})
	require.True(t, resp.GetSuccess(),
		"an unconfined caller must still see every process: %s", resp.GetErrorMessage())
}

// TestBackgroundStartRespectsExecAllowlist confirms bg_start is gated by the
// same exec rules as a synchronous run.
func TestBackgroundStartRespectsExecAllowlist(t *testing.T) {
	root := t.TempDir()

	d := newPolicyGateClient(t)
	resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
		RequestId:   "bg-start-denied",
		CommandType: "exec.bg_start",
		Payload: mustPayload(t, map[string]any{
			"argv":        []string{"curl", "evil.example.com"},
			"working_dir": root,
		}),
		Policy: bgPolicy("grant-a", root),
	})
	require.False(t, resp.GetSuccess(), "a background start must obey the exec allowlist")
	require.Contains(t, resp.GetErrorMessage(), "not in this connector's allowed commands")
}
