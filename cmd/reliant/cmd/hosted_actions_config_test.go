// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/hosteduse"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/pkg/hostedhttp"
)

// The worker's hosted HTTP executor builds from the control plane exactly as
// prod configures it: control-plane's deploy/kcl/lib/env.k gives every reliant
// role RELIANT_CONTROL_PLANE_URL=http://admin-server.<ns>.svc.cluster.local:8090
// plus INTERNAL_SERVICE_SECRET. v1.7.52's worker crash-looped on that shape
// ("hosted HTTP controller requires HTTPS"), so this loads the env the way
// `reliant server worker` does and builds what NewTemporalWorker builds: a
// working executor, not the disabled one.
func TestServerWorkerBuildsHostedActionsFromProdEnv(t *testing.T) {
	// build returns the executor and whether it is the disabled stand-in: a
	// disabled executor refuses with HostedDisabledError before anything else,
	// a real one gets as far as demanding a fixed Action activity.
	build := func(t *testing.T, cpURL string) (disabled bool, warned string) {
		t.Helper()
		isolateControlPlaneEnv(t)
		t.Setenv("RELIANT_CONTROL_PLANE_URL", cpURL)
		t.Setenv("INTERNAL_SERVICE_SECRET", "prod-shaped-secret")
		var opts workerOptions
		require.NoError(t, loadServerWorkerConfig(findServerCmd(t, "worker"), &opts))
		require.Equal(t, cpURL, opts.ControlPlane.URL)
		var logged bytes.Buffer
		executor := app.HostedActions(slog.New(slog.NewTextHandler(&logged, nil)), &hosteduse.MockStore{}, opts.ControlPlane)
		require.NotNil(t, executor, "a hosted worker has a hosted action executor")
		_, err := executor.ExecuteAction(context.Background(), "run", "node", "grant", hostedhttp.Request{URL: "https://example.com", Method: "POST"})
		require.Error(t, err, "outside an Action activity nothing executes")
		var off *handlers.HostedDisabledError
		return errors.As(err, &off), logged.String()
	}

	for _, ns := range []string{"control-plane-prod", "control-plane-e2e", "control-plane-dev-k8s"} {
		t.Run(ns, func(t *testing.T) {
			disabled, warned := build(t, "http://admin-server."+ns+".svc.cluster.local:8090")
			assert.False(t, disabled, "the in-cluster admin-server Service enables hosted actions")
			assert.Empty(t, warned)
		})
	}
	t.Run("dev host loopback", func(t *testing.T) {
		disabled, _ := build(t, "http://127.0.0.1:8090")
		assert.False(t, disabled)
	})
	t.Run("external http is refused", func(t *testing.T) {
		disabled, warned := build(t, "http://cp.reliant.example:8090")
		assert.True(t, disabled, "an operator-misconfigured external http URL never carries the bearer")
		assert.Contains(t, warned, "hosted HTTP actions are DISABLED")
	})
	t.Run("self-hosted has none", func(t *testing.T) {
		isolateControlPlaneEnv(t)
		var opts workerOptions
		require.NoError(t, loadServerWorkerConfig(findServerCmd(t, "worker"), &opts))
		assert.Nil(t, app.HostedActions(slog.Default(), &hosteduse.MockStore{}, opts.ControlPlane))
	})
}
