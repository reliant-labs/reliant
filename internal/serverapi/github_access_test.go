// Copyright (c) 2025 Reliant Labs

package serverapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
)

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestWireGitHubAccess(t *testing.T) {
	r, err := wireGitHubAccess(nil, nil, envOf(nil))
	require.NoError(t, err)
	assert.Nil(t, r, "no webhook secret: GitHub events never arrive, so nothing to keep fresh")

	_, err = wireGitHubAccess(nil, nil, envOf(map[string]string{
		"RELIANT_GITHUB_WEBHOOK_SECRET": "s", "RELIANT_CONTROL_PLANE_URL": "http://cp",
	}))
	require.Error(t, err, "hosted without the internal-service secret must not boot")
	assert.Contains(t, err.Error(), "INTERNAL_SERVICE_SECRET")

	r, err = wireGitHubAccess(nil, nil, envOf(map[string]string{
		"RELIANT_GITHUB_WEBHOOK_SECRET": "s", "RELIANT_CONTROL_PLANE_URL": "http://cp", "INTERNAL_SERVICE_SECRET": "x",
	}))
	require.NoError(t, err)
	require.NotNil(t, r, "hosted: tokens are delegated by control-plane")

	tokens, err := gitHubTokenSource(nil, nil, envOf(map[string]string{"RELIANT_CONTROL_PLANE_URL": "http://cp", "INTERNAL_SERVICE_SECRET": "x"}))
	require.NoError(t, err)
	assert.IsType(t, &ghaccess.DelegatedTokens{}, tokens)

	_, err = gitHubTokenSource(nil, nil, envOf(nil))
	assert.Error(t, err, "self-hosted needs the database for saved connections")
}
