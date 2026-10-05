// Copyright (c) 2025 Reliant Labs

package serverworker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
)

// The worker's search_integrations reports GitHub as connected exactly when
// this worker registered the control-plane broker its action node would
// authenticate through, and never otherwise.
func TestCatalogSearch_DelegatedFollowsTheWorkersBrokers(t *testing.T) {
	ctx := context.Background()
	var noConnections catalogindex.ConnectionLister

	hosted, err := newIntegrationCredentials(nil, envOf(map[string]string{
		"RELIANT_CONTROL_PLANE_URL": "http://admin-server:8090",
		"INTERNAL_SERVICE_SECRET":   "s",
	}))
	require.NoError(t, err)
	svc, err := newCatalogSearch(noConnections, hosted)
	require.NoError(t, err)
	usable, err := svc.Usable(ctx, "")
	require.NoError(t, err)
	assert.True(t, usable["github"], "hosted: GitHub is served by the control-plane broker")

	selfHosted, err := newIntegrationCredentials(nil, envOf(nil))
	require.NoError(t, err)
	svc, err = newCatalogSearch(noConnections, selfHosted)
	require.NoError(t, err)
	usable, err = svc.Usable(ctx, "")
	require.NoError(t, err)
	assert.False(t, usable["github"], "self-hosted: GitHub needs a saved connection")
}
