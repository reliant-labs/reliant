package drivers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCodexJWT(t *testing.T, exp time.Time) string {
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

func TestBuildAvailableDrivers_CodexUsesPersistedAccessToken(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "test-user"
	validToken := makeCodexJWT(t, time.Now().Add(1*time.Hour))

	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "codex", "oauth"))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  validToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	cfg, ok := availableDrivers.Drivers["codex"]
	require.True(t, ok, "codex should be available when oauth token is persisted")
	assert.Equal(t, validToken, cfg.APIKey)
	assert.True(t, cfg.Enabled)
}

func TestBuildAvailableDrivers_CodexExpiredTokenIsSkipped(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "test-user"
	expiredToken := makeCodexJWT(t, time.Now().Add(-10*time.Minute))

	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "codex", "oauth"))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  expiredToken,
		RefreshToken: "refresh-token",
		AccountID:    "test-account-id",
	}))
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "openai", "sk-openai-test"))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	_, hasCodex := availableDrivers.Drivers["codex"]
	assert.False(t, hasCodex, "codex should be skipped when access token is expired")

	openAI, hasOpenAI := availableDrivers.Drivers["openai"]
	require.True(t, hasOpenAI, "non-codex provider should still be available")
	assert.Equal(t, "sk-openai-test", openAI.APIKey)
}

func TestBuildAvailableDrivers_CodexMarkerWithoutTokensIsSkipped(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "test-user"

	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "codex", "oauth"))
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "anthropic", "sk-ant-test"))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	_, hasCodex := availableDrivers.Drivers["codex"]
	assert.False(t, hasCodex, "codex should be skipped when marker exists but tokens are missing")

	anthropic, hasAnthropic := availableDrivers.Drivers["anthropic"]
	require.True(t, hasAnthropic)
	assert.Equal(t, "sk-ant-test", anthropic.APIKey)
}
