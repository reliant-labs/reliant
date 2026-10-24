// Copyright (c) 2025 Reliant Labs

package app

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/hosteduse"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/pkg/hostedhttp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// Hosted (a control plane is configured): GitHub tokens come from
// control-plane, through the delegated source.
func TestIntegrationCredentials_HostedUsesControlPlane(t *testing.T) {
	src, err := newWorkerIntegrationCredentials(nil, controlplane.Endpoint{URL: "http://admin-server:8090", InternalServiceSecret: "s"})
	require.NoError(t, err)
	cs, ok := src.(connauth.Service)
	require.True(t, ok)
	_, registered := cs.Broker(ghdelegated.BrokerID)
	assert.True(t, registered, "hosted registers the control-plane GitHub broker under the id the github manifest names")
}

// Hosted without the secret is a boot error, never a silent fall-back to
// reliant's own GitHub provider — which would tell every hosted user their
// GitHub is not connected.
func TestIntegrationCredentials_HostedWithoutSecretFailsBoot(t *testing.T) {
	_, err := newWorkerIntegrationCredentials(nil, controlplane.Endpoint{URL: "http://admin-server:8090"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INTERNAL_SERVICE_SECRET")
}

// Self-hosted: saved connections only; `github` is reliant's own provider.
func TestIntegrationCredentials_SelfHostedIsSavedConnections(t *testing.T) {
	src, err := newWorkerIntegrationCredentials(nil, controlplane.Endpoint{})
	require.NoError(t, err)
	cs, ok := src.(connauth.Service)
	require.True(t, ok)
	_, registered := cs.Broker(ghdelegated.BrokerID)
	assert.False(t, registered, "self-hosted has no control plane to delegate to")
}

// The poll activity is handed the same source integration actions use, so a
// poll resolves a credential exactly as an action node would.
func TestTriggerCredentialsIsTheIntegrationSource(t *testing.T) {
	src, err := newWorkerIntegrationCredentials(nil, controlplane.Endpoint{})
	require.NoError(t, err)
	got := triggerCredentials(src)
	require.NotNil(t, got)
	assert.Same(t, src, got)
	assert.Nil(t, triggerCredentials(nil), "no source, no poll credentials: polls skip")
}

// Every go: executor a shipped manifest names is registered in the worker
// binary (gmail's send and get, linked in through webhook), so the boot check
// passes — and would fail boot, not a user's first call, if one went missing.
func TestEveryShippedExecutorIsRegistered(t *testing.T) {
	require.NoError(t, checkExecutors())
}

// The broker id the worker registers is the one the embedded github manifest
// declares; a rename on either side would silently disable hosted GitHub.
func TestGitHubManifestNamesTheRegisteredBroker(t *testing.T) {
	a, err := catalog.MustBuiltin().Resolve("github/user.get@1")
	require.NoError(t, err)
	m, ok := manifest.Method(a.Manifest.GetConnection(), manifest.AuthDelegated)
	require.True(t, ok)
	assert.Equal(t, ghdelegated.BrokerID, m.GetDelegated().GetBroker())
}

// The worker's search_integrations reports GitHub as connected exactly when
// this worker registered the control-plane broker its action node would
// authenticate through, and never otherwise.
func TestCatalogSearch_DelegatedFollowsTheWorkersBrokers(t *testing.T) {
	ctx := context.Background()
	var noConnections catalogindex.ConnectionLister

	hosted, err := newWorkerIntegrationCredentials(nil, controlplane.Endpoint{URL: "http://admin-server:8090", InternalServiceSecret: "s"})
	require.NoError(t, err)
	svc, err := newWorkerCatalogSearch(noConnections, hosted)
	require.NoError(t, err)
	usable, err := svc.Usable(ctx, "")
	require.NoError(t, err)
	assert.True(t, usable["github"], "hosted: GitHub is served by the control-plane broker")

	selfHosted, err := newWorkerIntegrationCredentials(nil, controlplane.Endpoint{})
	require.NoError(t, err)
	svc, err = newWorkerCatalogSearch(noConnections, selfHosted)
	require.NoError(t, err)
	usable, err = svc.Usable(ctx, "")
	require.NoError(t, err)
	assert.False(t, usable["github"], "self-hosted: GitHub needs a saved connection")
}

// A refused dedicated hosted-action endpoint never stops the whole worker.
// Cluster DNS does not waive HTTPS; the general API URL never enables actions.
func TestHostedActionsDegradeInsteadOfFailingTheWorker(t *testing.T) {
	store := &hosteduse.MockStore{}
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))

	assert.Nil(t, HostedActions(log, store, controlplane.Endpoint{}, "", ""), "self-hosted: no control plane, no hosted actions")
	assert.Empty(t, logged.String(), "no control plane is not an error")

	refused := HostedActions(log, store, controlplane.Endpoint{URL: "http://admin-server.ns.svc.cluster.local:8090", InternalServiceSecret: "s"}, "http://controller.ns.svc.cluster.local:9443", "")
	require.NotNil(t, refused)
	assert.Contains(t, logged.String(), "level=WARN")
	assert.Contains(t, logged.String(), "hosted HTTP actions are DISABLED")
	assert.Contains(t, logged.String(), "requires HTTPS")
	_, err := refused.ExecuteAction(context.Background(), "run", "node", "grant", hostedhttp.Request{URL: "https://example.com", Method: "POST"})
	var disabled *handlers.HostedDisabledError
	require.ErrorAs(t, err, &disabled)
	assert.Contains(t, err.Error(), "requires HTTPS")
	assert.Nil(t, HostedActions(log, store, controlplane.Endpoint{URL: "http://admin-server.ns.svc.cluster.local:8090", InternalServiceSecret: "s"}, "", ""), "general control-plane URL must not enable hosted actions")

	for _, u := range []string{"https://controller.ns.svc.cluster.local:9443", "http://127.0.0.1:8090"} {
		logged.Reset()
		enabled := HostedActions(log, store, controlplane.Endpoint{InternalServiceSecret: "s"}, u, "")
		require.NotNil(t, enabled, u)
		_, isDisabled := enabled.(disabledHostedActions)
		assert.False(t, isDisabled, "%s gets the real client", u)
		assert.Empty(t, logged.String(), u)
	}
}
