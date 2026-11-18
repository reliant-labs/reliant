// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/pkg/config"
)

// TestOrgUsageControlPlaneReachesTheOrgUsageHandler pins the path a hosted
// deployment takes to OrgUsageService's list prices and to control-plane's
// org-key read (org-provider-accounts P9). A hosted env's KCL
// (config_gen.workloadEnvMap for the api root, composed as control-plane's
// lib/reliant_process.k composes prod) projects RELIANT_CONTROL_PLANE_URL and
// INTERNAL_SERVICE_SECRET (a Secret ref, never a literal); `server api` loads
// its ServerProcessConfig from them; and app.OrgUsageListPrices (what
// newOrgUsage hands the handler) is non-nil only with both. The handler tests
// inject the price client directly, so without this a dropped env-map entry
// would ship usage without estimates and nobody would see why.
func TestOrgUsageControlPlaneReachesTheOrgUsageHandler(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/org_usage_control_plane.k"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	var env map[string]any
	require.NoError(t, kcleval.Select(context.Background(), fixture, "hosted_api", &env, project))

	url, ok := env["RELIANT_CONTROL_PLANE_URL"].(string)
	require.True(t, ok, "the api server's projected env has no RELIANT_CONTROL_PLANE_URL: %v", env)
	assert.Equal(t, "http://admin-server.control-plane-prod.svc.cluster.local:8090", url)
	secret, ok := env["INTERNAL_SERVICE_SECRET"].(map[string]any)
	require.True(t, ok, "INTERNAL_SERVICE_SECRET is not a Secret ref in the api server's env: %#v", env["INTERNAL_SERVICE_SECRET"])
	assert.Equal(t, "control-plane-internal", secret["name"])
	assert.Equal(t, "secret", secret["key"])

	testOrgUsageControlPlaneLoad(t, url)
}

// The fast half: `server api` loads what the env carries, and the org usage
// handler gets a price client only when both the URL and the secret are set.
func TestOrgUsageControlPlaneNeedsTheInternalSecret(t *testing.T) {
	testOrgUsageControlPlaneLoad(t, "http://admin-server.control-plane-prod.svc.cluster.local:8090")
}

func testOrgUsageControlPlaneLoad(t *testing.T, url string) {
	t.Helper()
	cmd, _, err := NewRootCmd().Find([]string{"server", "api"})
	require.NoError(t, err)

	isolateServerAPIConfigEnv(t)
	t.Setenv("RELIANT_CONTROL_PLANE_URL", url)
	t.Setenv("INTERNAL_SERVICE_SECRET", "s3cret")
	process, err := config.LoadServerProcessConfig(cmd)
	require.NoError(t, err)
	assert.Equal(t, url, process.GetControlPlane().GetUrl())
	assert.NotNil(t, app.OrgUsageListPrices(process.GetControlPlane()), "a hosted api server with the secret prices org usage")

	t.Setenv("INTERNAL_SERVICE_SECRET", "") // restored afterwards
	require.NoError(t, os.Unsetenv("INTERNAL_SERVICE_SECRET"))
	process, err = config.LoadServerProcessConfig(cmd)
	require.NoError(t, err)
	assert.Nil(t, app.OrgUsageListPrices(process.GetControlPlane()), "without the secret there is nothing to sign with: no estimates, never a typed nil")
}
