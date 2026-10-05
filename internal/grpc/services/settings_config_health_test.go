package services

import (
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A legacy-format reliant key is silently dropped from the available drivers;
// config health must tell the user to reconnect rather than leave models
// vanishing unexplained.
func TestGetConfigHealth_ReportsStaleReliantKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	find := func() *reliantv1.ConfigError {
		resp, err := svc.GetConfigHealth(ctx, connect.NewRequest(&reliantv1.GetConfigHealthRequest{}))
		require.NoError(t, err)
		for _, e := range resp.Msg.Errors {
			if e.Source == "provider:reliant" {
				return e
			}
		}
		return nil
	}

	assert.Nil(t, find(), "no key stored: nothing to report")

	require.NoError(t, repo.SetProviderAPIKey(ctx, "test-user", "reliant", "rlnt_abcdef0123456789"))
	got := find()
	require.NotNil(t, got, "legacy key must be reported")
	assert.Contains(t, got.Message, "Reconnect")
	assert.NotContains(t, got.Message, "rlnt_", "never echo the key")
}
