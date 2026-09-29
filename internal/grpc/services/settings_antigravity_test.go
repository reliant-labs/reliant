package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type antigravityDisconnectRepo struct {
	db.Repository

	deletedTokensForUser  string
	deletedMarkerForUser  string
	deletedMarkerProvider string
	refetchType           db.RefetchType
}

func (r *antigravityDisconnectRepo) DeleteAntigravityAuthTokens(_ context.Context, userID string) error {
	r.deletedTokensForUser = userID
	return nil
}

func (r *antigravityDisconnectRepo) DeleteProviderAPIKey(_ context.Context, userID string, provider string) error {
	r.deletedMarkerForUser = userID
	r.deletedMarkerProvider = provider
	return nil
}

func (r *antigravityDisconnectRepo) EmitUserRefetch(_ context.Context, _ string, refetchType db.RefetchType, _ db.RefetchOpts) error {
	r.refetchType = refetchType
	return nil
}

func TestSettingsService_UpdateProviderAPIKey_AntigravityDisconnectRemovesMarkerAndTokens(t *testing.T) {
	repo := &antigravityDisconnectRepo{}
	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	resp, err := svc.UpdateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.UpdateProviderAPIKeyRequest{
		Provider: "antigravity",
		ApiKey:   "",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	assert.Equal(t, "Disconnected from Antigravity", resp.Msg.Message)
	assert.Equal(t, "test-user", repo.deletedTokensForUser)
	assert.Equal(t, "test-user", repo.deletedMarkerForUser)
	assert.Equal(t, "antigravity", repo.deletedMarkerProvider)
	assert.Equal(t, db.RefetchConfigHealth, repo.refetchType)
}
