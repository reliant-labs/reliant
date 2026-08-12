package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCodexJWTForSettingsTest(t *testing.T, exp time.Time) string {
	t.Helper()

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := map[string]interface{}{
		"exp": exp.Unix(),
		"https://api.openai.com/auth": map[string]interface{}{
			"chatgpt_account_id": "test-account-id",
		},
	}
	claimsJSON, err := json.Marshal(claims)
	require.NoError(t, err)

	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signature := base64.RawURLEncoding.EncodeToString([]byte("test-signature"))
	return header + "." + payload + "." + signature
}

func newSettingsServiceTestContext() context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
}

func findProviderStatus(t *testing.T, statuses []*reliantv1.ProviderStatus, provider string) *reliantv1.ProviderStatus {
	t.Helper()
	for _, status := range statuses {
		if status.Provider == provider {
			return status
		}
	}
	t.Fatalf("provider %q not found", provider)
	return nil
}

func TestSettingsService_UpdateProviderAPIKey_CodexRejectsManualKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	_, err := svc.UpdateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.UpdateProviderAPIKeyRequest{
		Provider: "codex",
		ApiKey:   "manual-key-not-allowed",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "does not use manual API keys")
}

func TestSettingsService_UpdateProviderAPIKey_CodexDisconnectRemovesMarkerAndTokens(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	userID := "test-user"

	validToken := makeCodexJWTForSettingsTest(t, time.Now().Add(1*time.Hour))
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "codex", "oauth"))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  validToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	resp, err := svc.UpdateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.UpdateProviderAPIKeyRequest{
		Provider: "codex",
		ApiKey:   "",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	assert.Equal(t, "Disconnected from Codex", resp.Msg.Message)

	tokens, err := repo.GetCodexAuthTokens(ctx, userID)
	require.NoError(t, err)
	assert.Nil(t, tokens)

	keys, err := repo.GetProviderAPIKeys(ctx, userID)
	require.NoError(t, err)
	_, hasCodex := keys["codex"]
	assert.False(t, hasCodex)
}

func TestSettingsService_ValidateProviderAPIKey_CodexStatusFromPersistedTokens(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	userID := "test-user"

	// Missing tokens => not connected
	missingResp, err := svc.ValidateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.ValidateProviderAPIKeyRequest{
		Provider: "codex",
	}))
	require.NoError(t, err)
	assert.False(t, missingResp.Msg.Valid)
	assert.Equal(t, "Codex is not connected", missingResp.Msg.Message)

	// Expired token => expired message
	expiredToken := makeCodexJWTForSettingsTest(t, time.Now().Add(-10*time.Minute))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  expiredToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	expiredResp, err := svc.ValidateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.ValidateProviderAPIKeyRequest{
		Provider: "codex",
	}))
	require.NoError(t, err)
	assert.False(t, expiredResp.Msg.Valid)
	assert.Contains(t, expiredResp.Msg.Message, "session expired")

	// Valid token => connected
	validToken := makeCodexJWTForSettingsTest(t, time.Now().Add(1*time.Hour))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  validToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	validResp, err := svc.ValidateProviderAPIKey(ctx, connect.NewRequest(&reliantv1.ValidateProviderAPIKeyRequest{
		Provider: "codex",
	}))
	require.NoError(t, err)
	assert.True(t, validResp.Msg.Valid)
	assert.Equal(t, "Connected to Codex", validResp.Msg.Message)
}

func TestSettingsService_GetProviderStatuses_CodexUsesPersistedTokenExpiry(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()
	userID := "test-user"

	validToken := makeCodexJWTForSettingsTest(t, time.Now().Add(1*time.Hour))
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "codex", "oauth"))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  validToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	resp, err := svc.GetProviderStatuses(ctx, connect.NewRequest(&reliantv1.GetProviderStatusesRequest{}))
	require.NoError(t, err)

	codexStatus := findProviderStatus(t, resp.Msg.Providers, "codex")
	assert.True(t, codexStatus.Configured)
	assert.True(t, codexStatus.HasApiKey)
	assert.Nil(t, codexStatus.MaskedKey)

	expiredToken := makeCodexJWTForSettingsTest(t, time.Now().Add(-10*time.Minute))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  expiredToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	expiredResp, err := svc.GetProviderStatuses(ctx, connect.NewRequest(&reliantv1.GetProviderStatusesRequest{}))
	require.NoError(t, err)

	expiredCodexStatus := findProviderStatus(t, expiredResp.Msg.Providers, "codex")
	assert.False(t, expiredCodexStatus.Configured)
	assert.False(t, expiredCodexStatus.HasApiKey)
	assert.Nil(t, expiredCodexStatus.MaskedKey)
}
