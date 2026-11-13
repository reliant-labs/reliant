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

// installedIntegrationsSwitch boots role's ServerProcessConfig from the
// environment as it stands, installs it the way the process does
// (app.NewOrgPolicyResolver, built at boot by every server role), and returns
// the platform switch the integrations enforcement points then read.
func installedIntegrationsSwitch(t *testing.T, role string) orgpolicy.Enforcement {
	t.Helper()
	cmd, _, err := NewRootCmd().Find([]string{"server", role})
	require.NoError(t, err)
	server, err := config.LoadServerProcessConfig(cmd)
	require.NoError(t, err)
	prev := orgpolicy.EnforcementOf(orgpolicy.PointIntegrations)
	t.Cleanup(func() {
		orgpolicy.SetProcessEnforcement(orgpolicy.PointIntegrations, prev)
		orgpolicy.SetProcessResolver(nil)
	})
	_, err = app.NewOrgPolicyResolver(app.OrgPolicyResolverDeps{Enforcement: server.GetOrgPolicyEnforcement()})
	require.NoError(t, err)
	return orgpolicy.EnforcementOf(orgpolicy.PointIntegrations)
}

// TestOrgPolicyEnforcementSwitchReachesTheProcess pins the integrations
// switch on the path production takes: an env's KCL (config_gen.workloadEnvMap
// for the api and worker roots) projects
// RELIANT_ORGS_POLICY_ENFORCEMENT_INTEGRATIONS, the role loads its
// ServerProcessConfig from it, and app.NewOrgPolicyResolver installs it as the
// switch every integrations enforcement point reads. At the schema default,
// and in this repo's dev and demo envs, it is audit (count, never refuse);
// flipped in KCL, it is enforce.
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
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, project))
			got, ok := env[orgPolicyIntegrationsEnv]
			require.True(t, ok, "the %s role's projected env has no %s: %v", tc.role, orgPolicyIntegrationsEnv, env)
			require.Equal(t, string(tc.want), got)

			isolateServerAPIConfigEnv(t)
			t.Setenv(orgPolicyIntegrationsEnv, got.(string))
			require.Equal(t, tc.want, installedIntegrationsSwitch(t, tc.role))
		})
	}
}

// The fast half: with the variable unset, as every env ships, each role
// installs audit; set, it installs what it names. No KCL, just the load and
// the real wiring.
func TestOrgPolicyEnforcementSwitchIsAuditByDefault(t *testing.T) {
	for _, role := range []string{"api", "worker", "gateway"} {
		t.Run(role, func(t *testing.T) {
			isolateServerAPIConfigEnv(t)
			t.Setenv(orgPolicyIntegrationsEnv, "")
			require.Equal(t, orgpolicy.EnforcementAudit, installedIntegrationsSwitch(t, role))

			t.Setenv(orgPolicyIntegrationsEnv, "enforce")
			require.Equal(t, orgpolicy.EnforcementEnforce, installedIntegrationsSwitch(t, role))

			t.Setenv(orgPolicyIntegrationsEnv, "off")
			require.Equal(t, orgpolicy.EnforcementOff, installedIntegrationsSwitch(t, role))
		})
	}
}
