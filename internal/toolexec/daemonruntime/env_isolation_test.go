// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// TestConnectorCannotReadDaemonSecretsFromEnv is the check for the most
// mundane exfiltration path there is: print your own environment.
//
// The daemon's environment holds the user's git credential (GIT_TOKEN, which
// the daemon uses to configure git credential-store at startup) and the
// deployment's internal URLs. Every command the daemon spawns used to inherit
// all of it, so any allowlisted program — `env`, a test that dumps its
// environment on failure, a build script — would hand it straight back to a
// third-party model through the tool result.
func TestConnectorCannotReadDaemonSecretsFromEnv(t *testing.T) {
	root := t.TempDir()

	// Stand in for what a cloud workspace pod actually injects.
	t.Setenv("GIT_TOKEN", "ghp_SUPERSECRETTOKEN")
	t.Setenv("RELIANT_GATEWAY_URL", "https://internal-gateway.example.com")
	t.Setenv("PATH", "/usr/bin:/bin")

	policy := &reliantv1.ConnectorPolicy{
		GrantId:       "grant-env",
		AllowedTools:  []string{"exec.run"},
		PathRoot:      root,
		ExecMode:      "allowlist",
		ExecAllowlist: []string{"env"},
	}

	readEnv := func(t *testing.T, policy *reliantv1.ConnectorPolicy) string {
		t.Helper()
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "env-1",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				"argv":        []string{"env"},
				"working_dir": root,
			}),
			Policy: policy,
		})
		require.True(t, resp.GetSuccess(), "env failed: %s", resp.GetErrorMessage())

		var result struct {
			Stdout string `json:"stdout"`
		}
		require.NoError(t, json.Unmarshal(resp.GetPayload(), &result))
		return result.Stdout
	}

	t.Run("secrets are not inherited by a confined caller", func(t *testing.T) {
		out := readEnv(t, policy)

		require.NotContains(t, out, "ghp_SUPERSECRETTOKEN",
			"a connector must not be able to read the user's git token from its own environment")
		require.NotContains(t, out, "internal-gateway.example.com",
			"a connector must not be able to read the deployment's internal URLs")

		// The process is still usable: without PATH nothing resolves.
		require.Contains(t, out, "PATH=", "a confined process still needs PATH")
	})

	t.Run("first-party callers are unaffected", func(t *testing.T) {
		d := newPolicyGateClient(t)
		resp := runCommand(t, d, &reliantv1.DaemonCommandRequest{
			RequestId:   "env-2",
			CommandType: "exec.run",
			Payload: mustPayload(t, map[string]any{
				"command":     "env",
				"working_dir": root,
			}),
			// No policy: the user's own agent, entitled to the user's own
			// credentials.
		})
		require.True(t, resp.GetSuccess(), "env failed: %s", resp.GetErrorMessage())

		var result struct {
			Stdout string `json:"stdout"`
		}
		require.NoError(t, json.Unmarshal(resp.GetPayload(), &result))
		require.Contains(t, result.Stdout, "ghp_SUPERSECRETTOKEN",
			"the first-party path must keep inheriting the daemon's environment")
	})
}
