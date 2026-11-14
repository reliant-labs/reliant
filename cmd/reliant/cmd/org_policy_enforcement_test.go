// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/pkg/config"
)

const orgPolicyIntegrationsEnv = "RELIANT_ORGS_POLICY_ENFORCEMENT_INTEGRATIONS"

// orgPolicySwitchEnvs are the platform switch of every reliant enforcement
// point, by the variable a deployment sets it with.
var orgPolicySwitchEnvs = map[orgpolicy.Point]string{
	orgpolicy.PointIntegrations: orgPolicyIntegrationsEnv,
	orgpolicy.PointApprovals:    "RELIANT_ORGS_POLICY_ENFORCEMENT_APPROVALS",
	orgpolicy.PointMachines:     "RELIANT_ORGS_POLICY_ENFORCEMENT_MACHINES",
	orgpolicy.PointMCP:          "RELIANT_ORGS_POLICY_ENFORCEMENT_MCP",
	orgpolicy.PointProviders:    "RELIANT_ORGS_POLICY_ENFORCEMENT_PROVIDERS",
}

// installedSwitches boots role's ServerProcessConfig from the environment as
// it stands, installs it the way the process does (app.NewOrgPolicyResolver,
// built at boot by every server role), and returns the platform switch each
// enforcement point then reads.
func installedSwitches(t *testing.T, role string) map[orgpolicy.Point]orgpolicy.Enforcement {
	t.Helper()
	cmd, _, err := NewRootCmd().Find([]string{"server", role})
	require.NoError(t, err)
	server, err := config.LoadServerProcessConfig(cmd)
	require.NoError(t, err)
	prev := map[orgpolicy.Point]orgpolicy.Enforcement{}
	for point := range orgPolicySwitchEnvs {
		prev[point] = orgpolicy.EnforcementOf(point)
	}
	t.Cleanup(func() {
		for point, e := range prev {
			orgpolicy.SetProcessEnforcement(point, e)
		}
		orgpolicy.SetProcessResolver(nil)
	})
	_, err = app.NewOrgPolicyResolver(app.OrgPolicyResolverDeps{Enforcement: server.GetOrgPolicyEnforcement()})
	require.NoError(t, err)
	got := map[orgpolicy.Point]orgpolicy.Enforcement{}
	for point := range orgPolicySwitchEnvs {
		got[point] = orgpolicy.EnforcementOf(point)
	}
	return got
}

// every is want for every enforcement point.
func every(want orgpolicy.Enforcement) map[orgpolicy.Point]orgpolicy.Enforcement {
	out := map[orgpolicy.Point]orgpolicy.Enforcement{}
	for point := range orgPolicySwitchEnvs {
		out[point] = want
	}
	return out
}

// TestOrgPolicyEnforcementSwitchReachesTheProcess pins every enforcement
// point's switch on the path production takes: an env's KCL
// (config_gen.workloadEnvMap for the api and worker roots) projects
// RELIANT_ORGS_POLICY_ENFORCEMENT_<POINT>, the role loads its
// ServerProcessConfig from it, and app.NewOrgPolicyResolver installs it as the
// switch the point's enforcement reads. At the schema default, and in this
// repo's dev and demo envs, each is audit (count, never refuse or change);
// flipped in KCL, it is enforce or off.
func TestOrgPolicyEnforcementSwitchReachesTheProcess(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/org_policy_enforcement.k"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	for _, tc := range []struct {
		selector, role string
		want           orgpolicy.Enforcement
	}{
		{"api_default", "api", orgpolicy.EnforcementAudit},
		{"worker_default", "worker", orgpolicy.EnforcementAudit},
		{"dev_api", "api", orgpolicy.EnforcementAudit},
		{"dev_worker", "worker", orgpolicy.EnforcementAudit},
		{"demo_api", "api", orgpolicy.EnforcementAudit},
		{"demo_worker", "worker", orgpolicy.EnforcementAudit},
		{"api_enforce", "api", orgpolicy.EnforcementEnforce},
		{"worker_enforce", "worker", orgpolicy.EnforcementEnforce},
		{"api_off", "api", orgpolicy.EnforcementOff},
		{"worker_off", "worker", orgpolicy.EnforcementOff},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, project))
			isolateServerAPIConfigEnv(t)
			for point, name := range orgPolicySwitchEnvs {
				got, ok := env[name]
				require.True(t, ok, "the %s role's projected env has no %s: %v", tc.role, name, env)
				require.Equal(t, string(tc.want), got, "%s", point)
				t.Setenv(name, got.(string))
			}
			require.Equal(t, every(tc.want), installedSwitches(t, tc.role))
		})
	}
}

// The fast half: with the variables unset, as every env ships, each role
// installs audit for every point; set, it installs what they name. No KCL,
// just the load and the real wiring.
func TestOrgPolicyEnforcementSwitchIsAuditByDefault(t *testing.T) {
	for _, role := range []string{"api", "worker", "gateway"} {
		t.Run(role, func(t *testing.T) {
			isolateServerAPIConfigEnv(t)
			for _, want := range []orgpolicy.Enforcement{orgpolicy.EnforcementAudit, orgpolicy.EnforcementEnforce, orgpolicy.EnforcementOff} {
				for _, name := range orgPolicySwitchEnvs {
					value := string(want)
					if want == orgpolicy.EnforcementAudit {
						value = "" // unset: the schema default
					}
					t.Setenv(name, value)
				}
				require.Equal(t, every(want), installedSwitches(t, role))
			}
		})
	}
}
