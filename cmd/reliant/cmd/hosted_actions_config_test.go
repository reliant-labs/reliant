// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/controlplane/cptransport"
	"github.com/reliant-labs/reliant/internal/hosteduse"
)

// The worker's hosted HTTP executor builds from the control plane exactly as
// prod configures it: control-plane's deploy/kcl/lib/env.k gives every reliant
// role RELIANT_CONTROL_PLANE_URL=http://admin-server.<ns>.svc.cluster.local:8090
// plus INTERNAL_SERVICE_SECRET. v1.7.52's worker crash-looped on that shape
// ("hosted HTTP controller requires HTTPS"), so this loads the env the way
// `reliant server worker` does and builds what NewTemporalWorker builds.
func TestServerWorkerBuildsHostedActionsFromProdEnv(t *testing.T) {
	build := func(t *testing.T, cpURL string) error {
		t.Helper()
		isolateControlPlaneEnv(t)
		t.Setenv("RELIANT_CONTROL_PLANE_URL", cpURL)
		t.Setenv("INTERNAL_SERVICE_SECRET", "prod-shaped-secret")
		var opts workerOptions
		require.NoError(t, loadServerWorkerConfig(findServerCmd(t, "worker"), &opts))
		require.Equal(t, cpURL, opts.ControlPlane.URL)
		executor, err := app.HostedActions(&hosteduse.MockStore{}, opts.ControlPlane)
		if err == nil {
			require.NotNil(t, executor, "a hosted worker has a hosted action executor")
		}
		return err
	}

	for _, ns := range []string{"control-plane-prod", "control-plane-e2e", "control-plane-dev-k8s"} {
		t.Run(ns, func(t *testing.T) {
			require.NoError(t, build(t, "http://admin-server."+ns+".svc.cluster.local:8090"))
		})
	}
	t.Run("dev host loopback", func(t *testing.T) {
		require.NoError(t, build(t, "http://127.0.0.1:8090"))
	})
	t.Run("external http is refused", func(t *testing.T) {
		require.ErrorIs(t, build(t, "http://cp.reliant.example:8090"), cptransport.ErrInsecure)
	})
	t.Run("self-hosted has none", func(t *testing.T) {
		isolateControlPlaneEnv(t)
		var opts workerOptions
		require.NoError(t, loadServerWorkerConfig(findServerCmd(t, "worker"), &opts))
		executor, err := app.HostedActions(&hosteduse.MockStore{}, opts.ControlPlane)
		require.NoError(t, err)
		require.Nil(t, executor)
	})
}
