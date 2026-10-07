// Copyright (c) 2025 Reliant Labs

package serverapi

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/integrations/ghusers"
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

// The "Only from" lookup is wired exactly when GitHub triggers are, over the
// same token source, and classifies its errors for the handler.
func TestWireGitHubSenders(t *testing.T) {
	dirs, err := wireGitHubSenders(nil, nil, envOf(nil))
	require.NoError(t, err)
	assert.Nil(t, dirs, "no GitHub triggers on this server: nobody to look up")

	dirs, err = wireGitHubSenders(nil, nil, envOf(map[string]string{
		"RELIANT_GITHUB_WEBHOOK_SECRET": "s", "RELIANT_CONTROL_PLANE_URL": "http://cp", "INTERNAL_SERVICE_SECRET": "x",
	}))
	require.NoError(t, err)
	require.Contains(t, dirs, "github")

	g := gitHubSenders{}
	assert.True(t, g.IsPermanent(fmt.Errorf("wrapped: %w", ghaccess.ErrNotConnected)), "connect GitHub")
	assert.True(t, g.IsPermanent(ghaccess.ErrNeedsReconnect))
	assert.True(t, g.IsPermanent(fmt.Errorf("x: %w", ghusers.ErrTokenRejected)), "GitHub refused the token: reconnect")
	assert.False(t, g.IsPermanent(errors.New("GitHub answered 502")), "transient: retry")
	assert.True(t, g.IsInvalid(ghusers.ErrTooManyQueries))
	assert.False(t, g.IsInvalid(ghaccess.ErrNotConnected))
}
