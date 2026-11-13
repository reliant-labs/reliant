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
	"github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/pkg/config"
)

// TestProjectMovesGateReachesTheProjectHandler pins the rollout gate for
// moving a project to another org on the path production takes. dev and demo,
// as this repo renders them, ship it off. An env's KCL
// (config_gen.workloadEnvMap for the api root) projects
// RELIANT_ORGS_PROJECT_MOVES_ENABLED, `server api` loads its ApiServerConfig
// from it, and app.ProjectOrgRollout (what newProject hands the project
// handler) carries it. At the schema default it is "false" and
// MoveProjectToOrg is refused: no move starts before control-plane's half is
// in prod and in the rollback target. The handler tests inject OrgsConfig
// directly, so without this a flipped default or a dropped env-map entry
// would go unseen.
func TestProjectMovesGateReachesTheProjectHandler(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/org_project_moves_gate.k"
	const name = "RELIANT_ORGS_PROJECT_MOVES_ENABLED"
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
			assert.Equal(t, tc.wantOn, api.GetOrgs().GetProjectMovesEnabled(), "ApiServerConfig.Orgs as loaded")
			// What newProject hands the project handler, and the check
			// MoveProjectToOrg makes with it.
			assert.Equal(t, tc.wantOn, project.MovesAllowed(app.ProjectOrgRollout(api)), "moves allowed")
		})
	}
}

// Unset, as every env ships today, the API server's project handler gets the
// gate off. The fast half of the test above: no KCL, just the load.
func TestProjectMovesGateIsOffByDefault(t *testing.T) {
	isolateServerAPIConfigEnv(t)
	cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
	require.NoError(t, err)
	api, err := config.LoadApiServerConfig(cmd)
	require.NoError(t, err)
	assert.False(t, project.MovesAllowed(app.ProjectOrgRollout(api)))

	t.Setenv("RELIANT_ORGS_PROJECT_MOVES_ENABLED", "true")
	api, err = config.LoadApiServerConfig(cmd)
	require.NoError(t, err)
	assert.True(t, project.MovesAllowed(app.ProjectOrgRollout(api)))
}
