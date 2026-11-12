// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/pkg/config"
)

// TestOrgInstallationTriggersGateReachesTheTriggerHandler pins the rollout
// gate for installation-bound GitHub triggers on the path production takes:
// an env's KCL (config_gen.workloadEnvMap for the api root) projects
// RELIANT_ORGS_GITHUB_INSTALLATION_TRIGGERS_ENABLED, `server api` loads its
// ApiServerConfig from it, and app.TriggerOrgRollout (what newTrigger hands
// the trigger handler) carries it. At the schema default it is "false" and the
// handler's gate is off: no trigger can be bound, which is what keeps a
// previous release's replica (which ignores and erases a binding) safe. The
// handler tests inject OrgsConfig directly, so without this a flipped default
// or a dropped env-map entry would go unseen.
func TestOrgInstallationTriggersGateReachesTheTriggerHandler(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/org_installation_triggers_gate.k"
	const name = "RELIANT_ORGS_GITHUB_INSTALLATION_TRIGGERS_ENABLED"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector string
		wantEnv  string
		wantOn   bool
	}{
		{"gate_default", "false", false},
		{"gate_on", "true", true},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, project))
			got, ok := env[name]
			require.True(t, ok, "the api server's projected env has no %s: %v", name, env)
			require.Equal(t, tc.wantEnv, got)

			isolateServerAPIConfigEnv(t)
			t.Setenv(name, got.(string))
			cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
			require.NoError(t, err)
			api, err := config.LoadApiServerConfig(cmd)
			require.NoError(t, err)
			assert.Equal(t, tc.wantOn, app.TriggerOrgRollout(api).GetGithubInstallationTriggersEnabled())
		})
	}
}

// Unset, as every env ships today, the API server's trigger handler gets the
// gate off. The fast half of the test above: no KCL, just the load.
func TestOrgInstallationTriggersGateIsOffByDefault(t *testing.T) {
	isolateServerAPIConfigEnv(t)
	cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
	require.NoError(t, err)
	api, err := config.LoadApiServerConfig(cmd)
	require.NoError(t, err)
	assert.False(t, app.TriggerOrgRollout(api).GetGithubInstallationTriggersEnabled())

	t.Setenv("RELIANT_ORGS_GITHUB_INSTALLATION_TRIGGERS_ENABLED", "true")
	api, err = config.LoadApiServerConfig(cmd)
	require.NoError(t, err)
	assert.True(t, app.TriggerOrgRollout(api).GetGithubInstallationTriggersEnabled())
}
