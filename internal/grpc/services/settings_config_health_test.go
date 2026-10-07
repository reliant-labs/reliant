package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/accesstoken"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
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
	assert.Contains(t, got.Message, "cannot replace it automatically", "no healer installed: say why")
}

type stubMinter struct {
	key string
	err error
}

func (m stubMinter) MintForUser(context.Context, accesstokenclient.MintRequest) (accesstokenclient.Minted, error) {
	return accesstokenclient.Minted{Plaintext: m.key}, m.err
}

func TestGetConfigHealth_HealsLegacyKeyAndReportsOnlyOnFailure(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	t.Cleanup(func() { drivers.SetReliantKeyHealer(nil) })

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	reliantError := func() *reliantv1.ConfigError {
		resp, err := svc.GetConfigHealth(ctx, connect.NewRequest(&reliantv1.GetConfigHealthRequest{}))
		require.NoError(t, err)
		for _, e := range resp.Msg.Errors {
			if e.Source == "provider:reliant" {
				return e
			}
		}
		return nil
	}
	require.NoError(t, repo.SetProviderAPIKey(ctx, "test-user", "reliant", "rlnt_abcdef0123456789"))

	drivers.SetReliantKeyHealer(drivers.NewReliantKeyHealer(repo, stubMinter{err: errors.New("control-plane 503")}))
	got := reliantError()
	require.NotNil(t, got)
	assert.Contains(t, got.Message, "control-plane 503", "failure must say what failed")

	validKey := accesstoken.Prefix + strings.Repeat("a", accesstoken.TokenLen-len(accesstoken.Prefix))
	drivers.SetReliantKeyHealer(drivers.NewReliantKeyHealer(repo, stubMinter{key: validKey}))
	assert.Nil(t, reliantError(), "healed: no message")
	stored, err := repo.GetProviderAPIKey(ctx, "test-user", "reliant")
	require.NoError(t, err)
	assert.Equal(t, validKey, stored)
}
