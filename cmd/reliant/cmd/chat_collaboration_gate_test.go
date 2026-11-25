// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/handlers/sharing"
	"github.com/reliant-labs/reliant/pkg/config"
)

// TestChatCollaborationGateReachesTheSharingHandler pins the rollout gate for
// granting people access to a chat (control-plane
// docs/design/chat-collaboration.md §12) on the path production takes. dev
// and demo, as this repo renders them, ship it off. An env's KCL
// (config_gen.workloadEnvMap for the api root) projects
// RELIANT_CHAT_COLLABORATION_ENABLED, `server api` loads its ApiServerConfig
// from it, and newSharing hands its chat_collaboration block to the sharing
// handler. At the schema default it is "false" and SetChatMember is refused:
// no grant exists before every replica and worker reads them. The handler
// tests inject the block directly, so without this a flipped default or a
// dropped env-map entry would go unseen.
func TestChatCollaborationGateReachesTheSharingHandler(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge env kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/chat_collaboration_gate.k"
	const name = "RELIANT_CHAT_COLLABORATION_ENABLED"
	kcl := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector string
		wantEnv  string
		wantOn   bool
	}{
		{"gate_default", "false", false},
		{"dev_api", "false", false},
		{"demo_api", "false", false},
		{"gate_on", "true", true},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, kcl))
			got, ok := env[name]
			require.True(t, ok, "the api server's projected env has no %s: %v", name, env)
			require.Equal(t, tc.wantEnv, got)

			isolateServerAPIConfigEnv(t)
			t.Setenv(name, got.(string))
			cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
			require.NoError(t, err)
			api, err := config.LoadApiServerConfig(cmd)
			require.NoError(t, err)
			assert.Equal(t, tc.wantOn, sharing.GrantsAllowed(api.GetChatCollaboration()), "grants allowed")
		})
	}
}

// Unset, as every env ships today, the API server's sharing handler gets the
// gate off. The fast half of the test above: no KCL, just the load.
func TestChatCollaborationGateIsOffByDefault(t *testing.T) {
	isolateServerAPIConfigEnv(t)
	cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
	require.NoError(t, err)
	api, err := config.LoadApiServerConfig(cmd)
	require.NoError(t, err)
	assert.False(t, sharing.GrantsAllowed(api.GetChatCollaboration()))

	t.Setenv("RELIANT_CHAT_COLLABORATION_ENABLED", "true")
	api, err = config.LoadApiServerConfig(cmd)
	require.NoError(t, err)
	assert.True(t, sharing.GrantsAllowed(api.GetChatCollaboration()))
}
