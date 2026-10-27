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
	"github.com/reliant-labs/reliant/pkg/config"
	"github.com/reliant-labs/reliant/pkg/hostedhttp"
)

// The hosted-action executor reads its own worker-only URL, not the general
// plaintext control-plane API URL. Exercise the generated worker config loader
// and the same factory the actual worker uses.
func TestServerWorkerBuildsHostedActionsFromProdEnv(t *testing.T) {
	// build returns the executor and whether it is the disabled stand-in: a
	// disabled executor refuses with HostedDisabledError before anything else,
	// a real one gets as far as demanding a fixed Action activity.
	build := func(t *testing.T, cpURL string) (disabled bool, warned string) {
		t.Helper()
		isolateControlPlaneEnv(t)
		t.Setenv("RELIANT_CONTROL_PLANE_URL", cpURL)
		t.Setenv("INTERNAL_SERVICE_SECRET", "prod-shaped-secret")
		t.Setenv("RELIANT_WORKER_HOSTED_ACTIONS_URL", cpURL)
		var opts workerOptions
		require.NoError(t, loadServerWorkerConfig(findServerCmd(t, "worker"), &opts))
		require.Equal(t, cpURL, opts.ControlPlane.URL)
		var logged bytes.Buffer
		worker, err := config.LoadWorkerConfig(findServerCmd(t, "worker"))
		require.NoError(t, err)
		process, err := config.LoadServerProcessConfig(findServerCmd(t, "worker"))
		require.NoError(t, err)
		executor := app.HostedActions(slog.New(slog.NewTextHandler(&logged, nil)), &hosteduse.MockStore{}, process.GetControlPlane(), worker)
		require.NotNil(t, executor, "a hosted worker has a hosted action executor")
		_, err = executor.ExecuteAction(context.Background(), "run", "node", "grant", hostedhttp.Request{URL: "https://example.com", Method: "POST"})
		require.Error(t, err, "outside an Action activity nothing executes")
		var off *handlers.HostedDisabledError
		return errors.As(err, &off), logged.String()
	}

	for _, ns := range []string{"control-plane-prod", "control-plane-e2e", "control-plane-dev-k8s"} {
		t.Run(ns, func(t *testing.T) {
			disabled, warned := build(t, "https://workspace-controller."+ns+".svc.cluster.local:9443")
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
		worker, err := config.LoadWorkerConfig(findServerCmd(t, "worker"))
		require.NoError(t, err)
		process, err := config.LoadServerProcessConfig(findServerCmd(t, "worker"))
		require.NoError(t, err)
		assert.Nil(t, app.HostedActions(slog.Default(), &hosteduse.MockStore{}, process.GetControlPlane(), worker))
	})
}
