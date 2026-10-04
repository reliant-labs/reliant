// Copyright (c) 2025 Reliant Labs

package serverworker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
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
	assert.IsType(t, &ghdelegated.Source{}, src)
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
	assert.IsType(t, &connauth.Source{}, src)
}
