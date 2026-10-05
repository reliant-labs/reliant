// Copyright (c) 2025 Reliant Labs

package serverworker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// Hosted (a control plane is configured): GitHub tokens come from
// control-plane, through the delegated source.
func TestIntegrationCredentials_HostedUsesControlPlane(t *testing.T) {
	src, err := newIntegrationCredentials(nil, envOf(map[string]string{
		"RELIANT_CONTROL_PLANE_URL": "http://admin-server:8090",
		"INTERNAL_SERVICE_SECRET":   "s",
	}))
	require.NoError(t, err)
	cs, ok := src.(*connauth.Source)
	require.True(t, ok)
	_, registered := cs.Brokers().Get(ghdelegated.BrokerID)
	assert.True(t, registered, "hosted registers the control-plane GitHub broker under the id the github manifest names")
}

// Hosted without the secret is a boot error, never a silent fall-back to
// reliant's own GitHub provider — which would tell every hosted user their
// GitHub is not connected.
func TestIntegrationCredentials_HostedWithoutSecretFailsBoot(t *testing.T) {
	_, err := newIntegrationCredentials(nil, envOf(map[string]string{
		"RELIANT_CONTROL_PLANE_URL": "http://admin-server:8090",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INTERNAL_SERVICE_SECRET")
}

// Self-hosted: saved connections only; `github` is reliant's own provider.
func TestIntegrationCredentials_SelfHostedIsSavedConnections(t *testing.T) {
	src, err := newIntegrationCredentials(nil, envOf(nil))
	require.NoError(t, err)
	cs, ok := src.(*connauth.Source)
	require.True(t, ok)
	_, registered := cs.Brokers().Get(ghdelegated.BrokerID)
	assert.False(t, registered, "self-hosted has no control plane to delegate to")
}

// The poll activity is handed the same source integration actions use, so a
// poll resolves a credential exactly as an action node would.
func TestTriggerCredentialsIsTheIntegrationSource(t *testing.T) {
	src, err := newIntegrationCredentials(nil, envOf(nil))
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
