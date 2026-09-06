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

// An expired access token is NOT a reason to drop Codex: the driver's
// transport refreshes it before the request, exactly as it does for Claude.
// Dropping the provider here is what made an expired token fail the whole
// workflow ("401 token_expired") while a perfectly good refresh token sat
// unused in the database.
func TestBuildAvailableDrivers_CodexExpiredTokenIsRefreshable(t *testing.T) {
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

	cfg, hasCodex := availableDrivers.Drivers["codex"]
	require.True(t, hasCodex, "codex must stay available when an expired token can be refreshed")
	assert.Equal(t, "refresh-token", cfg.RefreshToken,
		"the refresh token must reach the driver or the transport cannot refresh")
	assert.False(t, cfg.TokenExpiresAt.IsZero(),
		"expiry must be derived from the JWT so the transport knows to refresh")

	openAI, hasOpenAI := availableDrivers.Drivers["openai"]
	require.True(t, hasOpenAI, "non-codex provider should still be available")
	assert.Equal(t, "sk-openai-test", openAI.APIKey)
}

// Without a refresh token there is nothing to recover with, so an expired
// session is genuinely unusable and Codex is dropped.
func TestBuildAvailableDrivers_CodexExpiredWithoutRefreshTokenIsSkipped(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "test-user"
	expiredToken := makeCodexJWT(t, time.Now().Add(-10*time.Minute))

	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "codex", "oauth"))
	require.NoError(t, repo.SetCodexAuthTokens(ctx, userID, db.CodexAuthTokens{
		AccessToken:  expiredToken,
		RefreshToken: "",
		AccountID:    "test-account-id",
	}))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	_, hasCodex := availableDrivers.Drivers["codex"]
	assert.False(t, hasCodex, "codex should be skipped when expired and unrecoverable")
}

func TestBuildAvailableDrivers_ReliantConfiguredViaProviderAPIKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	t.Setenv("RELIANT_API_BASE_URL", "https://proxy.example.com/v1")

	ctx := context.Background()
	userID := "test-user"

	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "reliant", "rlnt_abcdef0123456789"))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	cfg, ok := availableDrivers.Drivers["reliant"]
	require.True(t, ok, "reliant should be available when rlnt_ key is persisted")
	assert.Equal(t, "rlnt_abcdef0123456789", cfg.APIKey)
	assert.Equal(t, "https://proxy.example.com/v1", cfg.BaseURL)
	assert.True(t, cfg.Enabled)
}

func TestBuildAvailableDrivers_ReliantNotConfiguredWithoutKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	userID := "test-user"

	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "anthropic", "sk-ant-test"))

	availableDrivers, err := BuildAvailableDrivers(ctx, repo, userID)
	require.NoError(t, err)

	_, hasReliant := availableDrivers.Drivers["reliant"]
	assert.False(t, hasReliant, "reliant should not be available without a stored API key")
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
